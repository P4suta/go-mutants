// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package retention

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const (
	retainedBytes   = 4
	companionBytes  = 2
	retentionBudget = 8
)

func retainedAt(t *testing.T, root, name string, size int, modified time.Time) string {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "artifact")
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

func TestChildKindNamesWhatItAccepts(t *testing.T) {
	t.Parallel()
	if got := childFile.String(); got != "file" {
		t.Fatalf("childFile = %q", got)
	}
	if got := childDirectory.String(); got != "directory" {
		t.Fatalf("childDirectory = %q", got)
	}
}

func TestChildKindAcceptsOnlyItsOwnShapeAndNeverALink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "directory"), filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	children, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	accepted := map[string]map[childKind]bool{}
	for _, child := range children {
		accepted[child.Name()] = map[childKind]bool{
			childFile:      childFile.accepts(child),
			childDirectory: childDirectory.accepts(child),
		}
	}
	for name, want := range map[string]map[childKind]bool{
		"directory": {childFile: false, childDirectory: true},
		"file":      {childFile: true, childDirectory: false},
		"link":      {childFile: false, childDirectory: false},
	} {
		for kind, expected := range want {
			if accepted[name][kind] != expected {
				t.Fatalf("%s.accepts(%s) = %t, want %t", kind, name, accepted[name][kind], expected)
			}
		}
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

func TestEveryEntryPointReportsARootItCannotInspect(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		call func(root string) error
	}{
		{name: "Collect", call: func(root string) error { _, err := Collect(root, 1, time.Hour, time.Now()); return err }},
		{name: "CollectFiles", call: func(root string) error { _, err := CollectFiles(root, 1, time.Hour, time.Now()); return err }},
		{name: "Keep", call: func(root string) error { _, err := Keep(root, 1, nil, time.Now()); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(root, []byte("not a directory"), filemode.ReadableFile); err != nil {
				t.Fatal(err)
			}
			err := test.call(root)
			if err == nil || !strings.Contains(err.Error(), "inspect retained artifacts") {
				t.Fatalf("%s over a file = %v", test.name, err)
			}
		})
	}
}

func TestInspectingARootNobodyHasWrittenIsAnEmptyOne(t *testing.T) {
	t.Parallel()
	result, err := Collect(filepath.Join(t.TempDir(), "absent"), 1, time.Hour, time.Now())
	if err != nil || result.Before.Entries != 0 || result.RemovedEntries != 0 {
		t.Fatalf("Collect of a root that is not there = (%+v, %v)", result, err)
	}
}

func TestCollectKeepsARootExactlyAtItsByteBudget(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		maxBytes    int64
		wantRemoved int
	}{
		{name: "exactly at the budget", maxBytes: retentionBudget},
		{name: "one byte over the budget", maxBytes: retentionBudget - 1, wantRemoved: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			retainedAt(t, root, "older", retainedBytes, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			retainedAt(t, root, "newer", retainedBytes, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
			result, err := Collect(root, test.maxBytes, 0, time.Now())
			if err != nil || result.RemovedEntries != test.wantRemoved {
				t.Fatalf("Collect = (%+v, %v), want %d removed", result, err, test.wantRemoved)
			}
		})
	}
}

func TestCollectRemovesTheBytesItActuallyFreed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	retainedAt(t, root, "older", retainedBytes, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	retainedAt(t, root, "newer", companionBytes, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	result, err := Collect(root, 1, 0, time.Now())
	if err != nil || result.RemovedEntries != 2 || result.RemovedBytes != retainedBytes+companionBytes {
		t.Fatalf("Collect = (%+v, %v), want every byte counted", result, err)
	}
}

func TestExpiryNeedsAnAgeBudgetAKnownNowAndAKnownModificationTime(t *testing.T) {
	t.Parallel()
	modified := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := modified.Add(48 * time.Hour)
	for _, test := range []struct {
		name        string
		ttl         time.Duration
		now         time.Time
		wantRemoved int
	}{
		{name: "an age budget and a now", ttl: time.Hour, now: later, wantRemoved: 1},
		{name: "no age budget", now: later},
		{name: "no now", ttl: time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			retainedAt(t, root, "entry", retainedBytes, modified)
			result, err := Collect(root, 0, test.ttl, test.now)
			if err != nil || result.RemovedEntries != test.wantRemoved {
				t.Fatalf("Collect = (%+v, %v), want %d removed", result, err, test.wantRemoved)
			}
		})
	}
}

func TestInspectSummarisesTheSpanOfModificationTimes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	oldest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	middle := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	retainedAt(t, root, "a-oldest", retainedBytes, oldest)
	retainedAt(t, root, "b-newest", retainedBytes, newest)
	retainedAt(t, root, "c-middle", retainedBytes, middle)
	status, _, err := inspect(root, childDirectory, 0, time.Time{})
	if err != nil || !status.Oldest.Equal(oldest) || !status.Newest.Equal(newest) ||
		status.Entries != 3 || status.Bytes != 3*retainedBytes {
		t.Fatalf("inspect = (%+v, %v), want three entries spanning %s to %s", status, err, oldest, newest)
	}
}

func TestMetadataReportsTheNewestFileItWalked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	directory := retainedAt(t, root, "entry", retainedBytes, older)
	first := filepath.Join(directory, "a-newer")
	if err := os.WriteFile(first, make([]byte, companionBytes), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(first, newer, newer); err != nil {
		t.Fatal(err)
	}
	size, modified, err := metadata(directory)
	if err != nil || size != retainedBytes+companionBytes || !modified.Equal(newer) {
		t.Fatalf("metadata = (%d, %s, %v)", size, modified, err)
	}
}

func TestMetadataRefusesAnArtifactThatCrossesALink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := retainedAt(t, root, "entry", retainedBytes, time.Time{})
	if err := os.Symlink(t.TempDir(), filepath.Join(directory, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	_, _, err := metadata(directory)
	if err == nil || !strings.Contains(err.Error(), "crosses symbolic link") {
		t.Fatalf("metadata over a link = %v", err)
	}
}

func TestRemoveRefusesAnythingOutsideTheRootItWasGiven(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		target func(t *testing.T, root string) string
	}{
		{name: "a sibling", target: func(t *testing.T, root string) string { return filepath.Join(root, "..", "elsewhere") }},
		{name: "a nested path", target: func(t *testing.T, root string) string { return filepath.Join(root, "entry", "deeper") }},
		{name: "the parent itself", target: func(t *testing.T, root string) string { return filepath.Join(root, "..") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			err := remove(root, test.target(t, root))
			if err == nil || !strings.Contains(err.Error(), "refusing unconfined retained artifact removal") {
				t.Fatalf("remove(%q) = %v", test.target(t, root), err)
			}
		})
	}
}

func TestRemoveRefusesALinkInsideTheArtifact(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := retainedAt(t, root, "entry", retainedBytes, time.Time{})
	if err := os.Symlink(t.TempDir(), filepath.Join(directory, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	err := remove(root, directory)
	if err == nil || !strings.Contains(err.Error(), "refusing symbolic link in retained artifact") {
		t.Fatalf("remove of an artifact holding a link = %v", err)
	}
	if _, statErr := os.Stat(directory); statErr != nil {
		t.Fatalf("a refused removal took the artifact anyway: %v", statErr)
	}
}

func TestRemoveReportsWhatItCouldNotTake(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	err := remove(root, filepath.Join(root, "absent"))
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove of an artifact that is not there = %v", err)
	}
}

func TestSafeNameAcceptsOnlyAConfinedName(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: "entry", want: true},
		{name: ""},
		{name: "."},
		{name: ".."},
		{name: "parent/child"},
		{name: `parent\child`},
	} {
		t.Run("name "+test.name, func(t *testing.T) {
			t.Parallel()
			if got := safeName(test.name); got != test.want {
				t.Fatalf("safeName(%q) = %t, want %t", test.name, got, test.want)
			}
		})
	}
}

func TestCompareRetentionOrderPutsTheExpiredFirstThenTheOldestThenTheName(t *testing.T) {
	t.Parallel()
	earlier := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		a    entry
		b    entry
		want int
	}{
		{name: "expired before live", a: entry{expired: true}, b: entry{}, want: -1},
		{name: "live after expired", a: entry{}, b: entry{expired: true}, want: 1},
		{name: "older before newer", a: entry{modified: earlier}, b: entry{modified: later}, want: -1},
		{name: "newer after older", a: entry{modified: later}, b: entry{modified: earlier}, want: 1},
		{name: "same age, by name", a: entry{name: "a"}, b: entry{name: "b"}, want: -1},
		{name: "same age and name", a: entry{name: "a"}, b: entry{name: "a"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := compareRetentionOrder(test.a, test.b); got != test.want {
				t.Fatalf("compareRetentionOrder(%+v, %+v) = %d, want %d", test.a, test.b, got, test.want)
			}
		})
	}
}

func TestRemoveRefusesATargetItCannotPlaceUnderTheRoot(t *testing.T) {
	t.Parallel()
	err := remove("relative/root", filepath.Join(t.TempDir(), "entry"))
	if err == nil || !strings.Contains(err.Error(), "refusing unconfined retained artifact removal") {
		t.Fatalf("remove with a root nothing can be made relative to = %v", err)
	}
}

func TestCollectAndKeepCarryTheRemovalTheyCouldNotMake(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		call func(root string) error
	}{
		{name: "Collect", call: func(root string) error { _, err := Collect(root, 1, 0, time.Now()); return err }},
		{name: "Keep", call: func(root string) error { _, err := Keep(root, 0, nil, time.Now()); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			directory := retainedAt(t, root, "entry", retainedBytes, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			if err := os.Symlink(t.TempDir(), filepath.Join(directory, "elsewhere")); err != nil {
				t.Fatal(err)
			}
			err := test.call(root)
			if err == nil || !strings.Contains(err.Error(), "symbolic link") {
				t.Fatalf("%s over an artifact holding a link = %v", test.name, err)
			}
		})
	}
}

func TestAnArtifactWithNoFileInItIsAgedByItsOwnTimestamp(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "entry")
	nested := filepath.Join(directory, "nested")
	if err := os.MkdirAll(nested, filemode.ReadableDirectory); err != nil {
		t.Fatal(err)
	}
	own := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inner := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(nested, inner, inner); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(directory, own, own); err != nil {
		t.Fatal(err)
	}
	size, modified, err := metadata(directory)
	if err != nil || size != 0 || !modified.Equal(own) {
		t.Fatalf("metadata = (%d, %s, %v), want the artifact aged by its own timestamp %s", size, modified, err, own)
	}
}
