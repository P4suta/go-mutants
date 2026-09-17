// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package cli

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type (
	fileAttributeTagInfo struct {
		FileAttributes uint32
		ReparseTag     uint32
	}
	fileDispositionInfo struct {
		DeleteFile bool
	}
)

func removeDirectory(path string) error {
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		wide,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	var info fileAttributeTagInfo
	if err = windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return err
	}
	switch {
	case info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0:
		return errIsALink
	case info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0:
		return errNotDirectory
	}

	disposition := fileDispositionInfo{DeleteFile: true}
	return windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo,
		(*byte)(unsafe.Pointer(&disposition)), uint32(unsafe.Sizeof(disposition)))
}
