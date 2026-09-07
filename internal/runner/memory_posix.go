// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package runner

import (
	"os"
	"syscall"
)

// peakRSSOf reads the peak resident set size the kernel recorded for a reaped
// child, in bytes, and reports whether there was one.
//
// The number comes from wait4's rusage, which is filled in for every child this
// package reaps — a child that exited, and equally one whose tree was killed —
// so a run stopped by a bound still reports how far it got. It covers the child
// and every descendant the child itself waited for; a grandchild that outlived
// its parent contributed to the samples taken while it ran (see
// [memoryWatchdog]) but not to this. That approximation is stated rather than
// hidden because the two numbers are combined in [Run]: the peak reported is
// the larger of what was sampled and what was accounted.
//
// ru_maxrss is a high-water mark, not a reading, which is why nothing has to be
// timed to catch it — and, on Linux, why it is not the child's: see
// [accountedPeakBelongsToTheChild] there. [peakOf] decides per platform whether
// this number is consulted at all.
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
