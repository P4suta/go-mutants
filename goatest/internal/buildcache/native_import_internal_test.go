// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
