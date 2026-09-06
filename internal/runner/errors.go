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

	// Invocation is the command the failure was about. [Run] sets it on every
	// error it returns, the refused spec included; it is nil on an error built
	// anywhere else, because a failure that named no command should say so
	// rather than name an invented one.
	//
	// It is not part of [Error.Error], and that is deliberate: the message is a
	// stable one-liner that two runs of the same failure render identically,
	// while a command carries absolute paths and a temporary directory. The
	// renderer asks for it separately and prints it under the message.
	Invocation *Invocation

	// Output is what the child had printed by the time the failure was noticed,
	// as this package retained it. [Run] fills it in from the same capture
	// [Result.Output] carries; it is empty on the failures that never got a
	// child as far as writing anything — a refused spec, a process that could
	// not be started — because a command that never ran said nothing.
	//
	// It is kept out of [Error.Error] for the reason the invocation is: however
	// many lines some other program decided to print is not part of a stable
	// one-line message. The renderer asks for it separately and prints it
	// underneath.
	Output string
}

// RetainedOutput returns what the failing command printed, or an empty string
// when it printed nothing.
//
// It is the second half of the pair every go-mutants error answers — the other
// is [Error.Command] — so that one renderer can ask any of them for its output
// through a one-method interface, without importing the package that produced
// it or knowing how many such packages there are.
func (e *Error) RetainedOutput() string { return e.Output }

// Command returns the command this failure was about, or nil when there is
// none.
//
// It is an accessor rather than a bare field so that a renderer can ask any
// error for its command through a one-method interface, without importing the
// package that produced it or knowing how many such packages there are.
func (e *Error) Command() *Invocation { return e.Invocation }

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
