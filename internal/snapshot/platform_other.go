// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package snapshot

import (
	"io/fs"
	"os"
	"path/filepath"
)

func copyPerm(mode fs.FileMode) fs.FileMode { return mode.Perm() }

func dirPerm(mode fs.FileMode) fs.FileMode { return mode.Perm() | 0o700 }

func finalizePerm(f *os.File, mode fs.FileMode) error { return f.Chmod(mode.Perm()) }

func finalizeDirPerm(path string, mode fs.FileMode) error { return os.Chmod(path, mode.Perm()) }

func clearReadOnly(string) {}

func isReparsePoint(fs.FileInfo) bool { return false }

func pathsEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
