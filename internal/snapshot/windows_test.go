// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package snapshot

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/P4suta/go-mutants/internal/glob"
)

// mklinkJunction creates a directory junction, skipping the test when the
// machine cannot. A junction needs no privilege, unlike a symbolic link, which
// is exactly why it is the reparse point a real user is likely to have.
func mklinkJunction(t *testing.T, link, target string) {
	t.Helper()
	// mklink is a cmd builtin rather than an executable.
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("cannot create a junction with mklink /J on this machine (%v): %s", err, out)
	}
}

// TestCreateRejectsJunction is the test the reparse point detector exists for.
// A junction is the Windows way to have one directory appear inside another,
// and following it would let a snapshot copy an unbounded amount of the disk —
// or itself, since a junction can point at an ancestor.
func TestCreateRejectsJunction(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"elsewhere.go": "package elsewhere\n"})

	src := t.TempDir()
	writeTree(t, src, map[string]string{"pkg/real.go": "package pkg\n"})
	link := filepath.Join(src, "pkg", "linked")
	mklinkJunction(t, link, outside)

	// Record what the toolchain thinks a junction is, so a future change in
	// that mapping shows up in the failure output rather than as a mystery.
	if fi, err := os.Lstat(link); err == nil {
		t.Logf("os.Lstat reports mode %v (type %v) for the junction", fi.Mode(), fi.Mode().Type())
		if !isReparsePoint(fi) {
			t.Error("isReparsePoint did not fire on a junction")
		}
	}

	_, err := Create(src, Options{DestParent: t.TempDir()})
	assertCode(t, err, CodeReparsePoint)
	if !strings.Contains(err.Error(), "pkg/linked") {
		t.Errorf("error does not name the offending path: %v", err)
	}
}

// TestCreateSkipsExcludedJunction proves a user can get past a junction the
// same way they get past a symbolic link: by excluding it, which is checked
// before the entry is stat'ed at all.
func TestCreateSkipsExcludedJunction(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	src := t.TempDir()
	writeTree(t, src, map[string]string{"pkg/real.go": "package pkg\n"})
	mklinkJunction(t, filepath.Join(src, "pkg", "linked"), outside)

	snap := create(t, src, Options{Exclude: []glob.Pattern{glob.MustCompile("pkg/linked")}})
	if got := relPaths(snap.Manifest); len(got) != 1 || got[0] != "pkg/real.go" {
		t.Errorf("manifest paths = %v, want [pkg/real.go]", got)
	}
}

// TestCleanupSurvivesAFileLock is the real Windows condition the retry ladder
// exists for, without any timing: a handle opened with no sharing makes the
// file undeletable, so Cleanup must give up with a diagnostic rather than hang
// — and must succeed once the handle is closed.
func TestCleanupSurvivesAFileLock(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"pkg/locked.test": "binary\n"})
	dest := t.TempDir()

	snap, err := Create(src, Options{DestParent: dest})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	locked := filepath.Join(snap.Root, "pkg", "locked.test")
	name, err := syscall.UTF16PtrFromString(locked)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	// Share mode 0: no other handle may be opened, not even to delete.
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Skipf("cannot open %s exclusively on this machine (%v)", locked, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = syscall.CloseHandle(handle)
		}
	}()

	err = snap.Cleanup()
	assertCode(t, err, CodeCleanupFailed)
	if !strings.Contains(err.Error(), snap.Root) {
		t.Errorf("error does not name the snapshot: %v", err)
	}

	if err := syscall.CloseHandle(handle); err != nil {
		t.Fatalf("CloseHandle: %v", err)
	}
	closed = true

	if err := snap.Cleanup(); err != nil {
		t.Fatalf("Cleanup after the handle was released: %v", err)
	}
	if _, err := os.Stat(snap.Root); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("snapshot root survived the second Cleanup (err=%v)", err)
	}
}

// TestCreateAbandonsAPartialCopy exercises the one path where Create has to
// remove work it has already done: a source file that cannot be read after the
// destination directory exists. A file another process holds open with no
// sharing is the realistic version on Windows — a running test binary, an
// editor, a scanner.
func TestCreateAbandonsAPartialCopy(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	// "a.go" is copied first and "z.go" is the one that fails, so the copy is
	// genuinely partial when it is abandoned.
	writeTree(t, src, map[string]string{"a.go": "package a\n", "z.go": "package z\n"})

	name, err := syscall.UTF16PtrFromString(filepath.Join(src, "z.go"))
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Skipf("cannot open a source file exclusively on this machine (%v)", err)
	}
	defer func() { _ = syscall.CloseHandle(handle) }()

	assertAbandoned(t, src, t.TempDir())
}

// TestCleanupClearsReadOnlyFiles covers the one removal failure that waiting
// could never fix. A test fixture marked read-only would otherwise pin the
// snapshot directory in place forever.
func TestCleanupClearsReadOnlyFiles(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"pkg/fixture.txt": "fixture\n"})
	dest := t.TempDir()

	snap, err := Create(src, Options{DestParent: dest})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	readOnly := filepath.Join(snap.Root, "pkg", "fixture.txt")
	if err := os.Chmod(readOnly, 0o444); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if err := snap.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(snap.Root); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("snapshot root survived Cleanup (err=%v)", err)
	}
}
