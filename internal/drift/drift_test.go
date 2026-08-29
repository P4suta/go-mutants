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
