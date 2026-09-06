// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package cli

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// The two FILE_INFO_BY_HANDLE_CLASS payloads this file needs.
//
// golang.org/x/sys/windows exports the class numbers and not the structures, so
// the two layouts are written out here. Both are fixed by the Win32 ABI, and
// both are what the standard library declares for itself in
// internal/syscall/windows: two DWORDs, and one BOOLEAN — which is a single
// byte, exactly as Go's bool is.
type (
	fileAttributeTagInfo struct {
		FileAttributes uint32
		ReparseTag     uint32
	}
	fileDispositionInfo struct {
		DeleteFile bool
	}
)

// removeDirectory removes an empty directory and can remove nothing else. See
// the other platform's copy for why the collector may not use [os.Remove] here.
//
// Windows needs more than rmdir's spelling to keep that promise.
// RemoveDirectory does refuse a file, but it removes a directory symbolic link
// or a junction *itself* rather than refusing it — so a root replaced by a link
// between the listing and here would have the replacement deleted, which is the
// same hole one indirection further along.
//
// So the removal goes through a handle. The path is opened with
// FILE_FLAG_OPEN_REPARSE_POINT, which is what makes the handle refer to the
// link rather than to whatever it points at; that handle is asked what it is;
// and the delete is set on the same handle, so the object examined and the
// object removed are one object rather than one path visited twice. Anything
// wearing FILE_ATTRIBUTE_REPARSE_POINT is refused, which covers a junction and
// every other reparse tag as well as a symbolic link — the check is on the
// attribute, so a tag nobody here has heard of is refused too.
//
// FILE_FLAG_BACKUP_SEMANTICS is what allows a directory to be opened at all.
// The share mode is everything, because refusing to open a root another process
// happens to be reading would turn housekeeping into a failure; DELETE is what
// the disposition below needs, and FILE_READ_ATTRIBUTES is what the query does.
//
// Every refusal that is neither of the two kinds — most often
// ERROR_DIR_NOT_EMPTY for a directory that still holds something, and the
// not-found of a root that was never there — is returned as it was, for the
// caller to pass over in silence.
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
