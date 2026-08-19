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

// TestCreateRejectsIrregularFile covers the third rejection class on the
// platforms that can produce one cheaply. A named pipe is the honest test
// case: copying it would block forever waiting for a writer, and skipping it
// would leave a build referring to a file that is not there.
//
// The Windows counterpart of this test is the junction case in
// junction_windows_test.go, since Windows has no mkfifo.
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

// TestCreateAbandonsAPartialCopy exercises the one path where Create has to
// remove work it has already done: a source file that cannot be read after the
// destination directory exists.
func TestCreateAbandonsAPartialCopy(t *testing.T) {
	t.Parallel()

	if syscall.Geteuid() == 0 {
		t.Skip("root can read a file with no permission bits, so the copy would not fail")
	}
	src := t.TempDir()
	// "a.go" is copied first and "z.go" is the one that fails, so the copy is
	// genuinely partial when it is abandoned.
	writeTree(t, src, map[string]string{"a.go": "package a\n", "z.go": "package z\n"})
	if err := syscall.Chmod(filepath.Join(src, "z.go"), 0); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	assertAbandoned(t, src, t.TempDir())
}

// TestCreatePreservesPermissionBits pins the POSIX half of the mode policy:
// the executable bit on a fixture script has to survive, because a test that
// runs it would otherwise fail inside the snapshot for a reason that has
// nothing to do with any mutant. The directory is checked alongside the file,
// since preserving one and not the other is a policy that only looks applied.
//
// The strict umask is the point of the test, not scenery. Both os.OpenFile and
// os.MkdirAll have their mode argument filtered through it, so a copy relying
// on the creation mode alone lands 0o700 here and 0o755 on a machine whose
// umask happens to be 022 — a green test that proves only how the developer's
// shell was configured. Running under 077 is what makes the explicit chmod the
// thing under test.
//
// It is deliberately not parallel: the umask is process-wide. Parallel tests
// are parked until every top-level test has been entered, so a serial test has
// the process to itself; adding t.Parallel() here would leak 077 into whatever
// copies happened to be running and make their modes depend on scheduling.
func TestCreatePreservesPermissionBits(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"testdata/run.sh": "#!/bin/sh\nexit 0\n"})
	// chmod, unlike creation, is not umask-filtered, so the fixture holds the
	// modes it claims whatever the machine's umask is when the test starts.
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
