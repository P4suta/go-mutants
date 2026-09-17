// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package cli

import (
	"errors"
	"os"
	"syscall"
)

func removeDirectory(path string) error {
	err := syscall.Rmdir(path)
	if errors.Is(err, syscall.ENOTDIR) {
		return refusedKind(path)
	}
	return err
}

func refusedKind(path string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errIsALink
	}
	return errNotDirectory
}
