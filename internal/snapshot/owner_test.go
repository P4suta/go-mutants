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

// TestCreateOwnsItsDirectoryWithoutTouchingTheTree pins the layout ownership
// forced on this package: the copy is a subdirectory of the directory that
// carries the lock and the marker, and never their sibling.
//
// The separation is not tidiness. Two invariants depend on it — [Snapshot.Redigest]
// applies no exclusions, so a marker beside the sources would be reported as
// drift on every run, and a snapshot of a snapshot (the probe tree) would copy
// the marker and hash a manifest that no longer matches the tree it came from.
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

	// The tree is exactly what was copied: the ownership files are not in the
	// manifest, and they are not drift either.
	drifts, err := snap.Redigest()
	if err != nil {
		t.Fatalf("Redigest: %v", err)
	}
	if len(drifts) != 0 {
		t.Errorf("a fresh snapshot already drifted: %v", drifts)
	}
}

// TestCreateOfASnapshotReproducesItsDigest is the probe tree's precondition,
// pinned here because it is this package that could break it: internal/session
// copies a snapshot's tree and refuses to continue unless the copy hashes the
// same.
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

// TestKeepLeavesTheSnapshotOnDiskAndSaysSo covers the deliberate keep: the
// directory survives, the marker records that this was asked for rather than
// leaked, and the lock is released so the next sweep can read the marker
// without waiting on a process that has gone.
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

	// A kept snapshot stays kept. Cleanup is deferred all over this codebase,
	// and a keep that the next deferred call undid would be no keep at all.
	if err = snap.Cleanup(); err != nil {
		t.Errorf("Cleanup of a kept snapshot: %v", err)
	}
	if _, statErr := os.Stat(snap.Root); statErr != nil {
		t.Errorf("Cleanup removed a kept snapshot: %v", statErr)
	}
}
