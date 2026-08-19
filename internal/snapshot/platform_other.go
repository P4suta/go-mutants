// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package snapshot

import (
	"io/fs"
	"os"
	"path/filepath"
)

// copyPerm returns the mode a copied file is created with. On POSIX the source
// file's permission bits are propagated exactly, executable bit included,
// because a testdata fixture or a script in the tree may depend on them.
//
// A read-only source file therefore lands read-only in the snapshot. That is
// safe for the instrumentation phase only because every rewrite in go-mutants
// is a write to a temporary file followed by an atomic rename, which needs
// write permission on the directory and none on the file being replaced. The
// directories are created writable by [dirPerm] for exactly that reason.
func copyPerm(mode fs.FileMode) fs.FileMode { return mode.Perm() }

// dirPerm returns the mode a copied directory is created with: the source
// permissions, forced to owner rwx. A source tree may legitimately contain a
// r-x directory; the copy has to be writable or nothing can be instrumented
// inside it, and nothing but this process ever looks at the copy.
func dirPerm(mode fs.FileMode) fs.FileMode { return mode.Perm() | 0o700 }

// finalizePerm sets an open file's permissions to mode, and is what actually
// makes [copyPerm]'s answer true.
//
// The mode argument of os.OpenFile is a request the kernel filters through the
// process umask, so it is only ever a floor: a 0o755 source creates a 0o750
// file under umask 027 and a 0o700 file under umask 077. Preserving the source
// bits therefore cannot be done at creation time at all — it takes an explicit
// chmod, which no umask applies to.
//
// It works through the descriptor rather than the path so the bits land on the
// file that was just written, whatever has happened to the name meanwhile.
func finalizePerm(f *os.File, mode fs.FileMode) error { return f.Chmod(mode.Perm()) }

// finalizeDirPerm sets a directory's permissions to mode, for exactly the
// reason [finalizePerm] exists: os.MkdirAll's mode argument is umask-filtered
// too, so [dirPerm]'s answer is a floor until this call makes it exact.
func finalizeDirPerm(path string, mode fs.FileMode) error { return os.Chmod(path, mode.Perm()) }

// clearReadOnly does nothing on POSIX, where removing a file depends on the
// containing directory's permissions rather than the file's own.
func clearReadOnly(string) {}

// isReparsePoint reports whether fi describes a Windows reparse point, and so
// is always false here. POSIX has no such thing: a symbolic link is reported
// as a link and every other special file as irregular by the walk itself.
func isReparsePoint(fs.FileInfo) bool { return false }

// pathsEqual reports whether two paths name the same directory, for the
// Cleanup guard. POSIX paths are case sensitive, so cleaning and comparing is
// the whole of it. This is a comparison of spellings and not of inodes on
// purpose: the guard asks whether Root is still the path Create produced, and
// a symlink that has since been pointed at it is not an answer of yes.
func pathsEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
