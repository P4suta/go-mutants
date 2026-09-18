// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func storeNativeEntry(t *testing.T, root string, actionID []byte, body string) []byte {
	t.Helper()
	output := sha256.Sum256([]byte(body))
	actionName, outputName := hex.EncodeToString(actionID), hex.EncodeToString(output[:])
	for _, name := range []string{actionName, outputName} {
		if err := os.MkdirAll(filepath.Join(root, name[:entryPrefixHexDigits]), filemode.ReadableDirectory); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(nativeCachePath(root, outputName, "d"), []byte(body), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	record := fmt.Sprintf("v1 %s %s %d %d\n", actionName, outputName, len(body), faultsMoment.UnixNano())
	if err := os.WriteFile(nativeCachePath(root, actionName, "a"), []byte(record), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	return output[:]
}

func TestReadingAnEntryReportsANativeImportItCouldNotFinish(t *testing.T) {
	t.Parallel()
	layer := preparedLayer(t)
	native := t.TempDir()
	storeNativeEntry(t, native, key(1), routingBody)
	layers := Layers{Scratch: layer, NativeSource: native}
	failure := errors.New("the object could not be inspected")

	entry, source, err := layers.getWithHooks(key(1), faultsMoment, failingObjectStat(failure))
	if !errors.Is(err, failure) {
		t.Fatalf("Get reported %v, want %v", err, failure)
	}
	if source != SourceNone || entry.Size != 0 {
		t.Errorf("an import that failed answered (%+v, %s), want nothing", entry, source)
	}
}

func TestANativeImportLinksAnObjectTheLayersAlreadyHold(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		persist bool
		holder  Source
	}{
		{name: "a persisting run whose base already holds it", persist: true, holder: SourceBase},
		{name: "a scratch run whose scratch already holds it", holder: SourceScratch},
		{name: "a scratch run whose base already holds it", holder: SourceBase},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layers := Layers{Scratch: preparedLayer(t), Base: preparedLayer(t),
				Persist: test.persist, NativeSource: t.TempDir()}
			outputID := storeNativeEntry(t, layers.NativeSource, key(1), routingBody)
			holder := layers.layer(test.holder)
			if _, err := (Layers{Scratch: holder}).Put(key(9), outputID,
				strings.NewReader(routingBody), routingObjectSize, faultsMoment); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(nativeCachePath(layers.NativeSource,
				hex.EncodeToString(outputID), "d")); err != nil {
				t.Fatal(err)
			}

			entry, source, err := layers.Get(key(1), faultsMoment)
			if err != nil {
				t.Fatalf("Get reported %v, want the object the layers already hold linked", err)
			}
			if source != SourceNative {
				t.Fatalf("Get answered %s, want %s", source, SourceNative)
			}
			if entry.DiskPath != holder.objectPath(outputID) {
				t.Errorf("the import points at %s, want the object at %s", entry.DiskPath, holder.objectPath(outputID))
			}
		})
	}
}

type endingReader struct {
	body string
	read bool
}

func (reader *endingReader) Read(destination []byte) (int, error) {
	if reader.read {
		return 0, io.EOF
	}
	reader.read = true
	return copy(destination, reader.body), io.EOF
}

func TestANativeImportAsksTheFilesystemNothingItCannotUse(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		source   string
		actionID []byte
	}{
		{name: "a run with no native source", actionID: key(1)},
		{name: "an action with no identity at all", source: "native"},
		{name: "an action identity of another length", source: "native", actionID: []byte{1, 2, 3}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			hooks := layerHooks{lstat: func(path string) (fs.FileInfo, error) {
				t.Errorf("%s reached %s", test.name, path)
				return nil, os.ErrNotExist
			}}
			layers := Layers{Scratch: preparedLayer(t), NativeSource: test.source}
			entry, found, err := layers.importNative(test.actionID, faultsMoment, hooks.resolved())
			if found || err != nil {
				t.Fatalf("%s imported (%+v, %t, %v), want nothing", test.name, entry, found, err)
			}
		})
	}
}

func TestANativeImportStopsAtTheFirstLayerItCouldNotRead(t *testing.T) {
	t.Parallel()
	failure := errors.New("the object could not be inspected")
	layers := Layers{Scratch: preparedLayer(t), NativeSource: t.TempDir()}
	storeNativeEntry(t, layers.NativeSource, key(1), routingBody)
	asked := 0
	hooks := countingObjectStat(failure, &asked)
	if _, found, err := layers.importNative(key(1), faultsMoment, hooks.resolved()); !errors.Is(err, failure) || found {
		t.Fatalf("an import reported (%t, %v), want %v", found, err, failure)
	}
	if asked != 1 {
		t.Fatalf("the import asked for the object %d times, want it to stop at the layer that would not answer", asked)
	}
}

func TestANativeImportCopiesNothingForAnObjectItCannotOpen(t *testing.T) {
	t.Parallel()
	layers := Layers{Scratch: preparedLayer(t), NativeSource: t.TempDir()}
	outputID := storeNativeEntry(t, layers.NativeSource, key(1), routingBody)
	if err := os.Remove(nativeCachePath(layers.NativeSource, hexOf(outputID), "d")); err != nil {
		t.Fatal(err)
	}
	hooks := layerHooks{createTemporary: func(string, string) (layerWritableFile, error) {
		t.Error("an object the native cache could not open was copied anyway")
		return nil, errors.New("no temporary")
	}}
	entry, found, err := layers.importNative(key(1), faultsMoment, hooks.resolved())
	if found || err != nil {
		t.Fatalf("an import of an object that is not there answered (%+v, %t, %v), want nothing", entry, found, err)
	}
}

func TestANativeImportReportsAStoreItCouldNotFinish(t *testing.T) {
	t.Parallel()
	failure := errors.New("the object could not be stored")
	layers := Layers{Scratch: preparedLayer(t), NativeSource: t.TempDir()}
	storeNativeEntry(t, layers.NativeSource, key(1), routingBody)
	hooks := layerHooks{mkdirAll: func(string, os.FileMode) error { return failure }}
	if _, found, err := layers.importNative(key(1), faultsMoment, hooks.resolved()); !errors.Is(err, failure) || found {
		t.Fatalf("an import reported (%t, %v), want %v", found, err, failure)
	}
}

func TestAVerifiedNativeReaderReportsWhatItReadAlongWithTheEnd(t *testing.T) {
	t.Parallel()
	digest := sha256.Sum256([]byte(routingBody))
	for _, test := range []struct {
		name     string
		expected []byte
		ends     bool
	}{
		{name: "a body its digest names", expected: digest[:], ends: true},
		{name: "a body its digest does not name", expected: make([]byte, sha256.Size)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader := &verifiedNativeReader{
				source: &endingReader{body: routingBody}, digest: sha256.New(), expected: test.expected,
			}
			destination := make([]byte, len(routingBody))
			read, err := reader.Read(destination)
			if read != len(routingBody) {
				t.Fatalf("%s read %d bytes, want the %d it was given", test.name, read, len(routingBody))
			}
			if test.ends {
				if !errors.Is(err, io.EOF) || reader.invalid {
					t.Fatalf("%s reported (%v, invalid=%t), want the end", test.name, err, reader.invalid)
				}
				return
			}
			if err == nil || !reader.invalid {
				t.Fatalf("%s reported (%v, invalid=%t), want it refused", test.name, err, reader.invalid)
			}
		})
	}
}

func hexOf(identifier []byte) string { return hex.EncodeToString(identifier) }
