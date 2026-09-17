// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/tempowner"
)

func TestCreateOwnsItsDirectoryWithoutTouchingTheTree(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a/b/c.go": "package c\n"})
	dest := t.TempDir()

	snap, err := Create(src, Options{DestParent: dest})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = snap.Cleanup() })

	if base := filepath.Base(snap.Dir()); !strings.HasPrefix(base, DirPrefix) {
		t.Errorf("the owned directory is %q, want a name beginning with %s", base, DirPrefix)
	}
	if want := filepath.Join(snap.Dir(), TreeName); snap.Root != want {
		t.Errorf("Root is %q, want %q", snap.Root, want)
	}
	if snap.Parent() != dest {
		t.Errorf("Parent() is %q, want %q", snap.Parent(), dest)
	}
	for _, name := range []string{tempowner.LockName, tempowner.MarkerName} {
		if _, statErr := os.Stat(filepath.Join(snap.Dir(), name)); statErr != nil {
			t.Errorf("the snapshot directory has no %s: %v", name, statErr)
		}
		if _, statErr := os.Stat(filepath.Join(snap.Root, name)); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("%s is inside the copied tree (%v)", name, statErr)
		}
	}
	if lock, held, lockErr := tempowner.Acquire(tempowner.LockPath(snap.Dir())); lockErr != nil || held {
		t.Errorf("a live snapshot did not hold its lock (held=%v, err=%v)", held, lockErr)
		_ = lock.Release()
	}

	drifts, err := snap.Redigest()
	if err != nil {
		t.Fatalf("Redigest: %v", err)
	}
	if len(drifts) != 0 {
		t.Errorf("a fresh snapshot already drifted: %v", drifts)
	}
}

func TestCreateOfASnapshotReproducesItsDigest(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"main.go": "package main\n"})
	dest := t.TempDir()

	first, err := Create(src, Options{DestParent: dest})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = first.Cleanup() })

	second, err := Create(first.Root, Options{DestParent: first.Parent()})
	if err != nil {
		t.Fatalf("Create of the snapshot: %v", err)
	}
	t.Cleanup(func() { _ = second.Cleanup() })

	if second.WorkspaceDigest != first.WorkspaceDigest {
		t.Errorf("the copy of a snapshot hashes %s, want %s", second.WorkspaceDigest, first.WorkspaceDigest)
	}
	if second.Dir() == first.Dir() {
		t.Error("the copy claimed the same directory as its source")
	}
}

func TestKeepLeavesTheSnapshotOnDiskAndSaysSo(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"main.go": "package main\n"})
	dest := t.TempDir()

	snap, err := Create(src, Options{DestParent: dest})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err = snap.Keep(); err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if _, statErr := os.Stat(snap.Root); statErr != nil {
		t.Fatalf("Keep removed the snapshot: %v", statErr)
	}
	marker, err := tempowner.ReadMarker(snap.Dir())
	if err != nil {
		t.Fatalf("reading the marker: %v", err)
	}
	if !marker.Kept {
		t.Error("Keep did not mark the directory kept")
	}
	lock, held, err := tempowner.Acquire(tempowner.LockPath(snap.Dir()))
	if err != nil || !held {
		t.Fatalf("Keep did not release the lock (held=%v, err=%v)", held, err)
	}
	if err = lock.Release(); err != nil {
		t.Errorf("releasing the test's own lock: %v", err)
	}

	if err = snap.Cleanup(); err != nil {
		t.Errorf("Cleanup of a kept snapshot: %v", err)
	}
	if _, statErr := os.Stat(snap.Root); statErr != nil {
		t.Errorf("Cleanup removed a kept snapshot: %v", statErr)
	}
}

func TestKeepThatCannotBeRecordedIsNotAKeep(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"main.go": "package main\n"})
	dest := t.TempDir()

	snap, err := Create(src, Options{DestParent: dest})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	obstructMarker(t, snap.Dir())

	if err = snap.Keep(); err == nil {
		t.Fatal("Keep succeeded although the marker could not be written")
	}
	if err = snap.Cleanup(); err != nil {
		t.Fatalf("Cleanup after a failed Keep: %v", err)
	}
	if _, statErr := os.Stat(snap.Dir()); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a failed Keep left %s behind (%v)", snap.Dir(), statErr)
	}
}

func obstructMarker(t *testing.T, dir string) {
	t.Helper()
	path := tempowner.MarkerPath(dir)
	if err := os.Remove(path); err != nil {
		t.Fatalf("removing the marker: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("obstructing the marker: %v", err)
	}
}
