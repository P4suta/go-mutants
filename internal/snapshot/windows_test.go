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

func mklinkJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("cannot create a junction with mklink /J on this machine (%v): %s", err, out)
	}
}

func TestCreateRejectsJunction(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"elsewhere.go": "package elsewhere\n"})

	src := t.TempDir()
	writeTree(t, src, map[string]string{"pkg/real.go": "package pkg\n"})
	link := filepath.Join(src, "pkg", "linked")
	mklinkJunction(t, link, outside)

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

func TestCreateAbandonsAPartialCopy(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
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
