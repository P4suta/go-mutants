// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"math"
	"testing"

	"golang.org/x/sys/windows"
)

// TestTheJobObjectCarriesTheMemoryLimitAndKeepsKillOnClose is the one claim
// about the Windows bound that no observation of a finished [Run] can make.
//
// The sampler would kill a runaway tree on this platform whether or not the
// kernel had been told anything, so a behavioural test proves only that *one*
// of the two mechanisms works. This looks at the job object itself: the limit
// is there, the flag that enforces it is there, and — the part that is easy to
// lose, because the limit is set by replacing the whole structure — the
// kill-on-close backstop the package's central promise rests on is still there
// beside it.
//
// It is an internal test because a job handle is not, and must not become, part
// of this package's API.
func TestTheJobObjectCarriesTheMemoryLimitAndKeepsKillOnClose(t *testing.T) {
	t.Parallel()

	const limit = 512 << 20

	sup, err := newSupervisor(limit)
	if err != nil {
		t.Fatalf("newSupervisor(%d): %v", limit, err)
	}
	t.Cleanup(sup.release)

	job, ok := sup.(*jobSupervisor)
	if !ok {
		t.Fatalf("newSupervisor returned %T, want a *jobSupervisor on this platform", sup)
	}
	info, err := jobMemoryInfo(job.job)
	if err != nil {
		t.Fatalf("querying the job object: %v", err)
	}

	// The job carries the *kernel's* line, which sits above the sampler's; see
	// [kernelJobMemoryLimit] for why they may not be the same number.
	want, set := kernelJobMemoryLimit(limit)
	if !set {
		t.Fatalf("kernelJobMemoryLimit(%d) declined to set a line at all", limit)
	}
	if got := info.JobMemoryLimit; got != want {
		t.Errorf("JobMemoryLimit = %d, want %d, the line above the sampler's %d", got, want, limit)
	}
	if uint64(want) <= uint64(limit) {
		t.Errorf("the kernel's line %d is not above the sampler's %d", want, limit)
	}
	flags := info.BasicLimitInformation.LimitFlags
	if flags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY == 0 {
		t.Errorf("LimitFlags = %#x, want JOB_OBJECT_LIMIT_JOB_MEMORY set: the limit is recorded and not enforced",
			flags)
	}
	if flags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		t.Errorf("LimitFlags = %#x, want JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE kept: "+
			"setting the memory limit replaces the whole structure and has dropped the backstop", flags)
	}
}

// TestAnUnboundedJobObjectAsksTheKernelForNoLimit is the other half: a run with
// no bound must leave the job exactly as it was before the bound existed, so
// that the thousands of unbounded commands a run issues are not quietly given
// one.
func TestAnUnboundedJobObjectAsksTheKernelForNoLimit(t *testing.T) {
	t.Parallel()

	sup, err := newSupervisor(0)
	if err != nil {
		t.Fatalf("newSupervisor(0): %v", err)
	}
	t.Cleanup(sup.release)

	job, ok := sup.(*jobSupervisor)
	if !ok {
		t.Fatalf("newSupervisor returned %T, want a *jobSupervisor on this platform", sup)
	}
	info, err := jobMemoryInfo(job.job)
	if err != nil {
		t.Fatalf("querying the job object: %v", err)
	}
	if flags := info.BasicLimitInformation.LimitFlags; flags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY != 0 {
		t.Errorf("LimitFlags = %#x, want JOB_OBJECT_LIMIT_JOB_MEMORY clear for an unbounded run", flags)
	}
}

// TestTheKernelsMemoryLineSitsAboveTheSamplers is the arithmetic B1 turns on,
// and it is the one claim about the Windows bound that is not a claim about
// Windows at all.
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
