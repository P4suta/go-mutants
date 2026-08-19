// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"errors"
	"fmt"
	"strings"
)

// A Code is a stable, user-facing diagnostic code. Codes are part of the
// command line interface: they appear in error output, they are searchable in
// documentation, and they are the thing a user quotes in a bug report. A code
// is therefore allocated once and never reused for a different meaning, even
// if the condition it named disappears.
//
// This package owns the GOM70xx range.
type Code string

// The snapshot diagnostic codes.
const (
	// CodeInvalidOptions reports [Options] that cannot be honoured, such as a
	// report directory that is absolute or climbs out of the source root.
	CodeInvalidOptions Code = "GOM7001"

	// CodeSourceRoot reports a source root that cannot be read or is not a
	// directory.
	CodeSourceRoot Code = "GOM7002"

	// CodeWalk reports an operating system failure while reading the tree:
	// a directory that cannot be listed, an entry that cannot be stat'ed.
	CodeWalk Code = "GOM7003"

	// CodeSymlink reports a symbolic link inside the source tree. Links are
	// refused rather than followed or skipped; see the package documentation
	// for why silence is the wrong answer here.
	CodeSymlink Code = "GOM7004"

	// CodeReparsePoint reports a Windows reparse point — a junction, a mount
	// point, or any other name surrogate — inside the source tree.
	CodeReparsePoint Code = "GOM7005"

	// CodeIrregular reports a file that is neither a directory nor a regular
	// file: a device, a socket, a named pipe.
	CodeIrregular Code = "GOM7006"

	// CodeUnsupportedName reports a file name that cannot survive the
	// round trip through a '/'-normalized relative path, such as a name
	// containing a backslash on a POSIX filesystem.
	CodeUnsupportedName Code = "GOM7007"

	// CodeDestination reports a failure to create the snapshot directory
	// itself.
	CodeDestination Code = "GOM7008"

	// CodeCopy reports a failure while copying the tree into the snapshot.
	CodeCopy Code = "GOM7009"

	// CodeCleanupRefused reports a [Snapshot.Cleanup] call that was refused
	// because the recorded root does not look like a directory this package
	// created. It is the guard that stands between a bug in go-mutants and a
	// user's source tree.
	CodeCleanupRefused Code = "GOM7010"

	// CodeCleanupFailed reports a snapshot directory that survived every
	// removal attempt, usually a file still locked by a test binary on
	// Windows.
	CodeCleanupFailed Code = "GOM7011"
)

// An Error is every error this package returns, so a caller can always reach
// the [Code] with errors.As and render it without matching on message text.
//
// The wrapped Err, when present, is the operating system error underneath.
// It stays reachable through errors.Is so a caller can still ask whether the
// real cause was, say, fs.ErrPermission.
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// Path is the path the error is about. It is a '/'-normalized path
	// relative to the tree being walked wherever one exists, because that is
	// the spelling the manifest, the report, and the user's exclude patterns
	// all use. It is an absolute path only when the error is about a root or
	// a destination, which have no relative spelling.
	Path string
	// Message states the problem in one clause, without the code and without
	// the path, so a caller that already shows those can print it alone.
	Message string
	// Err is the underlying cause, or nil when the condition is one this
	// package detected itself rather than one the OS reported.
	Err error
}

// Error implements the error interface.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	b.WriteString(": snapshot: ")
	b.WriteString(e.Message)
	if e.Path != "" {
		fmt.Fprintf(&b, ": %q", e.Path)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap returns the underlying cause, so errors.Is reaches through to the
// operating system error.
func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the [Code] carried by err, or the empty Code if err did not
// come from this package. It saves every caller the errors.As dance.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
