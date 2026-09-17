// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const (
	reportBytes    = 4
	companionBytes = 2
)

func cacheEntryAt(t *testing.T, root, name string, size int, modified time.Time) string {
	t.Helper()
	directory := filepath.Join(root, "v1", name)
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "report.json")
	if err := os.WriteFile(path, make([]byte, size), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if !modified.IsZero() {
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func rootThatCannotHoldACache(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	if err := os.WriteFile(root, []byte("not a directory"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSafeEntryNameAcceptsOnlyAConfinedName(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: "digest", want: true},
		{name: ""},
		{name: "."},
		{name: ".."},
		{name: "parent/child"},
		{name: `parent\child`},
	} {
		t.Run("name "+test.name, func(t *testing.T) {
			t.Parallel()
			if got := safeEntryName(test.name); got != test.want {
				t.Fatalf("safeEntryName(%q) = %t, want %t", test.name, got, test.want)
			}
		})
	}
}

func TestCollectRefusesOnlyANegativePolicy(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		maxBytes int64
		ttl      time.Duration
		wantErr  bool
	}{
		{name: "no policy at all"},
		{name: "a negative byte budget", maxBytes: -1, wantErr: true},
		{name: "a negative age budget", ttl: -time.Second, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Collect(t.TempDir(), test.maxBytes, test.ttl, time.Now())
			if (err != nil) != test.wantErr {
				t.Fatalf("Collect(%d, %s) = %v, want an error %t", test.maxBytes, test.ttl, err, test.wantErr)
			}
		})
	}
}

func TestInspectReadsAnAbsentCacheAsAnEmptyOne(t *testing.T) {
	t.Parallel()
	status, err := Inspect(t.TempDir())
	if err != nil || status != (Status{}) {
		t.Fatalf("Inspect of an absent cache = (%+v, %v)", status, err)
	}
}

func TestEveryMaintenanceEntryPointReportsACacheItCannotInspect(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		call func(root string) error
	}{
		{name: "Inspect", call: func(root string) error { _, err := Inspect(root); return err }},
		{name: "Collect", call: func(root string) error { _, err := Collect(root, 1, time.Hour, time.Now()); return err }},
		{name: "Flush", call: func(root string) error { _, err := Flush(root); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.call(rootThatCannotHoldACache(t))
			if err == nil || !strings.Contains(err.Error(), "inspect cache") {
				t.Fatalf("%s over a file = %v", test.name, err)
			}
		})
	}
}

func TestInspectRefusesAVersionRootThatIsNotADirectory(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, root string)
	}{
		{
			name: "a regular file",
			prepare: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, "v1"), []byte("file"), filemode.ReadableFile); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "a symbolic link to a directory",
			prepare: func(t *testing.T, root string) {
				target := t.TempDir()
				if err := os.Symlink(target, filepath.Join(root, "v1")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			test.prepare(t, root)
			status, err := Inspect(root)
			if err == nil || !strings.Contains(err.Error(), "cache v1 root is not a confined directory") {
				t.Fatalf("Inspect = (%+v, %v)", status, err)
			}
		})
	}
}

func TestInspectRefusesAnEntryThatIsNotAConfinedDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "v1"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "v1", "loose"), []byte("file"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(root)
	if err == nil || !strings.Contains(err.Error(), `cache entry "loose" is not a confined directory`) {
		t.Fatalf("Inspect = (%+v, %v)", status, err)
	}
}

func TestInspectRefusesAnEntryThatCrossesASymbolicLink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := cacheEntryAt(t, root, "entry", 1, time.Time{})
	if err := os.Symlink(t.TempDir(), filepath.Join(directory, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(root)
	if err == nil || !strings.Contains(err.Error(), "crosses symbolic link") {
		t.Fatalf("Inspect = (%+v, %v)", status, err)
	}
}

func TestInspectSummarisesSizeAndTheSpanOfModificationTimes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	oldest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	middle := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cacheEntryAt(t, root, "a-oldest", 7, oldest)
	cacheEntryAt(t, root, "b-newest", 5, newest)
	cacheEntryAt(t, root, "c-middle", 3, middle)
	status, err := Inspect(root)
	if err != nil || status.Entries != 3 || status.Bytes != 15 ||
		!status.Oldest.Equal(oldest) || !status.Newest.Equal(newest) {
		t.Fatalf("Inspect = (%+v, %v), want three entries spanning %s to %s", status, err, oldest, newest)
	}
}

func TestEntryMetadataReportsTheNewestFileItWalked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	directory := cacheEntryAt(t, root, "entry", reportBytes, older)
	first := filepath.Join(directory, "a-newer.json")
	if err := os.WriteFile(first, make([]byte, companionBytes), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(first, newer, newer); err != nil {
		t.Fatal(err)
	}
	size, modified, err := entryMetadata(directory)
	if err != nil || size != reportBytes+companionBytes || !modified.Equal(newer) {
		t.Fatalf("entryMetadata = (%d, %s, %v), want every walked byte counted once, modified at %s", size, modified, err, newer)
	}
}

func TestExpiryNeedsAnAgeBudgetAKnownNowAndAKnownModificationTime(t *testing.T) {
	t.Parallel()
	modified := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := modified.Add(48 * time.Hour)
	for _, test := range []struct {
		name         string
		ttl          time.Duration
		now          time.Time
		withModified bool
		wantRemoved  int
	}{
		{name: "an age budget, a now and a modification time", ttl: time.Hour, now: later, withModified: true, wantRemoved: 1},
		{name: "no age budget", now: later, withModified: true},
		{name: "no now", ttl: time.Hour, withModified: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			stamp := time.Time{}
			if test.withModified {
				stamp = modified
			}
			cacheEntryAt(t, root, "entry", 4, stamp)
			collected, err := Collect(root, 0, test.ttl, test.now)
			if err != nil || collected.RemovedEntries != test.wantRemoved {
				t.Fatalf("Collect = (%+v, %v), want %d removed", collected, err, test.wantRemoved)
			}
		})
	}
}

func TestCollectKeepsACacheExactlyAtItsByteBudget(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		maxBytes    int64
		wantRemoved int
	}{
		{name: "exactly at the budget", maxBytes: 8},
		{name: "one byte over the budget", maxBytes: 7, wantRemoved: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			cacheEntryAt(t, root, "older", 4, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			cacheEntryAt(t, root, "newer", 4, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
			collected, err := Collect(root, test.maxBytes, 0, time.Now())
			if err != nil || collected.RemovedEntries != test.wantRemoved {
				t.Fatalf("Collect = (%+v, %v), want %d removed", collected, err, test.wantRemoved)
			}
		})
	}
}

func TestCollectRemovesTheExpiredThenTheOldestThenTheFirstByName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	shared := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	cacheEntryAt(t, root, "b-shared", 4, shared)
	cacheEntryAt(t, root, "a-shared", 4, shared)
	cacheEntryAt(t, root, "older", 4, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	cacheEntryAt(t, root, "expired", 4, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	collected, err := Collect(root, 8, 24*time.Hour, shared.Add(time.Hour))
	if err != nil || collected.RemovedEntries != 2 {
		t.Fatalf("Collect = (%+v, %v), want the expired entry and the oldest survivor removed", collected, err)
	}
	remaining := map[string]bool{}
	directories, err := os.ReadDir(filepath.Join(root, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range directories {
		remaining[directory.Name()] = true
	}
	if len(remaining) != 2 || !remaining["a-shared"] || !remaining["b-shared"] {
		t.Fatalf("remaining entries = %v, want the two newest kept", remaining)
	}
}

func TestFlushRunsItsHookExactlyWhenItWasGivenOne(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cacheEntryAt(t, root, "entry", 4, time.Time{})
	flushed, err := Flush(root)
	if err != nil || flushed.RemovedEntries != 1 || flushed.After.Entries != 0 {
		t.Fatalf("Flush = (%+v, %v)", flushed, err)
	}
	second := t.TempDir()
	cacheEntryAt(t, second, "entry", 4, time.Time{})
	hooked := 0
	flushed, err = flushWithHook(second, func() { hooked++ })
	if err != nil || hooked != 1 || flushed.RemovedEntries != 1 {
		t.Fatalf("flushWithHook = (%+v, %v), hook ran %d times", flushed, err, hooked)
	}
}

func TestRemoveEntriesRefusesWhatItCannotConfine(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, root string) []cacheEntry
		message string
	}{
		{
			name: "a name that leaves the cache",
			prepare: func(t *testing.T, root string) []cacheEntry {
				cacheEntryAt(t, root, "entry", 4, time.Time{})
				return []cacheEntry{{name: "../escape", version: versionRootInfo(t, root)}}
			},
			message: "refusing unconfined cache removal",
		},
		{
			name: "a version root nobody vouched for",
			prepare: func(t *testing.T, root string) []cacheEntry {
				cacheEntryAt(t, root, "entry", 4, time.Time{})
				return []cacheEntry{{name: "entry"}}
			},
			message: "cache v1 root changed before removal",
		},
		{
			name: "an entry that is no longer there",
			prepare: func(t *testing.T, root string) []cacheEntry {
				cacheEntryAt(t, root, "entry", 4, time.Time{})
				return []cacheEntry{{name: "absent", version: versionRootInfo(t, root)}}
			},
			message: "inspect cache entry before removal",
		},
		{
			name: "an entry that is a regular file",
			prepare: func(t *testing.T, root string) []cacheEntry {
				cacheEntryAt(t, root, "entry", 4, time.Time{})
				if err := os.WriteFile(filepath.Join(root, "v1", "loose"), []byte("file"), filemode.ReadableFile); err != nil {
					t.Fatal(err)
				}
				return []cacheEntry{{name: "loose", version: versionRootInfo(t, root)}}
			},
			message: "is not a confined directory",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			entries := test.prepare(t, root)
			err := removeEntries(root, entries, func() { t.Fatal("removal began after a refusal") })
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("removeEntries = %v, want %q", err, test.message)
			}
		})
	}
}

func TestRemoveEntriesAcceptsNothingToRemove(t *testing.T) {
	t.Parallel()
	if err := removeEntries(t.TempDir(), nil, func() { t.Fatal("an empty removal opened the cache") }); err != nil {
		t.Fatalf("removeEntries of nothing = %v", err)
	}
}

func versionRootInfo(t *testing.T, root string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(filepath.Join(root, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestOpenCacheVersionRootRefusesEveryRootItCannotVouchFor(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T) (string, os.FileInfo)
		message string
	}{
		{
			name: "a cache root that is not a directory",
			prepare: func(t *testing.T) (string, os.FileInfo) {
				return rootThatCannotHoldACache(t), nil
			},
			message: "open cache root for removal",
		},
		{
			name:    "a cache with no version root",
			prepare: func(t *testing.T) (string, os.FileInfo) { return t.TempDir(), nil },
			message: "inspect cache v1 root before removal",
		},
		{
			name: "a version root that is a regular file",
			prepare: func(t *testing.T) (string, os.FileInfo) {
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, "v1"), []byte("file"), filemode.ReadableFile); err != nil {
					t.Fatal(err)
				}
				return root, versionRootInfo(t, root)
			},
			message: "cache v1 root changed before removal",
		},
		{
			name: "a version root that is a symbolic link",
			prepare: func(t *testing.T) (string, os.FileInfo) {
				root := t.TempDir()
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "v1")); err != nil {
					t.Fatal(err)
				}
				return root, versionRootInfo(t, root)
			},
			message: "cache v1 root changed before removal",
		},
		{
			name: "a version root nobody vouched for",
			prepare: func(t *testing.T) (string, os.FileInfo) {
				root := t.TempDir()
				cacheEntryAt(t, root, "entry", 4, time.Time{})
				return root, nil
			},
			message: "cache v1 root changed before removal",
		},
		{
			name: "a version root that is not the one inspected",
			prepare: func(t *testing.T) (string, os.FileInfo) {
				root := t.TempDir()
				cacheEntryAt(t, root, "entry", 4, time.Time{})
				other := t.TempDir()
				cacheEntryAt(t, other, "entry", 4, time.Time{})
				return root, versionRootInfo(t, other)
			},
			message: "cache v1 root changed before removal",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, expected := test.prepare(t)
			opened, err := openCacheVersionRoot(root, expected)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				if opened != nil {
					_ = opened.Close()
				}
				t.Fatalf("openCacheVersionRoot = (%v, %v), want %q", opened, err, test.message)
			}
		})
	}
}

func TestCompareCollectionOrderPutsTheExpiredFirstThenTheOldestThenTheName(t *testing.T) {
	t.Parallel()
	earlier := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		a    cacheEntry
		b    cacheEntry
		want int
	}{
		{name: "expired before live", a: cacheEntry{expired: true}, b: cacheEntry{}, want: -1},
		{name: "live after expired", a: cacheEntry{}, b: cacheEntry{expired: true}, want: 1},
		{name: "older before newer", a: cacheEntry{modified: earlier}, b: cacheEntry{modified: later}, want: -1},
		{name: "newer after older", a: cacheEntry{modified: later}, b: cacheEntry{modified: earlier}, want: 1},
		{name: "same age, by name", a: cacheEntry{name: "a"}, b: cacheEntry{name: "b"}, want: -1},
		{name: "same age and name", a: cacheEntry{name: "a"}, b: cacheEntry{name: "a"}, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := compareCollectionOrder(test.a, test.b); got != test.want {
				t.Fatalf("compareCollectionOrder(%+v, %+v) = %d, want %d", test.a, test.b, got, test.want)
			}
		})
	}
}

func TestFlushReportsACacheThatChangedUnderTheRemovalItJustMade(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cacheEntryAt(t, root, "entry", 4, time.Time{})
	_, err := flushWithHook(root, func() {
		if err := os.RemoveAll(filepath.Join(root, "v1", "entry")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(root, "v1")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "v1"), []byte("file"), filemode.ReadableFile); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "cache v1 root is not a confined directory") {
		t.Fatalf("Flush over a cache replaced under it = %v", err)
	}
}

func TestCollectReportsACacheThatChangedUnderTheRemovalItJustMade(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cacheEntryAt(t, root, "entry", 4, time.Time{})
	_, err := collectWithHook(root, 1, 0, time.Now(), func() {
		if err := os.RemoveAll(filepath.Join(root, "v1", "entry")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(root, "v1")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "v1"), []byte("file"), filemode.ReadableFile); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "cache v1 root is not a confined directory") {
		t.Fatalf("Collect over a cache replaced under it = %v", err)
	}
}
