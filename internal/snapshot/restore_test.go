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

func writeInto(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

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
