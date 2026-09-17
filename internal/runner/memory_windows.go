// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"math"

	"golang.org/x/sys/windows"
)

const kernelBoundsMemory = true

const accountedPeakBelongsToTheChild = true

const memorySamplingSupported = true

func memorySamplingAvailable() bool { return true }

func jobPeakMemory(job windows.Handle) (int64, bool) {
	info, err := jobMemoryInfo(job)
	if err != nil {
		return 0, false
	}
	used := uint64(info.PeakJobMemoryUsed)
	if used > math.MaxInt64 {
		return math.MaxInt64, true
	}
	return int64(used), true
}

func limitJobMemory(job windows.Handle, limit int64) error {
	limits := jobLimitsFor(limit)
	if limits.Flags&jobLimitJobMemory == 0 {
		return nil
	}
	return setJobLimits(job, limits)
}
