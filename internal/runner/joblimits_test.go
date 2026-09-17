// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"fmt"
	"math"
	"math/bits"
	"testing"
)

func fitsUnderTheCeiling(limit int64) bool {
	return limit > 0 && uint64(limit) < uint64(^uintptr(0))
}

func TestJobLimitsPairEveryFlagWithTheValueItNames(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name          string
		memoryLimit   int64
		wantJobMemory bool
	}{
		{"no bound at all", 0, false},
		{"a negative bound", -1, false},
		{"an ordinary bound", 1 << 30, true},
		{"a bound of one byte", 1, true},
		{
			fmt.Sprintf("the largest bound an int64 holds, which only a 64-bit uintptr can sit above "+
				"(this build has %d)", bits.UintSize),
			math.MaxInt64,
			fitsUnderTheCeiling(math.MaxInt64),
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			limits := jobLimitsFor(c.memoryLimit)

			if limits.Flags&jobLimitKillOnClose == 0 {
				t.Errorf("LimitFlags = %#x, want the kill-on-close backstop in every set: "+
					"the call replaces the whole structure", limits.Flags)
			}
			named := limits.Flags&jobLimitJobMemory != 0
			if named != c.wantJobMemory {
				t.Errorf("LimitFlags = %#x names the job memory limit = %v, want %v",
					limits.Flags, named, c.wantJobMemory)
			}
			if named != (limits.JobMemory != 0) {
				t.Errorf("LimitFlags = %#x names the job memory limit = %v while JobMemoryLimit = %d: "+
					"a flag without its value is refused and a value without its flag is ignored",
					limits.Flags, named, limits.JobMemory)
			}
			if named && uint64(limits.JobMemory) <= uint64(c.memoryLimit) {
				t.Errorf("JobMemoryLimit = %d, which is not strictly above the sampler's line %d",
					limits.JobMemory, c.memoryLimit)
			}
			if extra := limits.Flags &^ (jobLimitKillOnClose | jobLimitJobMemory); extra != 0 {
				t.Errorf("LimitFlags = %#x carries %#x, which names a limit this structure sets no "+
					"value for", limits.Flags, extra)
			}
		})
	}
}

func TestTheKernelsMemoryLineSitsAboveTheSamplers(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name    string
		limit   int64
		want    uintptr
		wantSet bool
	}{
		{"an ordinary bound", 1 << 30, uintptr(1<<30 + 1<<28), true},
		{"a small bound", 4096, 5120, true},
		{"a bound so small the quarter rounds away", 1, 2, true},
		{"and one just above it", 3, 4, true},
		{"no bound at all", 0, 0, false},
		{"a negative bound", -1, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, set := kernelJobMemoryLimit(c.limit)
			if set != c.wantSet {
				t.Fatalf("kernelJobMemoryLimit(%d) set = %v, want %v", c.limit, set, c.wantSet)
			}
			if set && got != c.want {
				t.Errorf("kernelJobMemoryLimit(%d) = %d, want %d", c.limit, got, c.want)
			}
			if set && int64(got) <= c.limit {
				t.Errorf("kernelJobMemoryLimit(%d) = %d, which is not strictly above the sampler's line",
					c.limit, got)
			}
		})
	}

	for _, limit := range []int64{1, 4096, 1 << 20, 1 << 30, math.MaxInt64 / 2, math.MaxInt64} {
		got, set := kernelJobMemoryLimit(limit)
		if !set {
			continue
		}
		if uint64(got) <= uint64(limit) {
			t.Errorf("kernelJobMemoryLimit(%d) = %d, which is not strictly above the sampler's line",
				limit, got)
		}
	}
}
