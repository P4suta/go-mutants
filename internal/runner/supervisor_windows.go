// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const supervisorKind = "job-object"

const terminatedJobExitCode = 1

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

type jobSupervisor struct {
	job windows.Handle
}

func newSupervisor(memoryLimit int64) (supervisor, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, &Error{
			Code:    CodeSupervisionUnavailable,
			Message: "could not create the Windows job object that owns the child process tree",
			Err:     err,
		}
	}

	if err := setJobLimits(job, jobLimitsFor(0)); err != nil {
		_ = windows.CloseHandle(job)
		return nil, &Error{
			Code:    CodeSupervisionUnavailable,
			Message: "could not set kill-on-close on the Windows job object that owns the child process tree",
			Err:     err,
		}
	}

	if err := limitJobMemory(job, memoryLimit); err != nil {
		_ = windows.CloseHandle(job)
		return nil, &Error{
			Code:    CodeSupervisionUnavailable,
			Message: "could not set the memory limit on the Windows job object that owns the child process tree",
			Err:     err,
		}
	}

	return &jobSupervisor{job: job}, nil
}

func (s *jobSupervisor) configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
}

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

func resume(process windows.Handle) error {
	if err := ntResumeProcess.Find(); err != nil {
		return err
	}
	r1, _, _ := ntResumeProcess.Call(uintptr(process))

	status := windows.NTStatus(uint32(r1))
	if int32(status) < 0 {
		return status
	}
	return nil
}

func (s *jobSupervisor) terminate(<-chan struct{}, time.Duration) {
	supervisionTerminated.Add(1)
	_ = windows.TerminateJobObject(s.job, terminatedJobExitCode)
}

func (s *jobSupervisor) usedMemory(bool) (int64, bool) { return jobPeakMemory(s.job) }

func (s *jobSupervisor) peakMemory(*os.ProcessState) (int64, bool) { return jobPeakMemory(s.job) }

func (s *jobSupervisor) release() {
	_ = windows.CloseHandle(s.job)
}

func exitCodeOf(ps *os.ProcessState) int {
	if ps == nil {
		return ExitCodeUnavailable
	}
	return ps.ExitCode()
}
