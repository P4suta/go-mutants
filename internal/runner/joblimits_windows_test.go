// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

// TestTheJobLimitFlagsAreTheSystemsOwn is the guard on the one thing
// [jobLimits] buys its portability with: a second spelling of a constant.
//
// The flags live in joblimits.go so that the rule pairing each of them with the
// value it names can be checked by a `go test` on any machine. That is worth
// having and it is worth exactly nothing if the numbers drift, because the
// failure would be a job configured with a flag naming a *different* limit from
// the value beside it — refused by the kernel at best, and silently unbounded
// at worst.
func TestTheJobLimitFlagsAreTheSystemsOwn(t *testing.T) {
	t.Parallel()

	if got, want := jobLimitKillOnClose, uint32(windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE); got != want {
		t.Errorf("jobLimitKillOnClose = %#x, want JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = %#x", got, want)
	}
	if got, want := jobLimitJobMemory, uint32(windows.JOB_OBJECT_LIMIT_JOB_MEMORY); got != want {
		t.Errorf("jobLimitJobMemory = %#x, want JOB_OBJECT_LIMIT_JOB_MEMORY = %#x", got, want)
	}
}

// TestConcurrentSupervisorsEachGetTheirOwnLimits is the regression test for the
// flake that produced this file.
//
// windows-latest reported `GOM7201: could not set kill-on-close on the Windows
// job object that owns the child process tree: The parameter is incorrect.`
// once, on 2026-09-06, under the eight concurrent Workspace.Exec calls of the
// root package's TestConcurrentExecutionsUnderKeepTempRecordAndPreserveEveryScratch,
// and passed on every other Windows run. The structure handed to
// SetInformationJobObject was a stack local whose address had been converted to
// a uintptr one Go frame too early; a goroutine whose stack grew inside the
// x/sys wrapper had its frames copied and its old stack span returned to the
// pool, and under concurrency another goroutine was there to write over it
// before the kernel read it. See joblimits_windows.go.
//
// Which is why this test is concurrent and why it grows a stack first: those
// are the two conditions, and neither of them is arranged by a job object test
// that creates one supervisor at a time. It cannot fail deterministically —
// nothing that depends on a stack copy landing inside one call can — so it
// asserts on what the job actually carries rather than only on the error, and
// the gate that holds the rule for good is
// TestEveryPointerHandedToASyscallIsConvertedInsideTheCall.
func TestConcurrentSupervisorsEachGetTheirOwnLimits(t *testing.T) {
	t.Parallel()

	const supervisors = 16
	const limit = 512 << 20

	var group sync.WaitGroup
	for i := range supervisors {
		group.Add(1)
		go func() {
			defer group.Done()
			// A distinct bound per goroutine, so that a job configured from
			// another goroutine's structure is a wrong number rather than a
			// coincidence.
			bound := int64(limit + i*(1<<20))
			// A stack deep enough to have been grown at least once, so that the
			// call below is made on a frame the runtime has already moved.
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

// deepen calls body from depth frames down, with enough of a local in each that
// the goroutine's stack has to grow to hold them.
//
// The growth is the point rather than the depth: a stack that has been copied
// is a stack whose old span is back in the pool, which is the condition the
// defect this file's fix is about needed.
func deepen(depth int, body func()) {
	if depth <= 0 {
		body()
		return
	}
	var frame [512]byte
	deepen(depth-1, body)
	_ = frame
}
