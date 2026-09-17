// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package snapshot

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCreateRejectsIrregularFile(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeTree(t, src, map[string]string{"pkg/real.go": "package pkg\n"})
	fifo := filepath.Join(src, "pkg", "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a named pipe on this machine (%v)", err)
	}

	_, err := Create(src, Options{DestParent: t.TempDir()})
	assertCode(t, err, CodeIrregular)
	if !strings.Contains(err.Error(), "pkg/pipe") {
		t.Errorf("error does not name the offending path: %v", err)
	}
}

func TestCreateAbandonsAPartialCopy(t *testing.T) {
	t.Parallel()

	if syscall.Geteuid() == 0 {
		t.Skip("root can read a file with no permission bits, so the copy would not fail")
	}
	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.go": "package a\n", "z.go": "package z\n"})
	if err := syscall.Chmod(filepath.Join(src, "z.go"), 0); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	assertAbandoned(t, src, t.TempDir())
}

func TestCreatePreservesPermissionBits(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"testdata/run.sh": "#!/bin/sh\nexit 0\n"})
	for _, rel := range []string{"testdata", "testdata/run.sh"} {
		abs := filepath.Join(src, filepath.FromSlash(rel))
		if err := syscall.Chmod(abs, 0o755); err != nil {
			t.Fatalf("Chmod(%s): %v", rel, err)
		}
	}

	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	snap := create(t, src, Options{})

	for _, rel := range []string{"testdata", "testdata/run.sh"} {
		abs := filepath.Join(snap.Root, filepath.FromSlash(rel))
		var st syscall.Stat_t
		if err := syscall.Stat(abs, &st); err != nil {
			t.Fatalf("Stat(%s): %v", rel, err)
		}
		if perm := st.Mode & 0o777; perm != 0o755 {
			t.Errorf("copied mode of %s = %#o, want %#o", rel, perm, 0o755)
		}
	}
}
