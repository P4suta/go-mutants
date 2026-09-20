// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const (
	nativeObjectBody = "0123456789"

	inspectionsBeforeTheLink = 2
)

func nativeSourceFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source")
	writeStoredFile(t, path, body)
	return path
}

func failingLstat(failure error, when func(path string) bool) layerHooks {
	return layerHooks{lstat: func(path string) (fs.FileInfo, error) {
		if when(path) {
			return nil, failure
		}
		return os.Lstat(path)
	}}
}

func TestAFileIsRegularOnlyAtTheSizeItWasAskedFor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "file")
	writeStoredFile(t, file, nativeObjectBody)
	directory := filepath.Join(root, "directory")
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		path  string
		size  int64
		valid bool
	}{
		{name: "a file of the size it was asked for", path: file, size: int64(len(nativeObjectBody)), valid: true},
		{name: "a file of another size", path: file, size: 1},
		{name: "a directory", path: directory, size: 0},
		{name: "a path that is not there", path: filepath.Join(root, "absent")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			valid, err := regularFileWithSize(test.path, test.size, layerHooks{}.resolved())
			if err != nil {
				t.Fatalf("inspecting %s reported %v, want nothing", test.name, err)
			}
			if valid != test.valid {
				t.Fatalf("inspecting %s answered %t, want %t", test.name, valid, test.valid)
			}
		})
	}
	failure := errors.New("the object could not be inspected")
	hooks := failingLstat(failure, func(string) bool { return true }).resolved()
	valid, err := regularFileWithSize(file, 0, hooks)
	if valid || !errors.Is(err, failure) {
		t.Fatalf("inspecting a file it cannot reach answered (%t, %v), want %v", valid, err, failure)
	}
	if !strings.Contains(err.Error(), "inspect build cache object") {
		t.Errorf("the failure reads %v, want it to name the inspection", err)
	}
}

func TestPersistingAnObjectAnswersWhatItDidWithIt(t *testing.T) {
	t.Parallel()
	size := int64(len(nativeObjectBody))
	for _, test := range []struct {
		name        string
		body        string
		destination string
		want        persistedObject
		fails       string
	}{
		{name: "a source that is not there", want: objectUnusable},
		{name: "a source of another size", body: nativeObjectBody + "0", want: objectUnusable},
		{name: "a destination that is already the object", body: nativeObjectBody,
			destination: nativeObjectBody, want: objectPresent},
		{name: "a destination that is something else", body: nativeObjectBody,
			destination: "other", want: objectUnusable, fails: "has unexpected contents"},
		{name: "a destination that is not there yet", body: nativeObjectBody, want: objectLinked},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if test.body != "" {
				writeStoredFile(t, source, test.body)
			}
			destination := filepath.Join(root, "objects", "aa", "object")
			if test.destination != "" {
				writeStoredFile(t, destination, test.destination)
			}
			persisted, err := persistNativeObject(source, destination, size, faultsMoment, layerHooks{}.resolved())
			if test.fails != "" {
				if err == nil || !strings.Contains(err.Error(), test.fails) {
					t.Fatalf("persisting %s reported %v, want %q", test.name, err, test.fails)
				}
			} else if err != nil {
				t.Fatalf("persisting %s reported %v, want nothing", test.name, err)
			}
			if persisted != test.want {
				t.Fatalf("persisting %s answered %d, want %d", test.name, persisted, test.want)
			}
			if test.want == objectLinked {
				if _, statErr := os.Stat(destination); statErr != nil {
					t.Errorf("the object it says it linked is not there: %v", statErr)
				}
			}
		})
	}
}

func TestPersistingAnObjectReportsEveryStepItCouldNotTake(t *testing.T) {
	t.Parallel()
	failure := errors.New("the step failed")
	size := int64(len(nativeObjectBody))
	for _, test := range []struct {
		name  string
		hooks func(source, destination string) layerHooks
		want  string
	}{
		{
			name: "a source it cannot inspect",
			hooks: func(source, _ string) layerHooks {
				return failingLstat(failure, func(path string) bool { return path == source })
			},
			want: "inspect build cache object",
		},
		{
			name: "a destination that changed under it",
			hooks: func(_, destination string) layerHooks {
				seen := 0
				return layerHooks{lstat: func(path string) (fs.FileInfo, error) {
					if path != destination {
						return os.Lstat(path)
					}
					seen++
					if seen == 1 {
						return nil, os.ErrNotExist
					}
					return nil, failure
				}}
			},
			want: "inspect persistent build cache object",
		},
		{
			name: "a destination it cannot inspect",
			hooks: func(_, destination string) layerHooks {
				return failingLstat(failure, func(path string) bool { return path == destination })
			},
			want: "inspect build cache object",
		},
		{
			name: "a directory it cannot make",
			hooks: func(string, string) layerHooks {
				return layerHooks{mkdirAll: func(string, os.FileMode) error { return failure }}
			},
			want: "create persistent build cache object directory",
		},
		{
			name: "a source it cannot retain",
			hooks: func(string, string) layerHooks {
				return layerHooks{chtimes: func(string, time.Time, time.Time) error { return failure }}
			},
			want: "retain native build cache object",
		},
		{
			name: "an object it cannot link",
			hooks: func(string, string) layerHooks {
				return layerHooks{link: func(string, string) error { return failure }}
			},
			want: "persist native build cache object",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := filepath.Join(root, "source")
			writeStoredFile(t, source, nativeObjectBody)
			destination := filepath.Join(root, "objects", "aa", "object")
			persisted, err := persistNativeObject(source, destination, size, faultsMoment,
				test.hooks(source, destination).resolved())
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("persisting past %s reported %v, want %q and %v", test.name, err, test.want, failure)
			}
			if persisted != objectUnusable {
				t.Errorf("persisting past %s answered %d, want it unusable", test.name, persisted)
			}
		})
	}
}

func TestPersistingAnObjectTakesTheOneAnotherRunLinkedFirst(t *testing.T) {
	t.Parallel()
	size := int64(len(nativeObjectBody))
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeStoredFile(t, source, nativeObjectBody)
	destination := filepath.Join(root, "objects", "aa", "object")
	hooks := layerHooks{link: func(oldPath, newPath string) error {
		writeStoredFile(t, newPath, nativeObjectBody)
		return errors.New("the name was taken while this run was working")
	}}
	persisted, err := persistNativeObject(source, destination, size, faultsMoment, hooks.resolved())
	if err != nil {
		t.Fatalf("persisting over a name another run took reported %v, want nothing", err)
	}
	if persisted != objectPresent {
		t.Fatalf("persisting over a name another run took answered %d, want it already present", persisted)
	}
}

func TestPersistingAnObjectReportsASecondInspectionItCouldNotFinish(t *testing.T) {
	t.Parallel()
	failure := errors.New("the destination could not be inspected again")
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeStoredFile(t, source, nativeObjectBody)
	destination := filepath.Join(root, "objects", "aa", "object")
	seen := 0
	hooks := layerHooks{
		link: func(string, string) error { return errors.New("the name was taken") },
		lstat: func(path string) (fs.FileInfo, error) {
			if path != destination {
				return os.Lstat(path)
			}
			seen++
			if seen > inspectionsBeforeTheLink {
				return nil, failure
			}
			return nil, os.ErrNotExist
		},
	}
	if _, err := persistNativeObject(source, destination, int64(len(nativeObjectBody)),
		faultsMoment, hooks.resolved()); !errors.Is(err, failure) {
		t.Fatalf("persisting reported %v, want %v", err, failure)
	}
}

func TestPreparingANativeCacheMakesEveryPrefixAndRecordsWhenItTrimmed(t *testing.T) {
	t.Parallel()
	destination := filepath.Join(t.TempDir(), "native")
	if err := prepareNativeCache(destination, faultsMoment, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != nativeCachePrefixCount+1 {
		t.Fatalf("the cache holds %d entries, want %d prefixes and a trim record",
			len(entries), nativeCachePrefixCount)
	}
	trim, err := os.ReadFile(filepath.Join(destination, "trim.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(trim) != strconv.FormatInt(faultsMoment.Unix(), 10) {
		t.Fatalf("the trim record reads %q, want the moment it was prepared", trim)
	}

	unset := filepath.Join(t.TempDir(), "native")
	if err := prepareNativeCache(unset, time.Time{}, layerHooks{}.resolved()); err != nil {
		t.Fatal(err)
	}
	stamped, err := os.ReadFile(filepath.Join(unset, "trim.txt"))
	if err != nil {
		t.Fatal(err)
	}
	seconds, err := strconv.ParseInt(string(stamped), 10, 64)
	if err != nil || seconds <= 0 {
		t.Fatalf("a cache prepared without a clock recorded %q, want the moment it ran", stamped)
	}
}

func TestPreparingANativeCacheReportsEveryStepItCouldNotTake(t *testing.T) {
	t.Parallel()
	failure := errors.New("the step failed")
	for _, test := range []struct {
		name  string
		hooks layerHooks
		want  string
	}{
		{
			name:  "a root it cannot make",
			hooks: layerHooks{mkdirAll: func(string, os.FileMode) error { return failure }},
			want:  "create native build cache",
		},
		{
			name: "a prefix it cannot make",
			hooks: layerHooks{mkdirAll: func(path string, _ os.FileMode) error {
				if filepath.Base(path) == "ff" {
					return failure
				}
				return nil
			}},
			want: "create native build cache",
		},
		{
			name: "a trim record it cannot write",
			hooks: layerHooks{
				mkdirAll:  func(string, os.FileMode) error { return nil },
				writeFile: func(string, []byte, os.FileMode) error { return failure },
			},
			want: "initialize native build cache trim record",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := prepareNativeCache(filepath.Join(t.TempDir(), "native"), faultsMoment, test.hooks.resolved())
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("preparing past %s reported %v, want %q and %v", test.name, err, test.want, failure)
			}
		})
	}
}

func TestLinkingANativeObjectRefusesEveryProjectionThatIsNotTheOne(t *testing.T) {
	t.Parallel()
	size := int64(len(nativeObjectBody))
	failure := errors.New("the step failed")
	for _, test := range []struct {
		name        string
		source      string
		destination string
		repair      bool
		hooks       layerHooks
		want        string
		linked      bool
	}{
		{name: "a destination that is not there", source: nativeObjectBody, linked: true},
		{
			name: "a destination that is already the same file", source: nativeObjectBody,
			destination: "same", linked: true,
		},
		{
			name: "a destination that is another file", source: nativeObjectBody, destination: "other",
			want: "is not the expected projection",
		},
		{
			name: "a destination that is another file a repair may replace", source: nativeObjectBody,
			destination: "other", repair: true, linked: true,
		},
		{
			name: "a source of another size", source: nativeObjectBody + "0", destination: "other",
			want: "is not the expected regular file",
		},
		{
			name: "a source it cannot inspect", source: nativeObjectBody, destination: "other",
			hooks: layerHooks{lstat: func(path string) (fs.FileInfo, error) {
				if filepath.Base(path) == "source" {
					return nil, failure
				}
				return os.Lstat(path)
			}},
			want: "inspect native build cache source object",
		},
		{
			name: "a destination it cannot inspect", source: nativeObjectBody,
			hooks: layerHooks{lstat: func(path string) (fs.FileInfo, error) {
				if filepath.Base(path) == "object" {
					return nil, failure
				}
				return os.Lstat(path)
			}},
			want: "inspect native build cache object",
		},
		{
			name: "a destination it cannot replace", source: nativeObjectBody, destination: "other", repair: true,
			hooks: layerHooks{removeAll: func(string) error { return failure }},
			want:  "replace native build cache object",
		},
		{
			name: "an object it cannot link", source: nativeObjectBody,
			hooks: layerHooks{link: func(string, string) error { return failure }},
			want:  "hard-link native build cache object",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := filepath.Join(root, "source")
			writeStoredFile(t, source, test.source)
			destination := filepath.Join(root, "objects", "object")
			if err := os.MkdirAll(filepath.Dir(destination), filemode.ReadableDirectory); err != nil {
				t.Fatal(err)
			}
			switch test.destination {
			case "same":
				if err := os.Link(source, destination); err != nil {
					t.Fatal(err)
				}
			case "other":
				writeStoredFile(t, destination, strings.Repeat("x", len(nativeObjectBody)))
			}
			err := linkNativeObject(source, destination, size, test.repair, test.hooks.resolved())
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("linking %s reported %v, want %q", test.name, err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("linking %s reported %v, want nothing", test.name, err)
			}
			if !test.linked {
				return
			}
			body, readErr := os.ReadFile(destination)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(body) != nativeObjectBody {
				t.Fatalf("the projection reads %q, want the source it names", body)
			}
		})
	}
}
