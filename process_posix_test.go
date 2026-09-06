// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration && !windows

package gomutants_test

import (
	"errors"
	"fmt"
	"syscall"
)

// processIsGone reports whether the process with this pid has ended.
//
// Signal 0 is the POSIX liveness probe: the kernel does every permission and
// existence check and delivers nothing. ESRCH is the answer this test wants —
// no such process — and it is a *reliable* answer here rather than the usual
// half-truth about zombies, because the process being asked after is one this
// process started and has already waited for. internal/runner reaps every child
// it supervises before Exec returns, so the entry is gone from the table rather
// than lingering unreaped.
//
// EPERM means alive and owned by somebody else. That should be impossible for a
// target this suite started, and it is reported as alive rather than as an
// error, because a test asking "did the thing I killed die" has its answer
// either way: something is still running under that pid.
func processIsGone(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	switch {
	case errors.Is(err, syscall.ESRCH):
		return true, nil
	case err == nil, errors.Is(err, syscall.EPERM):
		return false, nil
	default:
		return false, fmt.Errorf("probing pid %d with signal 0: %w", pid, err)
	}
}
