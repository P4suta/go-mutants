// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package snapshot

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// copyPerm returns the mode a copied file is created with. Windows has no
// POSIX permission bits: the only thing the mode argument can express is
// FILE_ATTRIBUTE_READONLY, which os sets when the mode has no write bit.
//
// Propagating that attribute would be actively harmful. A read-only source
// file would produce a read-only copy, and the instrumentation phase has to
// replace files in the snapshot — on Windows a rename over a read-only file
// fails with ERROR_ACCESS_DENIED. So every copied file is created writable.
// Nothing is lost: the snapshot is a private, disposable directory, and the
// source tree it came from is never written to at all.
func copyPerm(fs.FileMode) fs.FileMode { return 0o666 }

// dirPerm returns the mode a copied directory is created with, writable for
// the same reason.
func dirPerm(fs.FileMode) fs.FileMode { return 0o777 }

// finalizePerm does nothing on Windows. Its POSIX counterpart exists to undo
// the umask filtering that os.OpenFile's mode argument goes through; Windows
// has no umask and no permission bits to restore, and the one thing a mode can
// express here — FILE_ATTRIBUTE_READONLY — is deliberately not propagated, as
// [copyPerm] explains.
func finalizePerm(*os.File, fs.FileMode) error { return nil }

// finalizeDirPerm does nothing on Windows, for the same reason.
func finalizeDirPerm(string, fs.FileMode) error { return nil }

// clearReadOnly best-effort clears FILE_ATTRIBUTE_READONLY from everything
// under root, which is the one removal failure a retry can actually fix
// without waiting: a file a test marked read-only will refuse to be deleted
// forever, however long the backoff. Errors are ignored because this is a last
// attempt to make the next removal succeed, and the removal reports its own
// failure.
func clearReadOnly(root string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Keep walking: one unreadable subtree should not stop the rest
			// from being unlocked.
			return nil
		}
		if !d.IsDir() {
			_ = os.Chmod(extendedPath(path), 0o666)
		}
		return nil
	})
}

// isReparsePoint reports whether fi describes a reparse point: a junction, a
// mount point, a OneDrive placeholder, a deduplicated file, anything that is a
// name or a promise rather than the bytes themselves.
//
// The attribute is read straight out of the Win32 data rather than inferred
// from fs.FileMode. Go's mapping of reparse tags to modes has changed across
// releases — junctions were links before Go 1.23 and are irregular after — and
// the tags themselves are open ended. This package's answer must not depend on
// which of those a toolchain implements, so it asks the question Windows
// actually answers: is FILE_ATTRIBUTE_REPARSE_POINT set.
func isReparsePoint(fi fs.FileInfo) bool {
	data, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false
	}
	return data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// pathsEqual reports whether two paths name the same directory, for the
// Cleanup guard. Windows file systems are conventionally case insensitive, and
// the temporary directory in particular reaches a program through environment
// variables that disagree about case ("C:\Users\Me\AppData\Local\Temp" from
// one API, "C:\USERS\ME\APPDATA\LOCAL\TEMP" from another). Comparing case
// sensitively would make the guard refuse a directory it created itself.
func pathsEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
