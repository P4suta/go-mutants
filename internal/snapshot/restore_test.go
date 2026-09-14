// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// Restore is one method with one caller in mind: a worker's private copy of an
// instrumented tree, which has to be identical before every mutant that worker
// runs. Without it, mutant k is measured against whatever mutant k-1's tests
// wrote -- which is the same corruption the shared-snapshot drift gate exists
// to catch, moved somewhere nobody is watching.

// TestRestoreUndoesEveryKindOfDrift is the method in one test: a changed file,
// an added one, and a removed one, all at once and all put back.
func TestRestoreUndoesEveryKindOfDrift(t *testing.T) {
	t.Parallel()

	source := testkit.NewModule(t).Module("fixture.example/restore").
		Source("kept.go", "package restore\n\nconst Kept = 1\n").
		Source("changed.go", "package restore\n\nconst Changed = 2\n").
		Source("removed.go", "package restore\n\nconst Removed = 3\n").
		Root()
	snap := createIn(t, source)

	changed := filepath.Join(snap.Root, "changed.go")
	removed := filepath.Join(snap.Root, "removed.go")
	added := filepath.Join(snap.Root, "testdata", "written-by-a-test.txt")

	writeInto(t, changed, "package restore\n\nconst Changed = 99\n")
	if err := os.Remove(removed); err != nil {
		t.Fatalf("removing a file to simulate a test that deletes one: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(added), 0o755); err != nil {
		t.Fatalf("staging: %v", err)
	}
	writeInto(t, added, "a golden file a test updated\n")

	drifts, err := snap.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got := make([]string, 0, len(drifts))
	for _, change := range drifts {
		got = append(got, change.Kind.String()+" "+change.RelPath)
	}
	want := []string{
		"changed changed.go",
		"removed removed.go",
		"added testdata/written-by-a-test.txt",
	}
	if !sameSet(got, want) {
		t.Errorf("Restore undid %v, want %v", got, want)
	}

	// And the tree is the tree again, which is the claim the list above is
	// only evidence for.
	after, err := snap.Redigest()
	if err != nil {
		t.Fatalf("Redigest after Restore: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("the snapshot still drifts after being restored: %+v", after)
	}
	if got, want := readAll(t, changed), readAll(t, filepath.Join(source, "changed.go")); got != want {
		t.Errorf("the changed file holds %q, want the source's %q", got, want)
	}
	if _, err := os.Stat(removed); err != nil {
		t.Errorf("the removed file was not put back: %v", err)
	}
	if _, err := os.Stat(added); !os.IsNotExist(err) {
		t.Errorf("the added file is still there: %v", err)
	}
}

// TestRestoreTouchesNothingWhenNothingDrifted is the cheap case, and the one a
// run pays for on every mutant of a suite that writes nowhere.
func TestRestoreTouchesNothingWhenNothingDrifted(t *testing.T) {
	t.Parallel()

	source := testkit.NewModule(t).Module("fixture.example/restore").
		Source("kept.go", "package restore\n\nconst Kept = 1\n").
		Root()
	snap := createIn(t, source)

	before := fileTimes(t, snap.Root)
	drifts, err := snap.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(drifts) != 0 {
		t.Errorf("Restore reported %+v on a snapshot nothing wrote into", drifts)
	}
	if after := fileTimes(t, snap.Root); !sameTimes(before, after) {
		t.Errorf("Restore rewrote files that had not drifted")
	}
}

// TestRestoreRefusesWhenTheSourceTreeItselfMoved is the postcondition, and it
// is the one failure mode a caller cannot check for itself.
//
// Restore copies from the tree the snapshot was made of, so a digest that
// disagrees after the copy means *that* tree has changed. A worker copy is
// restored from the shared instrumented tree between every mutant, and the
// whole arrangement rests on that tree being frozen; a silent success here
// would restore the wrong bytes onto every worker for the rest of the run.
func TestRestoreRefusesWhenTheSourceTreeItselfMoved(t *testing.T) {
	t.Parallel()

	source := testkit.NewModule(t).Module("fixture.example/restore").
		Source("moved.go", "package restore\n\nconst Moved = 1\n").
		Root()
	snap := createIn(t, source)

	writeInto(t, filepath.Join(snap.Root, "moved.go"), "package restore\n\nconst Moved = 2\n")
	writeInto(t, filepath.Join(source, "moved.go"), "package restore\n\nconst Moved = 3\n")

	_, err := snap.Restore()
	if err == nil {
		t.Fatal("Restore accepted a source tree that had changed under it")
	}
	if code := snapshot.CodeOf(err); code != snapshot.CodeRestoreFailed {
		t.Errorf("code = %q, want %q", code, snapshot.CodeRestoreFailed)
	}
	if !strings.Contains(err.Error(), "has itself changed") {
		t.Errorf("the refusal does not say the source moved: %v", err)
	}
}

// TestRestoreReportsASourceFileThatHasGone is the other way the source can
// betray a restore, and it is reported rather than passed over: a file the
// manifest names and the source no longer holds is a tree that has moved.
func TestRestoreReportsASourceFileThatHasGone(t *testing.T) {
	t.Parallel()

	source := testkit.NewModule(t).Module("fixture.example/restore").
		Source("gone.go", "package restore\n\nconst Gone = 1\n").
		Root()
	snap := createIn(t, source)

	if err := os.Remove(filepath.Join(snap.Root, "gone.go")); err != nil {
		t.Fatalf("staging: %v", err)
	}
	if err := os.Remove(filepath.Join(source, "gone.go")); err != nil {
		t.Fatalf("staging: %v", err)
	}

	_, err := snap.Restore()
	if err == nil {
		t.Fatal("Restore accepted a source that no longer holds the file")
	}
	if code := snapshot.CodeOf(err); code != snapshot.CodeRestoreFailed {
		t.Errorf("code = %q, want %q", code, snapshot.CodeRestoreFailed)
	}
}

// createIn makes a snapshot inside the test's own temporary directory, which is
// what every test in this file wants and none of them wants to spell.
func createIn(t *testing.T, source string) *snapshot.Snapshot {
	t.Helper()
	snap, err := snapshot.Create(source, snapshot.Options{DestParent: t.TempDir()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		if err := snap.Cleanup(); err != nil {
			t.Errorf("Cleanup: %v", err)
		}
	})
	return snap
}

// writeInto replaces a file's contents, creating it if it is not there.
func writeInto(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// readAll reads a file the test has just asserted about.
func readAll(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// fileTimes is every regular file under root with its modification time, so
// that "nothing was rewritten" can be asserted rather than assumed.
func fileTimes(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		out[path] = info.ModTime().UnixNano()
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return out
}

// sameTimes reports whether two readings of fileTimes agree.
func sameTimes(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for path, when := range a {
		if b[path] != when {
			return false
		}
	}
	return true
}

// sameSet compares two lists as sets, because Restore's order is Redigest's and
// this file is about what it undid rather than in which order.
func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]int, len(got))
	for _, s := range got {
		seen[s]++
	}
	for _, s := range want {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
