// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const (
	twoNativeObjects  = 2
	threeNativeBodies = 3 * projectedBodySize
)

func nativeBodyOf(fill byte) string {
	return strings.Repeat(string(rune('a'+fill%26)), projectedBodySize)
}

func nativeEntryAged(t *testing.T, native string, fill byte, aged time.Duration) []byte {
	t.Helper()
	outputID := storeNativeEntry(t, native, key(fill), nativeBodyOf(fill))
	stamp := collectMoment.Add(-aged)
	for _, path := range []string{
		nativeCachePath(native, hex.EncodeToString(key(fill)), "a"),
		nativeCachePath(native, hex.EncodeToString(outputID), "d"),
	} {
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	return outputID
}

func TestCollectingANativeCacheRefusesABoundBelowZero(t *testing.T) {
	t.Parallel()
	if _, err := collectNativeWithHooks(t.TempDir(), -1, layerHooks{}); err == nil {
		t.Fatal("a bound below zero was accepted")
	}
}

func TestCollectingANativeCacheLeavesEverythingUnderItsBound(t *testing.T) {
	t.Parallel()
	native := filepath.Join(t.TempDir(), "native")
	if err := prepareNativeCache(native, collectMoment, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	nativeEntryAged(t, native, 1, time.Hour)
	for _, bound := range []int64{0, projectedBodySize, projectedBodySize + 1} {
		collected, err := collectNativeWithHooks(native, bound, layerHooks{})
		if err != nil {
			t.Fatal(err)
		}
		if collected.RemovedObjects != 0 || collected.AfterBytes != projectedBodySize {
			t.Fatalf("collecting to %d answered %+v, want everything left", bound, collected)
		}
	}
}

func TestCollectingANativeCacheRemovesTheOldestObjectAndTheActionsThatNameIt(t *testing.T) {
	t.Parallel()
	native := filepath.Join(t.TempDir(), "native")
	if err := prepareNativeCache(native, collectMoment, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	oldest := nativeEntryAged(t, native, 0xaa, 2*time.Hour)
	newest := nativeEntryAged(t, native, 0xbb, time.Hour)

	collected, err := collectNativeWithHooks(native, projectedBodySize, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if collected.BeforeBytes != twoNativeObjects*projectedBodySize {
		t.Fatalf("the collection read %d bytes, want both objects", collected.BeforeBytes)
	}
	if collected.RemovedObjects != 1 || collected.RemovedActions != 1 {
		t.Fatalf("the collection removed %+v, want the oldest object and its action", collected)
	}
	if collected.AfterBytes != projectedBodySize || collected.RemovedBytes != projectedBodySize {
		t.Errorf("the collection left %d bytes and removed %d, want the size of one object each",
			collected.AfterBytes, collected.RemovedBytes)
	}
	if _, err := os.Stat(nativeCachePath(native, hex.EncodeToString(oldest), "d")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the oldest object survived: %v", err)
	}
	if _, err := os.Stat(nativeCachePath(native, hex.EncodeToString(newest), "d")); err != nil {
		t.Errorf("the newest object was removed too: %v", err)
	}
}

func TestCollectingANativeCacheReadsAnObjectsAgeFromTheNewestActionThatNamesIt(t *testing.T) {
	t.Parallel()
	native := filepath.Join(t.TempDir(), "native")
	if err := prepareNativeCache(native, collectMoment, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	old := nativeEntryAged(t, native, 0xaa, 3*time.Hour)
	nativeEntryAged(t, native, 0xbb, 2*time.Hour)
	second := nativeCachePath(native, hex.EncodeToString(key(0xcc)), "a")
	writeStoredFile(t, second, "v1 "+hex.EncodeToString(key(0xcc))+" "+hex.EncodeToString(old)+" 10 1\n")
	if err := os.Chtimes(second, collectMoment, collectMoment); err != nil {
		t.Fatal(err)
	}

	collected, err := collectNativeWithHooks(native, projectedBodySize, layerHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if collected.RemovedObjects != 1 {
		t.Fatalf("the collection removed %+v, want one object", collected)
	}
	if _, err := os.Stat(nativeCachePath(native, hex.EncodeToString(old), "d")); err != nil {
		t.Fatalf("the object a fresh action still names was removed: %v", err)
	}
}

func TestCollectingANativeCacheReportsWhatItCouldNotRemove(t *testing.T) {
	t.Parallel()
	failure := errors.New("the entry could not be removed")
	for _, test := range []struct {
		name   string
		suffix string
		want   string
	}{
		{name: "an object", suffix: "-d", want: "collect native build cache object"},
		{name: "an action", suffix: "-a", want: "collect native build cache action"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			native := filepath.Join(t.TempDir(), "native")
			if err := prepareNativeCache(native, collectMoment, layerHooks{}.resolved()); err != nil {
				t.Fatal(err)
			}
			nativeEntryAged(t, native, 0xaa, time.Hour)
			refuse := func(path string) error {
				if strings.HasSuffix(path, test.suffix) {
					return failure
				}
				return os.Remove(path)
			}
			hooks := layerHooks{remove: refuse, removeAll: refuse}
			_, err := collectNativeWithHooks(native, 1, hooks)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("collecting %s reported %v, want %q and %v", test.name, err, test.want, failure)
			}
		})
	}
}

func TestCollectingANativeCacheForgivesAnEntrySomebodyRemovedFirst(t *testing.T) {
	t.Parallel()
	native := filepath.Join(t.TempDir(), "native")
	if err := prepareNativeCache(native, collectMoment, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	nativeEntryAged(t, native, 0xaa, time.Hour)
	gone := func(string) error { return os.ErrNotExist }
	hooks := layerHooks{remove: gone, removeAll: gone}
	collected, err := collectNativeWithHooks(native, 1, hooks)
	if err != nil {
		t.Fatalf("collecting past an entry somebody removed first reported %v", err)
	}
	if collected.RemovedObjects != 1 || collected.RemovedActions != 1 {
		t.Fatalf("the collection counted %+v, want what it went to remove", collected)
	}
}

func TestInspectingANativeCacheRefusesTheShapesItIsNot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "file")
	writeStoredFile(t, file, "")
	native := filepath.Join(root, "native")
	if err := prepareNativeCache(native, collectMoment, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(native, "00")); err != nil {
		t.Fatal(err)
	}
	writeStoredFile(t, filepath.Join(native, "00"), "")
	for _, test := range []struct {
		name      string
		directory string
		want      string
	}{
		{name: "a root that is not there", directory: filepath.Join(root, "absent"),
			want: "inspect native build cache root"},
		{name: "a root that is a file", directory: file, want: "is not a directory"},
		{name: "a prefix that is a file", directory: native, want: "prefix"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := inspectNativeCache(test.directory, layerHooks{}.resolved())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("inspecting %s reported %v, want %q", test.name, err, test.want)
			}
		})
	}
}

func TestInspectingANativeCacheReadsOnlyTheEntriesItWrote(t *testing.T) {
	t.Parallel()
	native := filepath.Join(t.TempDir(), "native")
	if err := prepareNativeCache(native, collectMoment, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	outputID := nativeEntryAged(t, native, 0xaa, time.Hour)
	prefix := filepath.Join(native, "aa")
	writeStoredFile(t, filepath.Join(prefix, "not-an-entry"), "")
	writeStoredFile(t, filepath.Join(prefix, "zz-d"), "")
	writeStoredFile(t, filepath.Join(prefix, "zz-a"),
		"v1 zz "+hex.EncodeToString(outputID)+" 10 1\n")
	if err := os.MkdirAll(filepath.Join(prefix, hex.EncodeToString(key(0xdd))+"-a"),
		filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}

	actions, objects, err := inspectNativeCache(native, layerHooks{}.resolved())
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].name != hex.EncodeToString(outputID) {
		t.Fatalf("the inspection read %+v, want the one object it wrote", objects)
	}
	named := actions[hex.EncodeToString(outputID)]
	if len(named) != 1 {
		t.Fatalf("the inspection read %v for the object, want the one action that names it", named)
	}
	if slices.Contains(named, filepath.Join(prefix, "zz-a")) {
		t.Errorf("the inspection read an action whose name is not an identity: %v", named)
	}
}

func TestInspectingANativeCacheReadsAPrefixItNeverMade(t *testing.T) {
	t.Parallel()
	native := filepath.Join(t.TempDir(), "native")
	if err := os.MkdirAll(filepath.Join(native, "aa"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	outputID := nativeEntryAged(t, native, 0xaa, time.Hour)
	_, objects, err := inspectNativeCache(native, layerHooks{}.resolved())
	if err != nil {
		t.Fatalf("inspecting a cache of one prefix reported %v, want nothing", err)
	}
	if len(objects) != 1 || objects[0].name != hex.EncodeToString(outputID) {
		t.Fatalf("the inspection read %+v, want the one object under the prefix it has", objects)
	}
}

func TestInspectingANativeCacheKeepsAnObjectYoungerThanTheActionThatNamesIt(t *testing.T) {
	t.Parallel()
	native := filepath.Join(t.TempDir(), "native")
	if err := prepareNativeCache(native, collectMoment, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	outputID := nativeEntryAged(t, native, 0xaa, time.Hour)
	object := nativeCachePath(native, hex.EncodeToString(outputID), "d")
	if err := os.Chtimes(object, collectMoment, collectMoment); err != nil {
		t.Fatal(err)
	}
	_, objects, err := inspectNativeCache(native, layerHooks{}.resolved())
	if err != nil {
		t.Fatal(err)
	}
	if !objects[0].modified.Equal(collectMoment) {
		t.Fatalf("the object reads %s, want its own moment rather than the older action's",
			objects[0].modified)
	}
}

func TestInspectingANativeCacheForgivesAnActionThatVanished(t *testing.T) {
	t.Parallel()
	native := filepath.Join(t.TempDir(), "native")
	if err := prepareNativeCache(native, collectMoment, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	nativeEntryAged(t, native, 0xaa, time.Hour)
	hooks := layerHooks{stat: func(path string) (fs.FileInfo, error) {
		if strings.HasSuffix(path, "-a") {
			return nil, os.ErrNotExist
		}
		return os.Stat(path)
	}}
	if _, _, err := inspectNativeCache(native, hooks.resolved()); err != nil {
		t.Fatalf("inspecting past an action that vanished reported %v, want nothing", err)
	}
}

func TestInspectingANativeCacheReportsEveryReadItCouldNotFinish(t *testing.T) {
	t.Parallel()
	failure := errors.New("the cache could not be read")
	for _, test := range []struct {
		name  string
		hooks layerHooks
		want  string
	}{
		{
			name: "a prefix it cannot inspect",
			hooks: layerHooks{lstat: func(path string) (fs.FileInfo, error) {
				if filepath.Base(path) == "aa" {
					return nil, failure
				}
				return os.Lstat(path)
			}},
			want: "inspect native build cache",
		},
		{
			name: "a prefix it cannot read",
			hooks: layerHooks{readDir: func(path string) ([]os.DirEntry, error) {
				if filepath.Base(path) == "aa" {
					return nil, failure
				}
				return os.ReadDir(path)
			}},
			want: "inspect native build cache",
		},
		{
			name: "an object it cannot describe",
			hooks: layerHooks{readDir: func(path string) ([]os.DirEntry, error) {
				if filepath.Base(path) != "aa" {
					return os.ReadDir(path)
				}
				return []os.DirEntry{stubDirEntry{
					name: hex.EncodeToString(key(0xbb)) + "-d", infoErr: failure,
				}}, nil
			}},
			want: "inspect native build cache object",
		},
		{
			name: "an action it cannot inspect",
			hooks: layerHooks{stat: func(path string) (fs.FileInfo, error) {
				if strings.HasSuffix(path, "-a") {
					return nil, failure
				}
				return os.Stat(path)
			}},
			want: "inspect native build cache action",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			native := filepath.Join(t.TempDir(), "native")
			if err := prepareNativeCache(native, collectMoment, layerHooks{}.resolved()); err != nil {
				t.Fatal(err)
			}
			nativeEntryAged(t, native, 0xaa, time.Hour)
			_, _, err := inspectNativeCache(native, test.hooks.resolved())
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("inspecting past %s reported %v, want %q and %v", test.name, err, test.want, failure)
			}
		})
	}
}

type directoryEntryInfo struct{ stubLayerInfo }

func (directoryEntryInfo) IsDir() bool { return true }

type directoryDirEntry struct{ name string }

func (entry directoryDirEntry) Name() string               { return entry.name }
func (entry directoryDirEntry) IsDir() bool                { return false }
func (entry directoryDirEntry) Type() os.FileMode          { return 0 }
func (entry directoryDirEntry) Info() (os.FileInfo, error) { return directoryEntryInfo{}, nil }

func TestInspectingANativeCacheReportsAnObjectTreeItCannotWalk(t *testing.T) {
	t.Parallel()
	native := filepath.Join(t.TempDir(), "native")
	if err := prepareNativeCache(native, collectMoment, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	hooks := layerHooks{readDir: func(path string) ([]os.DirEntry, error) {
		if filepath.Base(path) != "aa" {
			return os.ReadDir(path)
		}
		return []os.DirEntry{directoryDirEntry{name: hex.EncodeToString(key(0xbb)) + "-d"}}, nil
	}}
	_, _, err := inspectNativeCache(native, hooks.resolved())
	if err == nil || !strings.Contains(err.Error(), "inspect native build cache executable") {
		t.Fatalf("inspecting an object tree that is not there reported %v", err)
	}
}

func TestANativeObjectSizeCountsEveryFileBeneathIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "file")
	writeStoredFile(t, file, projectedBody)
	info, err := os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}
	size, err := nativeObjectSize(file, info)
	if err != nil || size != projectedBodySize {
		t.Fatalf("a file counted (%d, %v), want %d", size, err, projectedBodySize)
	}

	directory := filepath.Join(root, "directory")
	for _, name := range []string{"one", filepath.Join("nested", "two"), filepath.Join("nested", "three")} {
		writeStoredFile(t, filepath.Join(directory, name), projectedBody)
	}
	info, err = os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	size, err = nativeObjectSize(directory, info)
	if err != nil || size != threeNativeBodies {
		t.Fatalf("a directory counted (%d, %v), want %d", size, err, threeNativeBodies)
	}

	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeObjectSize(directory, info); err == nil {
		t.Fatal("a directory that is no longer there was counted")
	}
}
