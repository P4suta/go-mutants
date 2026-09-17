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
// EWOULDBLOCK and EAGAIN are the same errno on Linux and macOS and are allowed
// by POSIX to differ elsewhere, so both are read as "somebody else holds it"
// rather than as a failure. They are two cases of one switch rather than the
// two halves of an `||` for a reason worth stating, because the `||` is the
// more ordinary spelling: where the two constants are equal the two calls are
// one predicate, so `||` and `&&` there select the same branch on every input.
// That is a mutant no test can kill and no ledger row can honestly declare --
// its argument would hold only on the platforms this project's own gate happens
// to run on -- and a `switch` says the same thing to a reader while proposing
// nothing to mutate.
func tryAdvisoryLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	switch {
	case errors.Is(err, unix.EWOULDBLOCK), errors.Is(err, unix.EAGAIN):
		return false, nil
	}
	return false, err
}

func unlockAdvisory(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }
