//go:build !windows

// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tempowner

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryAdvisoryLock takes the BSD flock on the open file description, which is
// what makes the lock disappear when the process dies however it died — the
// property the whole sweep rests on.
//
// EWOULDBLOCK and EAGAIN are the same errno on Linux and different ones on some
// other unices, so both are read as "somebody else holds it" rather than as a
// failure.
func tryAdvisoryLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return false, err
}

func unlockAdvisory(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }
