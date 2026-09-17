// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package runner

import (
	"os"
	"syscall"
)

func peakRSSOf(ps *os.ProcessState) (int64, bool) {
	if ps == nil {
		return 0, false
	}
	usage, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok || usage.Maxrss <= 0 {
		return 0, false
	}
	return int64(usage.Maxrss) * maxRSSUnit, true
}
