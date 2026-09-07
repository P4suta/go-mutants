// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"math"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// memorySamplingSupported: the job object accounts for the whole tree at any
// moment, so a bound is both measured and enforced here — twice over, in fact.
// See [jobSupervisor.usedMemory].
const memorySamplingSupported = true

// memorySamplingAvailable has nothing to probe. A job object that cannot be
// created is already fatal to the run that asked for it — see [newSupervisor],
// which is fail-closed — so a machine that reaches a bounded run at all can
// account for one.
func memorySamplingAvailable() bool { return true }

// jobMemoryInfo reads the job's extended limit information, which is where
// Windows keeps both what the job is allowed and what it has used.
//
// One query answers every question this package asks about a job's memory: the
// peak for [Result.PeakMemory], the same peak as the sample a bound is checked
// against, and the limit itself for the test that proves the kernel was told.
func jobMemoryInfo(job windows.Handle) (windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION, error) {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	// The untyped pointer plus a length is the documented calling convention
	// rather than pointer arithmetic; KeepAlive pins the value for the call.
	err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	)
	runtime.KeepAlive(&info)
	return info, err
}

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

// kernelMemoryHeadroomDivisor sets how far above the sampler's line the
// kernel's own line sits: a quarter of the bound.
//
// It is a quarter rather than a token amount because the gap has to be larger
// than a plateau the sampler could sit on. A commit is chunk-granular — a Go
// heap grows in spans, not bytes — so a job pressed against a kernel limit
// stops at whatever multiple of a span fits under it, and the sampler has to be
// able to read a number *strictly above* the bound before that happens. A
// quarter of the bound is many spans at every bound worth setting.
const kernelMemoryHeadroomDivisor = 4

// kernelJobMemoryLimit is the line the kernel is given, and whether to give it
// one at all.
//
// It sits strictly above the sampler's, and that inequality is the whole point
// of this function. JOB_OBJECT_LIMIT_JOB_MEMORY does not kill a job that
// reaches its limit: it makes the offending commit *fail*, and it caps the
// job's own accounting at the limit while doing so. Set the kernel's line to
// the sampler's number and PeakJobMemoryUsed can never exceed it — so
// `used > limit` is never true, go-mutants never kills the tree, and the child
// dies of a failed allocation with a non-zero status and nothing anywhere
// saying why. Every user-visible half of the bound would be missing on one
// platform and present on the other two.
//
// So the sampler is what reports and the kernel is the backstop underneath it:
// the line that catches a sampler which somehow stopped, and a backstop that
// fires first is not a backstop.
//
// Two limits of the field are handled rather than truncated into. A bound whose
// headroom does not fit a uintptr is clamped to the field's ceiling, which is
// still strictly above every bound the field can hold. A bound the field cannot
// hold *at all* — only reachable on a 32-bit build — gets no kernel line,
// because there is no line above it to give; the sampler is the whole of the
// bound there, which is what it is on POSIX in any case.
func kernelJobMemoryLimit(limit int64) (uintptr, bool) {
	const ceiling = uint64(^uintptr(0))
	if limit <= 0 || uint64(limit) >= ceiling {
		return 0, false
	}
	// max(_, 1) because integer division collapses for a small enough bound:
	// a limit of one byte would otherwise put the kernel's line at one byte
	// too, which is the exact equality this whole function exists to avoid.
	// The bounds that reach here in practice are hundreds of megabytes, so the
	// clamp costs nothing and closes the case the unit test found.
	headroom := uint64(limit) + max(uint64(limit)/kernelMemoryHeadroomDivisor, 1)
	if headroom > ceiling || headroom < uint64(limit) {
		return uintptr(ceiling), true
	}
	return uintptr(headroom), true
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
func limitJobMemory(job windows.Handle, limit int64) error {
	kernelLimit, set := kernelJobMemoryLimit(limit)
	if !set {
		return nil
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			// The kill-on-close backstop is not a separate setting: this call
			// replaces the whole structure, so it has to carry every flag the
			// job is meant to keep.
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_JOB_MEMORY,
		},
		JobMemoryLimit: kernelLimit,
	}
	_, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	runtime.KeepAlive(&info)
	return err
}
