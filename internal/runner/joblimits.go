// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

// What a Windows job object is asked to enforce, described without reference to
// Windows.
//
// A job object is configured by handing SetInformationJobObject one
// JOBOBJECT_EXTENDED_LIMIT_INFORMATION: a bitmask naming which limits the job
// carries, and one field per limit holding its value. The kernel answers
// ERROR_INVALID_PARAMETER when the two disagree — a flag whose value field was
// never set — and silently ignores the other direction, a value no flag names,
// which is worse: the job then runs unbounded and nothing says so. That pairing
// is the whole of what there is to get right here, and it is arithmetic rather
// than a system call.
//
// So it lives in a file no build tag hides, and it is checked by a `go test`
// on every platform rather than only on the one where a mistake in it is a
// failed run. [setJobLimits] is where these numbers meet Windows' own; that the
// two agree is a test of its own, in joblimits_windows_test.go, because two
// spellings of one constant fail in the worst possible direction — a job
// configured with a flag naming a different limit from the value beside it.

// The LimitFlags bits this package sets, which are Windows'
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE and JOB_OBJECT_LIMIT_JOB_MEMORY.
const (
	jobLimitKillOnClose uint32 = 0x00002000
	jobLimitJobMemory   uint32 = 0x00000200
)

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

// jobLimits is one job object's configuration: the flag bits, and the value
// each flag names.
type jobLimits struct {
	// Flags is the LimitFlags mask, and every bit in it names a field below
	// that carries a value.
	Flags uint32
	// JobMemory is the committed memory the whole job is allowed, in bytes. It
	// is non-zero exactly when [jobLimitJobMemory] is set in Flags.
	JobMemory uintptr
}

// jobLimitsFor is the configuration a job object holding a tree bounded at
// memoryLimit is given.
//
// Kill-on-close is unconditional, and that is the case easiest to lose:
// SetInformationJobObject replaces the whole structure, so a call carrying only
// the memory line would take the backstop away — and the backstop is what this
// package's promise to kill a whole tree rests on when go-mutants itself
// panics, is killed, or forgets. The memory line is added only when there is
// one to add, and it is the *kernel's* line rather than the sampler's; see
// [kernelJobMemoryLimit] for why the two are not the same number.
func jobLimitsFor(memoryLimit int64) jobLimits {
	limits := jobLimits{Flags: jobLimitKillOnClose}
	if value, set := kernelJobMemoryLimit(memoryLimit); set {
		limits.Flags |= jobLimitJobMemory
		limits.JobMemory = value
	}
	return limits
}

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
