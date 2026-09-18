// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	projectedBodySize = 10
	twoProjections    = 2
)

var projectedBody = strings.Repeat("0", projectedBodySize)

func projectedLayer(t *testing.T) Layer {
	t.Helper()
	layer := preparedLayer(t)
	if _, err := (Layers{Scratch: layer}).Put(key(1), key(2),
		strings.NewReader(projectedBody), projectedBodySize, faultsMoment); err != nil {
		t.Fatal(err)
	}
	return layer
}

func TestProjectingANativeCacheRefusesThePairsItCannotWorkWith(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, test := range []struct {
		name        string
		base        string
		destination string
		want        string
	}{
		{name: "no source at all", destination: root, want: "requires source and destination"},
		{name: "no destination at all", base: root, want: "requires source and destination"},
		{name: "one directory for both", base: root, destination: root, want: "are the same directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := projectNative(test.base, test.destination, faultsMoment, false, layerHooks{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("projecting %s reported %v, want %q", test.name, err, test.want)
			}
		})
	}
}

func TestProjectingANativeCacheSkipsEveryEntryItCannotTrust(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		action   string
		record   string
		object   string
		projects bool
	}{
		{
			name:   "an action of an identity and an object of the size it names",
			action: hex.EncodeToString(key(1)), record: `{"output":"` + "%s" + `","size":10}`,
			object: projectedBody, projects: true,
		},
		{
			name: "an action of no size at all", action: hex.EncodeToString(key(1)),
			record: `{"output":"` + "%s" + `","size":0}`, object: "", projects: true,
		},
		{
			name: "an action whose name is not an identity", action: "aa",
			record: `{"output":"` + "%s" + `","size":10}`, object: projectedBody,
		},
		{
			name: "an action whose output is not an identity", action: hex.EncodeToString(key(1)),
			record: `{"output":"zz","size":10}`, object: projectedBody,
		},
		{
			name: "an action of a size below zero", action: hex.EncodeToString(key(1)),
			record: `{"output":"` + "%s" + `","size":-1}`, object: projectedBody,
		},
		{
			name: "an action whose object is not there", action: hex.EncodeToString(key(1)),
			record: `{"output":"` + "%s" + `","size":10}`,
		},
		{
			name: "an action whose object is another size", action: hex.EncodeToString(key(1)),
			record: `{"output":"` + "%s" + `","size":10}`, object: projectedBody + "0",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := preparedLayer(t)
			output := hex.EncodeToString(key(2))
			record := strings.Replace(test.record, "%s", output, 1)
			writeStoredFile(t, filepath.Join(layer.Dir, actionsDirectory,
				test.action[:entryPrefixHexDigits], test.action), record)
			if test.object != "" {
				writeStoredFile(t, layer.objectPath(key(2)), test.object)
			} else if strings.Contains(test.record, `"size":0`) {
				writeStoredFile(t, layer.objectPath(key(2)), "")
			}
			seed, err := projectNative(layer.Dir, filepath.Join(t.TempDir(), "native"),
				faultsMoment, false, layerHooks{})
			if err != nil {
				t.Fatalf("projecting %s reported %v", test.name, err)
			}
			if projected := seed.Actions == 1; projected != test.projects {
				t.Fatalf("projecting %s answered %+v, want projected=%t", test.name, seed, test.projects)
			}
			if !test.projects && seed.Skipped != 1 {
				t.Errorf("projecting %s counted %d skipped, want one", test.name, seed.Skipped)
			}
		})
	}
}

func TestProjectingANativeCacheLinksOneObjectForEveryActionThatNamesIt(t *testing.T) {
	t.Parallel()
	layer := preparedLayer(t)
	for _, action := range []byte{1, 3} {
		if _, err := (Layers{Scratch: layer}).Put(key(action), key(2),
			strings.NewReader(projectedBody), projectedBodySize, faultsMoment); err != nil {
			t.Fatal(err)
		}
	}
	seed, err := projectNative(layer.Dir, filepath.Join(t.TempDir(), "native"), faultsMoment, false, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if seed.Actions != twoProjections {
		t.Fatalf("the seed projected %d actions, want both that name the object", seed.Actions)
	}
	if seed.Objects != 1 || seed.Bytes != projectedBodySize {
		t.Fatalf("the seed linked %d objects of %d bytes, want the one they share", seed.Objects, seed.Bytes)
	}
}

func TestProjectingANativeCacheKeepsAProjectionItAlreadyMade(t *testing.T) {
	t.Parallel()
	layer := projectedLayer(t)
	destination := filepath.Join(t.TempDir(), "native")
	if _, err := projectNative(layer.Dir, destination, faultsMoment, false, layerHooks{}); err != nil {
		t.Fatal(err)
	}
	wrote := 0
	hooks := layerHooks{writeFile: func(path string, data []byte, perm os.FileMode) error {
		if strings.HasSuffix(path, "-a") {
			wrote++
		}
		return os.WriteFile(path, data, perm)
	}}
	seed, err := projectNative(layer.Dir, destination, faultsMoment, false, hooks)
	if err != nil {
		t.Fatalf("projecting over what it already made reported %v", err)
	}
	if seed.Actions != 1 {
		t.Fatalf("the second projection counted %d actions, want the one it already made", seed.Actions)
	}
	if wrote != 0 {
		t.Fatalf("the second projection rewrote %d actions, want it to leave them alone", wrote)
	}
}

func TestProjectingANativeCacheRefusesAnActionAnotherHandWrote(t *testing.T) {
	t.Parallel()
	for _, repair := range []bool{false, true} {
		t.Run(map[bool]string{false: "a seed", true: "a refresh"}[repair], func(t *testing.T) {
			t.Parallel()
			layer := projectedLayer(t)
			destination := filepath.Join(t.TempDir(), "native")
			if err := prepareNativeCache(destination, faultsMoment, layerHooks{}.resolved()); err != nil {
				t.Fatal(err)
			}
			actionPath := nativeCachePath(destination, hex.EncodeToString(key(1)), "a")
			writeStoredFile(t, actionPath, "v1 written by another hand\n")
			seed, err := projectNative(layer.Dir, destination, faultsMoment, repair, layerHooks{})
			if !repair {
				if err == nil || !strings.Contains(err.Error(), "is not the expected projection") {
					t.Fatalf("a seed over an action another hand wrote reported %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("a refresh over an action another hand wrote reported %v", err)
			}
			if seed.Actions != 1 {
				t.Fatalf("a refresh counted %d actions, want the one it replaced", seed.Actions)
			}
		})
	}
}

func TestProjectingANativeCacheReportsEveryStepItCouldNotTake(t *testing.T) {
	t.Parallel()
	failure := errors.New("the step failed")
	for _, test := range []struct {
		name  string
		hooks layerHooks
		want  string
	}{
		{
			name:  "a source layer it cannot list",
			hooks: layerHooks{readDir: func(string) ([]os.DirEntry, error) { return nil, failure }},
			want:  "read build cache layer",
		},
		{
			name: "an object it cannot inspect",
			hooks: layerHooks{stat: func(path string) (fs.FileInfo, error) {
				if strings.Contains(path, string(filepath.Separator)+objectsDirectory+string(filepath.Separator)) {
					return nil, failure
				}
				return os.Stat(path)
			}},
			want: "read build cache object",
		},
		{
			name:  "an object it cannot link",
			hooks: layerHooks{link: func(string, string) error { return failure }},
			want:  "hard-link native build cache object",
		},
		{
			name: "an action it cannot inspect",
			hooks: layerHooks{lstat: func(path string) (fs.FileInfo, error) {
				if strings.HasSuffix(path, "-a") {
					return nil, failure
				}
				return os.Lstat(path)
			}},
			want: "inspect native build cache action",
		},
		{
			name: "an action it cannot write",
			hooks: layerHooks{writeFile: func(path string, data []byte, perm os.FileMode) error {
				if strings.HasSuffix(path, "-a") {
					return failure
				}
				return os.WriteFile(path, data, perm)
			}},
			want: "write native build cache action",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layer := projectedLayer(t)
			_, err := projectNative(layer.Dir, filepath.Join(t.TempDir(), "native"),
				faultsMoment, false, test.hooks)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("projecting past %s reported %v, want %q and %v", test.name, err, test.want, failure)
			}
		})
	}
}

func TestProjectingANativeCacheReportsAnActionItCouldNotReplace(t *testing.T) {
	t.Parallel()
	failure := errors.New("the action could not be replaced")
	layer := projectedLayer(t)
	destination := filepath.Join(t.TempDir(), "native")
	if err := prepareNativeCache(destination, faultsMoment, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, nativeCachePath(destination, hex.EncodeToString(key(1)), "a"), "v1 another hand\n")
	hooks := layerHooks{removeAll: func(string) error { return failure }}
	_, err := projectNative(layer.Dir, destination, faultsMoment, true, hooks)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "replace native build cache action") {
		t.Fatalf("a refresh reported %v, want the replacement it could not make", err)
	}
}

func TestProjectingANativeCacheRecordsNoMomentBeforeTheEpoch(t *testing.T) {
	t.Parallel()
	layer := projectedLayer(t)
	ancient := time.Unix(0, 0).Add(-time.Hour)
	if err := os.Chtimes(layer.actionPath(key(1)), ancient, ancient); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "native")
	if _, err := projectNative(layer.Dir, destination, faultsMoment, false, layerHooks{}); err != nil {
		t.Fatal(err)
	}
	record, err := os.ReadFile(nativeCachePath(destination, hex.EncodeToString(key(1)), "a"))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(record))
	if fields[nativeActionTimestampField] != "0" {
		t.Fatalf("the action records the moment %q, want the epoch for one written before it",
			fields[nativeActionTimestampField])
	}
}

func TestProjectingANativeCacheRecordsTheMomentTheActionCarries(t *testing.T) {
	t.Parallel()
	layer := projectedLayer(t)
	written := time.Unix(0, 0).Add(time.Hour)
	if err := os.Chtimes(layer.actionPath(key(1)), written, written); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "native")
	if _, err := projectNative(layer.Dir, destination, faultsMoment, false, layerHooks{}); err != nil {
		t.Fatal(err)
	}
	record, err := os.ReadFile(nativeCachePath(destination, hex.EncodeToString(key(1)), "a"))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(record))
	if stamp := fields[nativeActionTimestampField]; stamp != strconv.FormatInt(written.UnixNano(), 10) {
		t.Fatalf("the action records the moment %q, want %d", stamp, written.UnixNano())
	}
}

func TestWhatASeedProjectedIsWhatAPersistenceSkips(t *testing.T) {
	t.Parallel()
	for _, again := range []bool{false, true} {
		t.Run(map[bool]string{false: "a projection it made", true: "a projection it found"}[again],
			func(t *testing.T) {
				t.Parallel()
				seedProjectedThenPersisted(t, again)
			})
	}
}

func seedProjectedThenPersisted(t *testing.T, again bool) {
	t.Helper()
	layer := projectedLayer(t)
	native := filepath.Join(t.TempDir(), "native")
	if again {
		if _, err := projectNative(layer.Dir, native, faultsMoment, false, layerHooks{}); err != nil {
			t.Fatal(err)
		}
	}
	seed, err := projectNative(layer.Dir, native, faultsMoment, false, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if seed.Actions != 1 {
		t.Fatalf("the seed projected %+v, want the one action the layer holds", seed)
	}
	base := preparedLayer(t)
	persisted, err := persistNativeWithHooks(base.Dir, native, seed, faultsMoment, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Actions != 0 || persisted.Objects != 0 {
		t.Fatalf("persisting what the seed projected answered %+v, want it all skipped", persisted)
	}
}
