// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

func TestTheJobLimitFlagsAreTheSystemsOwn(t *testing.T) {
	t.Parallel()

	if got, want := jobLimitKillOnClose, uint32(windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE); got != want {
		t.Errorf("jobLimitKillOnClose = %#x, want JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = %#x", got, want)
	}
	if got, want := jobLimitJobMemory, uint32(windows.JOB_OBJECT_LIMIT_JOB_MEMORY); got != want {
		t.Errorf("jobLimitJobMemory = %#x, want JOB_OBJECT_LIMIT_JOB_MEMORY = %#x", got, want)
	}
}

func TestConcurrentSupervisorsEachGetTheirOwnLimits(t *testing.T) {
	t.Parallel()

	const supervisors = 16
	const limit = 512 << 20

	var group sync.WaitGroup
	for i := range supervisors {
		group.Add(1)
		go func() {
			defer group.Done()
			bound := int64(limit + i*(1<<20))
			deepen(24, func() {
				sup, err := newSupervisor(bound)
				if err != nil {
					t.Errorf("newSupervisor(%d): %v", bound, err)
					return
				}
				defer sup.release()

				job, ok := sup.(*jobSupervisor)
				if !ok {
					t.Errorf("newSupervisor returned %T, want a *jobSupervisor on this platform", sup)
					return
				}
				info, err := jobMemoryInfo(job.job)
				if err != nil {
					t.Errorf("querying the job object bounded at %d: %v", bound, err)
					return
				}
				want, set := kernelJobMemoryLimit(bound)
				if !set {
					t.Errorf("kernelJobMemoryLimit(%d) declined to set a line at all", bound)
					return
				}
				if got := info.JobMemoryLimit; got != want {
					t.Errorf("JobMemoryLimit = %d, want %d for the bound %d this goroutine asked for",
						got, want, bound)
				}
				flags := info.BasicLimitInformation.LimitFlags
				if flags != jobLimitsFor(bound).Flags {
					t.Errorf("LimitFlags = %#x, want %#x", flags, jobLimitsFor(bound).Flags)
				}
			})
		}()
	}
	group.Wait()
}

func deepen(depth int, body func()) {
	if depth <= 0 {
		body()
		return
	}
	var frame [512]byte
	deepen(depth-1, body)
	_ = frame
}
