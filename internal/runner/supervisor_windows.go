// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// supervisorKind names the mechanism this platform supervises with.
const supervisorKind = "job-object"

// terminatedJobExitCode is the status TerminateJobObject stamps on every
// process in the job. It never reaches a caller — [Run] normalises the exit
// code of a tree it killed to [ExitCodeUnavailable] — so its only job is to be
// something other than zero for anyone reading Task Manager or an ETW trace.
const terminatedJobExitCode = 1

// ntResumeProcess resumes every thread of a process.
//
// It is the one undocumented call in go-mutants, and it is here because the
// documented alternative is worse in a way that matters at this tool's scale.
// The child is created suspended (see [jobSupervisor.configure]) and something
// has to start it once the job owns it. Windows offers no supported call that
// resumes a process; the supported route is to snapshot every thread on the
// machine with CreateToolhelp32Snapshot — TH32CS_SNAPTHREAD ignores its pid
// argument — filter the handful belonging to this child, and resume each. That
// is a system-wide walk per mutant, thousands of times per run, to restart a
// process that has exactly one thread.
//
// NtResumeProcess has been in ntdll since Windows NT and is what every process
// suspender on the platform uses. The entry point is resolved lazily, and a
// failure to find or call it is fail-closed like any other supervision failure:
// see [jobSupervisor.adopt].
var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// jobSupervisor owns a Windows Job Object holding one child process tree.
//
// The job is what makes tree ownership exact on Windows. Every process created
// by a process in a job joins that job, so the set is closed under descent —
// there is no equivalent of a POSIX descendant escaping by calling setsid.
type jobSupervisor struct {
	job windows.Handle
}

// newSupervisor creates and configures the job object, before the child
// exists. A machine that cannot create job objects is discovered while nothing
// is running.
func newSupervisor() (supervisor, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, &Error{
			Code:    CodeSupervisionUnavailable,
			Message: "could not create the Windows job object that owns the child process tree",
			Err:     err,
		}
	}

	// KILL_ON_JOB_CLOSE is the backstop. Even if go-mutants panics, is killed
	// itself, or forgets to terminate, the last handle to the job closing
	// takes the whole tree with it.
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	// SetInformationJobObject takes the struct as an untyped pointer plus a
	// length, so the uintptr conversion here is the documented calling
	// convention rather than pointer arithmetic; KeepAlive pins the value for
	// the duration of the call.
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	runtime.KeepAlive(&info)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, &Error{
			Code:    CodeSupervisionUnavailable,
			Message: "could not set kill-on-close on the Windows job object that owns the child process tree",
			Err:     err,
		}
	}

	return &jobSupervisor{job: job}, nil
}

// configure has the child created suspended, which is what closes the
// assign-after-start window.
//
// Windows gives os/exec no way to create a process already inside a job.
// CREATE_SUSPENDED is the next best thing and it is exact for this purpose:
// the process exists, so it can be assigned, but not one instruction of it has
// run, so it cannot yet have forked a grandchild that would land outside the
// job.
//
// Without it the window is not a formality. Measured on a loaded machine —
// sixteen concurrent runs, which is what an eight-core mutation run looks like
// — the gap between CreateProcess and AssignProcessToJobObject ranged from two
// to fifty milliseconds, with outliers past a hundred and eighty. Cmd.Start
// does real work after the process exists (closing descriptors, launching the
// output copier) and the Go scheduler is free to park the goroutine in the
// middle of it. A Go test binary boots and can fork inside that gap, and a
// grandchild that lands outside the job survives the kill: this package's
// central promise, silently broken, on exactly the loaded machine where it
// matters most.
//
// The cost is that something must resume the child once the job owns it, and
// that a bug in the resume path hangs the child forever instead of leaking a
// descendant. [jobSupervisor.adopt] treats a failed resume as fatal for that
// reason.
func (s *jobSupervisor) configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
}

// adopt assigns the suspended child to the job and then lets it run.
//
// Both halves are fail-closed, for different reasons. A child that cannot be
// assigned is a child this package cannot promise to kill, and go-mutants does
// not run test binaries it cannot kill. A child that cannot be resumed is
// worse than that: it is a process frozen before its first instruction, which
// would hang the run rather than fail it, so the caller kills it and reports
// the failure instead.
//
// Opening by pid is safe despite pid reuse: os/exec still holds an open handle
// to the child, and Windows does not recycle a pid while any handle to the
// process remains.
func (s *jobSupervisor) adopt(cmd *exec.Cmd) error {
	handle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return &Error{
			Code:    CodeSupervisionUnavailable,
			Message: "could not open the child process to assign it to a job object",
			Err:     err,
		}
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	if err := windows.AssignProcessToJobObject(s.job, handle); err != nil {
		return &Error{
			Code:    CodeSupervisionUnavailable,
			Message: "could not assign the child process to its job object",
			Err:     err,
		}
	}

	if err := resume(handle); err != nil {
		return &Error{
			Code:    CodeSupervisionUnavailable,
			Message: "could not resume the child process after assigning it to its job object",
			Err:     err,
		}
	}

	supervisionAdopted.Add(1)
	return nil
}

// resume starts a process created suspended.
func resume(process windows.Handle) error {
	if err := ntResumeProcess.Find(); err != nil {
		return err
	}
	r1, _, _ := ntResumeProcess.Call(uintptr(process))

	// NtResumeProcess answers with an NTSTATUS rather than setting the last
	// error, so the return value is the whole answer and GetLastError says
	// nothing about it.
	//
	// Failure is NT_SUCCESS's own test — the sign of the status read as a
	// signed 32-bit value — rather than "non-zero". The success and
	// informational severities both live in the non-negative half, so a status
	// like STATUS_PENDING is not a failure, and this branch kills the child:
	// reading a benign status as an error would turn a mutant whose test
	// binary started perfectly well into a reported infrastructure failure.
	status := windows.NTStatus(uint32(r1))
	if int32(status) < 0 {
		return status
	}
	return nil
}

// terminate ends the whole job at once. There is no graceful phase: Windows
// has no signal a console test binary is obliged to handle, and inventing one
// out of CTRL_BREAK would deliver it to a group this package does not own.
func (s *jobSupervisor) terminate(<-chan struct{}) {
	supervisionTerminated.Add(1)
	_ = windows.TerminateJobObject(s.job, terminatedJobExitCode)
}

// release closes the job handle, which — because of KILL_ON_JOB_CLOSE — also
// kills anything still inside it. That is why a normal exit needs no separate
// sweep for stragglers on this platform.
func (s *jobSupervisor) release() {
	_ = windows.CloseHandle(s.job)
}

// exitCodeOf reads the child's status. Windows has no signal deaths, so the
// exit code is the whole story.
func exitCodeOf(ps *os.ProcessState) int {
	if ps == nil {
		return ExitCodeUnavailable
	}
	return ps.ExitCode()
}
