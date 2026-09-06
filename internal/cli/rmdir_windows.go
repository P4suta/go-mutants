// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package cli

import (
	"errors"

	"golang.org/x/sys/windows"
)

// removeDirectory removes an empty directory and can remove nothing else. See
// the other platform's copy for why the collector may not use [os.Remove] here.
//
// RemoveDirectory is the Windows spelling of rmdir and refuses a file with
// ERROR_DIRECTORY. That constant is taken from golang.org/x/sys/windows rather
// than from syscall, which does not export it — and syscall.ENOTDIR is no
// substitute: on Windows it is defined as ERROR_PATH_NOT_FOUND, which
// [syscall.Errno.Is] answers as [fs.ErrNotExist], so a file would come back
// indistinguishable from a path that was never there.
func removeDirectory(path string) error {
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err = windows.RemoveDirectory(wide); errors.Is(err, windows.ERROR_DIRECTORY) {
		return errNotDirectory
	}
	return err
}
