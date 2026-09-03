//go:build windows

// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryAdvisoryLock takes a one-byte exclusive range lock on the file handle.
// LOCKFILE_FAIL_IMMEDIATELY is what turns the blocking wait into the answer a
// sweep needs, and the lock is released by the operating system when the handle
// closes — including when the process is killed.
func tryAdvisoryLock(file *os.File) (bool, error) {
	overlapped := new(windows.Overlapped)
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, overlapped,
	)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return false, err
}

func unlockAdvisory(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, new(windows.Overlapped))
}
