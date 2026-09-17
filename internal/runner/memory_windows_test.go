// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"testing"

	"golang.org/x/sys/windows"
)

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
