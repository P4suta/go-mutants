// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func nativeCacheOfOne(t *testing.T) (native string, outputID []byte) {
	t.Helper()
	native = filepath.Join(t.TempDir(), "native")
	if err := prepareNativeCache(native, faultsMoment, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	return native, storeNativeEntry(t, native, key(1), projectedBody)
}

func TestPersistingANativeCacheRefusesThePairsItCannotWorkWith(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "file")
	writeStoredFile(t, file, "")
	for _, test := range []struct {
		name   string
		base   string
		source string
		want   string
	}{
		{name: "no destination at all", source: root, want: "requires source and destination"},
		{name: "no source at all", base: root, want: "requires source and destination"},
		{name: "one directory for both", base: root, source: root, want: "are the same directory"},
		{name: "a source that is not there", base: root, source: filepath.Join(root, "absent"),
			want: "inspect native build cache persistence source"},
		{name: "a source that is a file", base: root, source: file, want: "is not a directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := persistNativeWithHooks(test.base, test.source, NativeSeed{}, faultsMoment, layerHooks{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("persisting %s reported %v, want %q", test.name, err, test.want)
			}
		})
	}
}

func TestPersistingANativeCacheDefersToTheRunThatHoldsTheBase(t *testing.T) {
	t.Parallel()
	native, _ := nativeCacheOfOne(t)
	base := preparedLayer(t)
	hooks := layerHooks{lockFile: func(*os.File) (bool, error) { return false, nil }}
	persisted, err := persistNativeWithHooks(base.Dir, native, NativeSeed{}, faultsMoment, hooks)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Deferred || persisted.Actions != 0 {
		t.Fatalf("persisting under another run's lock answered %+v, want it deferred", persisted)
	}
}

func TestPersistingANativeCacheReportsALockItCouldNotTake(t *testing.T) {
	t.Parallel()
	failure := errors.New("the lock could not be taken")
	native, _ := nativeCacheOfOne(t)
	base := preparedLayer(t)
	hooks := layerHooks{lockFile: func(*os.File) (bool, error) { return false, failure }}
	if _, err := persistNativeWithHooks(base.Dir, native, NativeSeed{}, faultsMoment, hooks); !errors.Is(err, failure) {
		t.Fatalf("persisting reported %v, want %v", err, failure)
	}
}

func TestPersistingANativeCacheTakesEveryActionItCanAndSkipsTheRest(t *testing.T) {
	t.Parallel()
	native, outputID := nativeCacheOfOne(t)
	prefix := filepath.Join(native, hex.EncodeToString(key(1))[:entryPrefixHexDigits])
	writeStoredFile(t, filepath.Join(prefix, "not-an-action"), "")
	writeStoredFile(t, filepath.Join(prefix, "zz-a"), "v1 zz zz 1 1\n")
	if err := os.MkdirAll(filepath.Join(prefix, "directory-a"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}

	base := preparedLayer(t)
	persisted, err := persistNativeWithHooks(base.Dir, native, NativeSeed{}, faultsMoment, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Actions != 1 || persisted.Objects != 1 || persisted.Bytes != projectedBodySize {
		t.Fatalf("persisting answered %+v, want the one action it can read", persisted)
	}
	if persisted.Skipped != 1 {
		t.Errorf("persisting counted %d skipped, want the action whose name is not an identity", persisted.Skipped)
	}
	if _, statErr := os.Stat(base.objectPath(outputID)); statErr != nil {
		t.Errorf("the object it says it persisted is not there: %v", statErr)
	}
}

func TestPersistingANativeCacheKeepsAnActionTheBaseAlreadyHolds(t *testing.T) {
	t.Parallel()
	native, _ := nativeCacheOfOne(t)
	base := preparedLayer(t)
	first, err := persistNativeWithHooks(base.Dir, native, NativeSeed{}, faultsMoment, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Actions != 1 {
		t.Fatalf("the first persistence answered %+v, want the action it took", first)
	}
	second, err := persistNativeWithHooks(base.Dir, native, NativeSeed{}, faultsMoment, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Actions != 0 || second.Objects != 0 || second.Skipped != 0 {
		t.Fatalf("the second persistence answered %+v, want it to leave what the first left", second)
	}
}

func TestPersistingANativeCacheTakesAnActionWhoseObjectWentMissing(t *testing.T) {
	t.Parallel()
	native, outputID := nativeCacheOfOne(t)
	base := preparedLayer(t)
	if _, err := persistNativeWithHooks(base.Dir, native, NativeSeed{}, faultsMoment, layerHooks{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(base.objectPath(outputID)); err != nil {
		t.Fatal(err)
	}
	again, err := persistNativeWithHooks(base.Dir, native, NativeSeed{}, faultsMoment, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Objects != 1 {
		t.Fatalf("persisting over a base whose object went missing answered %+v, want it linked again", again)
	}
}

func TestPersistingANativeCacheReportsEveryStepItCouldNotTake(t *testing.T) {
	t.Parallel()
	failure := errors.New("the step failed")
	for _, test := range []struct {
		name   string
		hooks  layerHooks
		want   string
		seeded bool
	}{
		{
			name: "a prefix it cannot inspect",
			hooks: layerHooks{lstat: func(path string) (fs.FileInfo, error) {
				if filepath.Base(path) == "aa" {
					return nil, failure
				}
				return os.Lstat(path)
			}},
			want: "inspect native build cache persistence source",
		},
		{
			name: "a prefix it cannot read",
			hooks: layerHooks{readDir: func(path string) ([]os.DirEntry, error) {
				if filepath.Base(path) == "aa" {
					return nil, failure
				}
				return os.ReadDir(path)
			}},
			want: "inspect native build cache persistence source",
		},
		{
			name: "an action of the base it cannot read",
			hooks: layerHooks{readFile: func(path string) ([]byte, error) {
				if strings.Contains(path, string(filepath.Separator)+actionsDirectory+string(filepath.Separator)) {
					return nil, failure
				}
				return os.ReadFile(path)
			}},
			want: "read build cache action", seeded: true,
		},
		{
			name:  "an object it cannot link",
			hooks: layerHooks{link: func(string, string) error { return failure }},
			want:  "persist native build cache object",
		},
		{
			name: "an action of the base it cannot write",
			hooks: layerHooks{createTemporary: func(directory, pattern string) (layerWritableFile, error) {
				if strings.HasPrefix(pattern, ".action-") {
					return nil, failure
				}
				return os.CreateTemp(directory, pattern)
			}},
			want: "write build cache action",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			native, _ := nativeCacheOfOne(t)
			base := preparedLayer(t)
			if test.seeded {
				if _, err := persistNativeWithHooks(base.Dir, native, NativeSeed{},
					faultsMoment, layerHooks{}); err != nil {
					t.Fatal(err)
				}
			}
			_, err := persistNativeWithHooks(base.Dir, native, NativeSeed{}, faultsMoment, test.hooks)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("persisting past %s reported %v, want %q and %v", test.name, err, test.want, failure)
			}
		})
	}
}

func TestPersistingANativeCacheRefusesAPrefixThatIsNotADirectoryOfItsOwn(t *testing.T) {
	t.Parallel()
	native := filepath.Join(t.TempDir(), "native")
	if err := os.MkdirAll(native, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, filepath.Join(native, "00"), "")
	base := preparedLayer(t)
	_, err := persistNativeWithHooks(base.Dir, native, NativeSeed{}, faultsMoment, layerHooks{})
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("persisting over a prefix that is a file reported %v", err)
	}
}

func TestPersistingANativeCacheReadsTheMomentItWasGivenOrTheOneItRuns(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		now  time.Time
	}{
		{name: "a moment it was given", now: faultsMoment},
		{name: "no moment at all", now: time.Time{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			native, outputID := nativeCacheOfOne(t)
			base := preparedLayer(t)
			if _, err := persistNativeWithHooks(base.Dir, native, NativeSeed{}, test.now, layerHooks{}); err != nil {
				t.Fatal(err)
			}
			record, _, err := base.readAction(key(1), layerHooks{}.resolved())
			if err != nil {
				t.Fatal(err)
			}
			if record.Output != hex.EncodeToString(outputID) {
				t.Fatalf("the base holds %+v, want the action it persisted", record)
			}
			if test.now.IsZero() {
				if record.Time.IsZero() {
					t.Fatal("the action it persisted without a clock carries no moment at all")
				}
				return
			}
			if !record.Time.Equal(test.now) {
				t.Fatalf("the action carries %s, want the moment it was given, %s", record.Time, test.now)
			}
		})
	}
}

func TestPersistingANativeCacheRecordsWhatItTookSoTheNextRunSkipsIt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		seeded bool
	}{
		{name: "an action it took"},
		{name: "an action the base already held", seeded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			native, _ := nativeCacheOfOne(t)
			base := preparedLayer(t)
			if test.seeded {
				if _, err := persistNativeWithHooks(base.Dir, native, NativeSeed{},
					faultsMoment, layerHooks{}); err != nil {
					t.Fatal(err)
				}
			}
			baseline := NativeSeed{actions: make(map[string]bool)}
			if _, err := persistNativeWithHooks(base.Dir, native, baseline, faultsMoment, layerHooks{}); err != nil {
				t.Fatal(err)
			}
			if !baseline.actions[hex.EncodeToString(key(1))] {
				t.Fatalf("persisting %s recorded %v, want the action it need not look at again",
					test.name, baseline.actions)
			}
		})
	}
}
