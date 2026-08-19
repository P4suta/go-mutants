// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import "errors"

// Stable error codes for this package.
//
// Codes are part of the user-facing contract: they appear in console output,
// in reports, and in bug reports, and they are searchable in the docs. The
// GOM72xx block belongs to the process layer, and each code is defined exactly
// once, here.
const (
	// CodeSupervisionUnavailable reports that the process tree could not be
	// placed under supervision. On Windows that is a job object that could not
	// be created, configured, or assigned. It is always fatal to the run that
	// asked for it: go-mutants does not execute a test binary it cannot
	// guarantee it can kill.
	CodeSupervisionUnavailable = "GOM7201"
	// CodeProcessStartFailed reports that the child process could not be
	// started at all — the executable is missing, is not executable, or the
	// working directory does not exist. It never means the child ran and
	// failed.
	CodeProcessStartFailed = "GOM7202"
	// CodeSpecInvalid reports a [Spec] that cannot describe a process, which
	// in practice means an empty or blank argument vector. It is a programming
	// error in the caller rather than a condition of the machine.
	CodeSpecInvalid = "GOM7203"
	// CodeProcessWaitFailed reports that the child was started and supervised
	// but the operating system then refused to say how it ended. This is not a
	// non-zero exit — that is ordinary data, reported through
	// [Result.ExitCode] — it is the wait itself failing, which leaves the
	// result untrustworthy.
	CodeProcessWaitFailed = "GOM7204"
)

// Error is a runner failure carrying a stable GOM#### code.
//
// The type is defined here rather than shared with the other GOM72xx package,
// internal/gocmd, on purpose: a package's error contract should not be
// something a caller has to import a second package to name, and the two
// packages are free to diverge without one of them being wrong.
type Error struct {
	// Code is the stable GOM#### identifier.
	Code string
	// Message is a one-line explanation with no timings, pids, or absolute
	// paths of our own invention, so that two runs of the same failure render
	// the same text.
	Message string
	// Err is the underlying cause, if any. It is unwrapped, so errors.Is and
	// errors.As reach syscall errors and os/exec sentinels through it.
	Err error
}

// Error renders the code, the message, and the cause.
func (e *Error) Error() string {
	if e.Err == nil {
		return e.Code + ": " + e.Message
	}
	return e.Code + ": " + e.Message + ": " + e.Err.Error()
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the GOM#### code carried by err, or "" if err is not a runner
// error. It saves every caller the errors.As dance when all it wants is to
// print or classify the code.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
