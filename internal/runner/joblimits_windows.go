// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	procSetInformationJobObject   = kernel32.NewProc("SetInformationJobObject")
	procQueryInformationJobObject = kernel32.NewProc("QueryInformationJobObject")
)

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

func jobObjectErrno(errno syscall.Errno) error {
	if errno == 0 {
		return syscall.EINVAL
	}
	return errno
}
