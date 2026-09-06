// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package drift_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/drift"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/snapshot"
)

func TestUnexpectedFiltersOnlyTheInstrumentationOwnedChanges(t *testing.T) {
	source := t.TempDir()
	for name, contents := range map[string]string{
		"guarded.go":    "package fixture\n",
		"removed.txt":   "remove me\n",
		"untouched.txt": "original\n",
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := snapshot.Create(source, snapshot.Options{DestParent: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("cleanup: %v", cleanupErr)
		}
	})

	write := func(relative, contents string) {
		t.Helper()
		path := filepath.Join(snap.Root, filepath.FromSlash(relative))
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(path, []byte(contents), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	write("guarded.go", "package fixture\n// guarded\n")
	write("generated/runtime.go", "package generated\n")
	write("extra.txt", "unexpected\n")
	write("untouched.txt", "changed\n")
	if removeErr := os.Remove(filepath.Join(snap.Root, "removed.txt")); removeErr != nil {
		t.Fatal(removeErr)
	}

	got, err := drift.Unexpected(snap, instrument.Result{
		FilesInstrumented: []string{"guarded.go"},
		RuntimeDir:        "generated",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"added extra.txt",
		"removed removed.txt",
		"changed untouched.txt",
	}
	if !slices.Equal(got, want) {
		t.Errorf("unexpected drift = %v, want %v", got, want)
	}
}

// TestUnexpectedDriftsCarriesBothSidesOfEveryChange is the claim the public
// API's DriftError rests on: the filtering is the same one [drift.Unexpected]
// does, and what survives it is the drift itself — kind, path, and the digests
// on both sides — rather than a sentence about it.
func TestUnexpectedDriftsCarriesBothSidesOfEveryChange(t *testing.T) {
	source := t.TempDir()
	for name, contents := range map[string]string{
		"guarded.go":    "package fixture\n",
		"removed.txt":   "remove me\n",
		"untouched.txt": "original\n",
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := snapshot.Create(source, snapshot.Options{DestParent: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("cleanup: %v", cleanupErr)
		}
	})

	write := func(relative, contents string) {
		t.Helper()
		path := filepath.Join(snap.Root, filepath.FromSlash(relative))
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(path, []byte(contents), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	write("guarded.go", "package fixture\n// guarded\n")
	write("generated/runtime.go", "package generated\n")
	write("extra.txt", "unexpected\n")
	write("untouched.txt", "changed\n")
	if removeErr := os.Remove(filepath.Join(snap.Root, "removed.txt")); removeErr != nil {
		t.Fatal(removeErr)
	}

	got, err := drift.UnexpectedDrifts(snap, instrument.Result{
		FilesInstrumented: []string{"guarded.go"},
		RuntimeDir:        "generated",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("unexpected drift = %+v, want three changes", got)
	}
	kinds := []snapshot.DriftKind{snapshot.DriftAdded, snapshot.DriftRemoved, snapshot.DriftChanged}
	paths := []string{"extra.txt", "removed.txt", "untouched.txt"}
	for i, change := range got {
		if change.Kind != kinds[i] || change.RelPath != paths[i] {
			t.Errorf("drift[%d] = %+v, want %s %s", i, change, kinds[i], paths[i])
		}
	}
	if got[0].GotSHA256 == "" || got[0].WantSHA256 != "" {
		t.Errorf("added drift = %+v, want only the side that exists", got[0])
	}
	if got[1].WantSHA256 == "" || got[1].GotSHA256 != "" {
		t.Errorf("removed drift = %+v, want only the side that exists", got[1])
	}
	if got[2].WantSHA256 == "" || got[2].GotSHA256 == "" || got[2].WantSHA256 == got[2].GotSHA256 {
		t.Errorf("changed drift = %+v, want two different digests", got[2])
	}
}

func TestUnexpectedReturnsRedigestErrorsWithoutPartialChanges(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "source.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := snapshot.Create(source, snapshot.Options{DestParent: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if cleanupErr := snap.Cleanup(); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}

	got, err := drift.Unexpected(snap, instrument.Result{})
	if err == nil {
		t.Fatal("Unexpected succeeded after the snapshot was removed")
	}
	if got != nil {
		t.Errorf("changes = %v, want nil alongside an error", got)
	}
}
