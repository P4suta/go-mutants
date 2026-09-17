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

func copyPerm(fs.FileMode) fs.FileMode { return 0o666 }

func dirPerm(fs.FileMode) fs.FileMode { return 0o777 }

func finalizePerm(*os.File, fs.FileMode) error { return nil }

func finalizeDirPerm(string, fs.FileMode) error { return nil }

func clearReadOnly(root string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			_ = os.Chmod(ExtendedPath(path), 0o666)
		}
		return nil
	})
}

func isReparsePoint(fi fs.FileInfo) bool {
	data, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false
	}
	return data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func pathsEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
