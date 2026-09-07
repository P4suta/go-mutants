// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The two kernel32 entry points a job object's limits are written and read
// through.
//
// They are bound here rather than reached through golang.org/x/sys/windows,
// and the reason is a rule of the Go compiler rather than a preference.
// SetInformationJobObject and QueryInformationJobObject take the structure as
// an untyped address plus a length, and x/sys spells that parameter `uintptr`
// — so calling those wrappers means writing `uintptr(unsafe.Pointer(&info))`
// in the argument list of an ordinary Go function, and that is precisely where
// the conversion means nothing.
//
// It is safe in the argument list of the assembly call and nowhere else.
// syscall.SyscallN carries `//go:uintptrkeepalive` and `//go:nosplit` for
// exactly this, and the runtime's own note beside those pragmas says why:
// "stack copying does not account for uintptrkeepalive, so the stack must not
// grow. Stack copying cannot blindly assume that all uintptr arguments are
// pointers, because some values may look like pointers, but not really be
// pointers, and adjusting their value would break the call."
//
// One Go frame earlier nothing holds. `info` is a local; escape analysis leaves
// it on the stack, because a uintptr is not a pointer; and the x/sys wrapper's
// own prologue is a stack-growth point — it opens with `CMPQ SP, 0x10(R14)` and
// then calls LazyProc.Find. A goroutine that grows there has its frames copied
// and its old stack span returned to the pool, with the uintptr still naming
// the old address. Another goroutine takes the span and writes its own frames
// into it, and the kernel reads whatever landed at that offset: LimitFlags
// naming a limit the structure does not carry is ERROR_INVALID_PARAMETER.
// runtime.KeepAlive cannot repair it, and its presence at those call sites was
// the misreading — it keeps a value from being *collected*, and a stack frame
// is not collected, it is moved.
//
// That is a defect only a concurrent run can show, because a freed stack span
// is only overwritten while something else is running, and it showed once:
// windows-latest, 2026-09-06, `GOM7201: could not set kill-on-close on the
// Windows job object that owns the child process tree: The parameter is
// incorrect.` under the eight concurrent Workspace.Exec calls of
// TestConcurrentExecutionsUnderKeepTempRecordAndPreserveEveryScratch, green on
// every other Windows run before and since.
//
// internal/runner/unsafeptr_test.go is the rule as a gate, over the whole
// module and on every platform, because `go vet` has no analyzer for this
// direction of the conversion.
var (
	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	procSetInformationJobObject   = kernel32.NewProc("SetInformationJobObject")
	procQueryInformationJobObject = kernel32.NewProc("QueryInformationJobObject")
)

// setJobLimits configures the job object with limits.
//
// The whole structure is written on every call — that is what the API does —
// so limits carries every flag the job is meant to keep and not only the ones
// this call is about. [jobLimitsFor] is where that set is decided.
//
// A failure names what the kernel was handed. ERROR_INVALID_PARAMETER says only
// that the structure was refused, and the three things worth knowing next are
// which flags were in it, which value stood beside them, and how many bytes the
// kernel was told to read; a message without them leaves the next occurrence as
// undiagnosable as the first.
func setJobLimits(job windows.Handle, limits jobLimits) error {
	if err := procSetInformationJobObject.Find(); err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: limits.Flags,
		},
		JobMemoryLimit: limits.JobMemory,
	}
	r1, _, errno := syscall.SyscallN(
		procSetInformationJobObject.Addr(),
		uintptr(job),
		uintptr(windows.JobObjectExtendedLimitInformation),
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if r1 == 0 {
		return fmt.Errorf(
			"SetInformationJobObject(JobObjectExtendedLimitInformation, LimitFlags=%#x, "+
				"JobMemoryLimit=%d, %d bytes): %w",
			limits.Flags, limits.JobMemory, unsafe.Sizeof(info), jobObjectErrno(errno))
	}
	return nil
}

// jobMemoryInfo reads the job's extended limit information, which is where
// Windows keeps both what the job is allowed and what it has used.
//
// One query answers every question this package asks about a job's memory: the
// peak for [Result.PeakMemory], the same peak as the sample a bound is checked
// against, and the limits themselves for the test that proves the kernel was
// told.
//
// The returned length is not asked for. The structure has a fixed size and the
// call is refused outright if the buffer is not that size, so a length that
// came back is a length already known.
func jobMemoryInfo(job windows.Handle) (windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION, error) {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := procQueryInformationJobObject.Find(); err != nil {
		return info, err
	}
	r1, _, errno := syscall.SyscallN(
		procQueryInformationJobObject.Addr(),
		uintptr(job),
		uintptr(windows.JobObjectExtendedLimitInformation),
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
		0,
	)
	if r1 == 0 {
		return info, fmt.Errorf("QueryInformationJobObject(JobObjectExtendedLimitInformation, %d bytes): %w",
			unsafe.Sizeof(info), jobObjectErrno(errno))
	}
	return info, nil
}

// jobObjectErrno is the error a failed call carries.
//
// A zero errno beside a failed call means the thread's last error says nothing
// about it, and reporting "the operation completed successfully" for a call
// that did not is the least useful thing this could do. x/sys' generated
// wrappers make the same substitution for the same reason.
func jobObjectErrno(errno syscall.Errno) error {
	if errno == 0 {
		return syscall.EINVAL
	}
	return errno
}
