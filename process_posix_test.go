// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration && !windows

package gomutants_test

import (
	"errors"
	"fmt"
	"syscall"
)

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
