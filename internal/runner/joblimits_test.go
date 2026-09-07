// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"math"
	"testing"
)

// TestJobLimitsPairEveryFlagWithTheValueItNames is the arithmetic underneath
// the Windows job object, checked on every platform because it is not a
// Windows fact.
//
// SetInformationJobObject answers ERROR_INVALID_PARAMETER for a
// JOBOBJECT_EXTENDED_LIMIT_INFORMATION whose LimitFlags names a limit the
// structure does not carry a value for — and for the reverse, a value with no
// flag naming it, which is worse than an error because it is silently ignored
// and the job runs unbounded. Both are decided here, in ordinary integer code
// that a Linux `go test` can run, rather than on the one platform where getting
// it wrong is a failed run nobody can reproduce.
//
// Kill-on-close is in every set, and that is the case easiest to lose:
// [setJobLimits] replaces the whole structure, so a call that carried only the
// memory line would take the backstop away — and the backstop is what the
// package's promise to kill a whole tree rests on.
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
		{"the largest bound an int64 holds", math.MaxInt64, true},
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
			// Unsigned, because the kernel's line is a uintptr and the
			// headroom above a bound near the top of an int64 is not one.
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

// TestTheKernelsMemoryLineSitsAboveTheSamplers is the one claim about the
// Windows bound that is not a claim about Windows at all, which is why it runs
// everywhere.
//
// JOB_OBJECT_LIMIT_JOB_MEMORY does not kill a job that reaches its limit: it
// makes the offending commit *fail*, and it caps the job's own accounting at
// the limit while doing so. Set the kernel's line to the sampler's number and
// PeakJobMemoryUsed can never exceed it, so `used > limit` is never true, the
// tree is never killed by go-mutants, and the child dies of a failed allocation
// with a non-zero status and no `memory_exceeded` anywhere — the whole
// user-visible half of this feature, silently absent on one platform.
//
// So the kernel's line sits a quarter above the sampler's. The sampler is what
// reports; the kernel is the backstop for a sampler that somehow stopped, and a
// backstop that fires first is not a backstop.
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

	// And the invariant itself, over the whole range and on either word size:
	// either the kernel is not asked for a line at all, or the line it is given
	// is strictly above the sampler's. A headroom that overflows the field is
	// clamped to the field's ceiling, which is still above every bound the
	// field can hold; a bound the field cannot hold at all is left to the
	// sampler, because no line above it exists to give.
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
