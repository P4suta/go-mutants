// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"math"

	"golang.org/x/sys/windows"
)

// kernelBoundsMemory: the job object carries a memory line of its own, so a
// tree can be ended by the kernel rather than by the sampler and
// [exceededAtExit] has something to detect.
const kernelBoundsMemory = true

// memorySamplingSupported: the job object accounts for the whole tree at any
// moment, so a bound is both measured and enforced here — twice over, in fact.
// See [jobSupervisor.usedMemory].
const memorySamplingSupported = true

// memorySamplingAvailable has nothing to probe. A job object that cannot be
// created is already fatal to the run that asked for it — see [newSupervisor],
// which is fail-closed — so a machine that reaches a bounded run at all can
// account for one.
func memorySamplingAvailable() bool { return true }

// jobPeakMemory is the highest committed memory the whole job has held, in
// bytes, and whether the kernel would say.
//
// PeakJobMemoryUsed is committed memory rather than a resident set, which is
// the honest difference between this platform and the other one: Windows
// accounts for what a process has claimed and Linux for what it has touched,
// and a Go program's numbers are close but never equal. Both answer the
// question a budget asks — how much of the machine is this mutant taking — and
// neither is converted into the other, because a conversion between two things
// the kernels measure differently would be a number go-mutants invented.
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

// limitJobMemory asks the kernel to enforce a line above limit on the job
// itself.
//
// It is set in addition to the sampler rather than instead of it, and the two
// fail differently on purpose. JOB_OBJECT_LIMIT_JOB_MEMORY makes the offending
// commit *fail*: the Go runtime then dies with its own out-of-memory message
// and a non-zero status, which is a bounded process but one this package would
// report as an ordinary failing test. The sampler is what turns that into
// [Result.MemoryExceeded]. Keeping the kernel limit underneath it means a
// machine where the sampler somehow stops still cannot be taken down by one
// mutant, which is the incident this whole mechanism exists for.
//
// The kill-on-close backstop travels with the memory line rather than being a
// separate setting, because [setJobLimits] replaces the whole structure and a
// call carrying only the memory line would take the backstop away.
// [jobLimitsFor] is where that set is decided, in portable code, so that the
// pairing of each flag with the value it names is checked on every platform.
func limitJobMemory(job windows.Handle, limit int64) error {
	limits := jobLimitsFor(limit)
	if limits.Flags&jobLimitJobMemory == 0 {
		return nil
	}
	return setJobLimits(job, limits)
}
