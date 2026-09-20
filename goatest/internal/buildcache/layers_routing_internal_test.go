// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const (
	routingObjectSize = 10

	outputIdentityHexDigits = 2 * 32
)

var routingBody = strings.Repeat("0", routingObjectSize)

func preparedLayer(t *testing.T) Layer {
	t.Helper()
	layer := Layer{Dir: t.TempDir()}
	if err := layer.Prepare(); err != nil {
		t.Fatal(err)
	}
	return layer
}

func failingObjectStat(failure error) layerHooks {
	return countingObjectStat(failure, new(int))
}

func countingObjectStat(failure error, asked *int) layerHooks {
	return layerHooks{stat: func(path string) (fs.FileInfo, error) {
		if strings.Contains(path, string(filepath.Separator)+objectsDirectory+string(filepath.Separator)) {
			*asked++
			return nil, failure
		}
		return os.Stat(path)
	}}
}

func TestReadingAnEntryReportsAnObjectItCouldNotInspect(t *testing.T) {
	t.Parallel()
	layer := preparedLayer(t)
	layers := Layers{Scratch: layer}
	if _, err := layers.Put(key(1), key(2), strings.NewReader(routingBody), routingObjectSize, faultsMoment); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("the object could not be inspected")
	entry, source, err := layers.getWithHooks(key(1), faultsMoment, failingObjectStat(failure))
	if !errors.Is(err, failure) {
		t.Fatalf("Get reported %v, want %v", err, failure)
	}
	if source != SourceNone || entry.Size != 0 {
		t.Errorf("a read that failed answered (%+v, %s), want nothing", entry, source)
	}
}

func TestWritingAnEntryReportsAnObjectItCouldNotInspect(t *testing.T) {
	t.Parallel()
	layer := preparedLayer(t)
	layers := Layers{Scratch: layer}
	failure := errors.New("the object could not be inspected")
	asked := 0
	entry, err := layers.putWithHooks(key(1), key(2), strings.NewReader(routingBody),
		routingObjectSize, faultsMoment, countingObjectStat(failure, &asked))
	if !errors.Is(err, failure) {
		t.Fatalf("Put reported %v, want %v", err, failure)
	}
	if entry.Size != 0 {
		t.Errorf("a write that failed answered %+v, want nothing", entry)
	}
	if asked != 1 {
		t.Errorf("the write asked for the object %d times, want it to stop at the layer that would not answer", asked)
	}
}

func TestWritingAnEntryReusesAnObjectALayerAlreadyHolds(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		persist bool
		holder  Source
	}{
		{name: "a persisting run reading the base layer", persist: true, holder: SourceBase},
		{name: "a scratch run reading the scratch layer", holder: SourceScratch},
		{name: "a scratch run reading the base layer", holder: SourceBase},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layers := Layers{Scratch: preparedLayer(t), Base: preparedLayer(t), Persist: test.persist}
			holder := layers.layer(test.holder)
			if _, err := (Layers{Scratch: holder}).Put(key(9), key(2),
				strings.NewReader(routingBody), routingObjectSize, faultsMoment); err != nil {
				t.Fatal(err)
			}
			entry, err := layers.Put(key(1), key(2), failingReader{err: errors.New("the body was read")},
				routingObjectSize, faultsMoment)
			if err != nil {
				t.Fatalf("Put reported %v, want the object the layer already holds reused", err)
			}
			if entry.Size != routingObjectSize || entry.DiskPath != holder.objectPath(key(2)) {
				t.Fatalf("Put answered %+v, want the object at %s", entry, holder.objectPath(key(2)))
			}
		})
	}
}

func TestReadingAnEntrySkipsARecordWhoseOutputIdentityDoesNotDecode(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		output string
	}{
		{
			name:   "an output that is not hexadecimal at all",
			output: "zz" + strings.Repeat("0", outputIdentityHexDigits-len("zz")),
		},
		{
			name:   "an output that decodes to a prefix and then stops",
			output: "00zz" + strings.Repeat("0", outputIdentityHexDigits-len("00zz")),
		},
		{name: "an output of an odd number of digits", output: "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := preparedLayer(t)
			layers := Layers{Scratch: layer}
			writeActionRecord(t, layer, key(1), actionRecord{Output: test.output, Size: 1})
			plantObject(t, layer, []byte{0x00}, "x")

			entry, source, err := layers.Get(key(1), faultsMoment)
			if err != nil {
				t.Fatalf("Get reported %v, want the record skipped", err)
			}
			if source != SourceNone || entry.Size != 0 {
				t.Fatalf("Get answered (%+v, %s), want nothing for a record it cannot read", entry, source)
			}
		})
	}
}

func writeActionRecord(t *testing.T, layer Layer, actionID []byte, record actionRecord) {
	t.Helper()
	path := layer.actionPath(actionID)
	if err := os.MkdirAll(filepath.Dir(path), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
}

func plantObject(t *testing.T, layer Layer, outputID []byte, body string) {
	t.Helper()
	name := hex.EncodeToString(outputID)
	if len(name) < entryPrefixHexDigits {
		name = strings.Repeat("0", entryPrefixHexDigits)
	}
	path := filepath.Join(layer.Dir, objectsDirectory, name[:entryPrefixHexDigits], name)
	if err := os.MkdirAll(filepath.Dir(path), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
}
