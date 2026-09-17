// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupRemovesTheSnapshot(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"a/b/c.go": "package c\n"})
	dest := t.TempDir()

	snap, err := Create(src, Options{DestParent: dest})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := snap.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(snap.Root); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("snapshot root survived Cleanup (err=%v)", err)
	}
	if _, err := os.Stat(snap.Dir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the owned directory survived Cleanup (err=%v)", err)
	}
	if got := readFile(t, filepath.Join(src, "a", "b", "c.go")); got != "package c\n" {
		t.Errorf("source file changed to %q", got)
	}
	if err := snap.Cleanup(); err != nil {
		t.Errorf("second Cleanup: %v", err)
	}
}

func TestCleanupNilSnapshot(t *testing.T) {
	t.Parallel()

	var snap *Snapshot
	if err := snap.Cleanup(); err != nil {
		t.Errorf("Cleanup on a nil snapshot = %v, want nil", err)
	}
}

func TestCleanupRefusesUnsafeRoots(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	tests := []struct {
		name string
		snap Snapshot
	}{
		{
			name: "zero snapshot",
			snap: Snapshot{},
		},
		{
			name: "relative path",
			snap: owned(filepath.Join("relative", DirPrefix+"x"), "relative"),
		},
		{
			name: "a real directory that is not a snapshot",
			snap: owned(dest, filepath.Dir(dest)),
		},
		{
			name: "right parent, wrong name",
			snap: owned(filepath.Join(dest, "important-sources"), dest),
		},
		{
			name: "right name, unrelated parent",
			snap: owned(filepath.Join(dest, "sub", DirPrefix+"x"), dest),
		},
		{
			name: "no destination parent recorded",
			snap: owned(filepath.Join(dest, DirPrefix+"x"), ""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			snap := tt.snap
			removed := false
			snap.remove = func(string) error { removed = true; return nil }
			snap.sleep = func(time.Duration) {}

			assertCode(t, snap.Cleanup(), CodeCleanupRefused)
			if removed {
				t.Error("Cleanup attempted a removal it should have refused")
			}
		})
	}
}

func TestCleanupAcceptsTheOSTemporaryDirectory(t *testing.T) {
	t.Parallel()

	snap := owned(filepath.Join(os.TempDir(), DirPrefix+"synthetic"), "")
	snap.sleep = func(time.Duration) {}
	var got string
	snap.remove = func(path string) error { got = path; return nil }

	if err := snap.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if got != snap.Dir() {
		t.Errorf("removed %q, want %q", got, snap.Dir())
	}
}

func owned(dir, destParent string) Snapshot {
	return Snapshot{dir: dir, Root: filepath.Join(dir, TreeName), destParent: destParent}
}

func TestCleanupRetriesUntilItSucceeds(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	snap := owned(filepath.Join(dest, DirPrefix+"x"), dest)

	attempts := 0
	snap.remove = func(string) error {
		attempts++
		if attempts < 3 {
			return errors.New("used by another process")
		}
		return nil
	}
	var slept []time.Duration
	snap.sleep = func(d time.Duration) { slept = append(slept, d) }

	if err := snap.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	want := []time.Duration{cleanupBackoff, 2 * cleanupBackoff}
	if len(slept) != len(want) {
		t.Fatalf("slept %v, want %v", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Errorf("sleep %d = %v, want %v", i, slept[i], want[i])
		}
	}
}

func TestCleanupGivesUp(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	snap := owned(filepath.Join(dest, DirPrefix+"x"), dest)

	stubborn := errors.New("used by another process")
	attempts := 0
	snap.remove = func(string) error { attempts++; return stubborn }
	var slept []time.Duration
	snap.sleep = func(d time.Duration) { slept = append(slept, d) }

	err := snap.Cleanup()
	assertCode(t, err, CodeCleanupFailed)
	if !errors.Is(err, stubborn) {
		t.Errorf("error does not wrap the last removal failure: %v", err)
	}
	if attempts != cleanupAttempts {
		t.Errorf("attempts = %d, want %d", attempts, cleanupAttempts)
	}
	want := []time.Duration{cleanupBackoff, 2 * cleanupBackoff, 4 * cleanupBackoff, 8 * cleanupBackoff}
	if len(slept) != len(want) {
		t.Fatalf("slept %v, want %v", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Errorf("sleep %d = %v, want %v", i, slept[i], want[i])
		}
	}
}
