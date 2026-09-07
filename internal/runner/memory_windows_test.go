// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
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
