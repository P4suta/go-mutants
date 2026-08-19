// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"errors"
	"slices"
	"strings"
)

// A Code is a stable, user-facing diagnostic code.
//
// Codes are part of the command line interface: they appear in error output, in
// CI logs, and in bug reports, and they are what somebody searches for when the
// message is not enough. A code therefore means exactly one thing forever, and
// a retired code is never reused.
//
// This package owns the GOM40xx block. It does not re-code the failures of the
// packages it drives: a snapshot, toolchain, or process failure is returned
// with the GOM70xx or GOM72xx code its own package gave it, because wrapping it
// in a second code would mean two identifiers for one condition and a user
// searching for the wrong one.
type Code string

// The orchestration codes.
const (
	// CodeWorkspaceRoot reports a workspace root that is empty or cannot be
	// resolved against the current working directory.
	CodeWorkspaceRoot Code = "GOM4001"

	// GOM4002 is retired and is deliberately not redefined. It reported an
	// exclude pattern that did not compile, from when this package compiled
	// `mutation.exclude` for the snapshot walk — which was itself the bug: an
	// exclude says what is worth mutating and has no business shrinking the
	// tree that gets built and tested. Compiling those patterns belongs to
	// discovery, which allocates a code for it in its own block. The number
	// stays spent, because a code means one thing forever.

	// CodeTestCommand reports an empty test command, or one whose program name
	// is blank. It is a configuration mistake that only becomes visible when
	// something tries to run it.
	CodeTestCommand Code = "GOM4003"
	// CodeScratchDir reports a per-run scratch directory that could not be
	// created. The run stops rather than letting children write into the
	// user's temporary directory, which is the thing the scratch directory
	// exists to prevent.
	CodeScratchDir Code = "GOM4004"

	// CodeBaselineBuildFailed reports a snapshot that does not compile. It is
	// always a fact about the workspace, never about a mutant: no source has
	// been rewritten at this point.
	CodeBaselineBuildFailed Code = "GOM4010"
	// CodeBaselineTestFailed reports an unmutated test run that failed. Every
	// mutant would be reported as killed by a suite that is already red, so
	// the run stops instead of producing a flattering score.
	CodeBaselineTestFailed Code = "GOM4011"
	// CodeBaselineTimedOut reports a baseline command that did not finish
	// inside [BaselineCap]. The cap is generous and fixed, because there is no
	// measurement to derive one from yet.
	CodeBaselineTimedOut Code = "GOM4012"

	// CodeTimeoutTooSmall reports an explicit `test.timeout` that is not above
	// the slowest baseline run. Such a timeout would expire during ordinary
	// work, so every mutant would be reported as a timeout and the run would
	// measure nothing.
	CodeTimeoutTooSmall Code = "GOM4020"

	// CodeInterrupted reports a run stopped by a cancelled context, which in
	// practice means Ctrl-C or SIGTERM. It is an error so that the sequence
	// unwinds and the snapshot is cleaned up, and the command line maps it to
	// exit 130 or 143 rather than to the infrastructure code.
	CodeInterrupted Code = "GOM4030"
)

// Warning codes this package emits. They live in the same block as the errors
// because they are the same kind of promise to the user.
const (
	// CodeMutationPhasesPending reports that a run ended after the baseline
	// because the mutation phases are not implemented yet.
	//
	// It is deliberately GOM0001 rather than a GOM40xx code: it is not a
	// property of the orchestration, it is a property of this pre-release
	// build, and it disappears — code and all — when the mutation phases land.
	// Giving it a number inside an allocated block would leave a permanent
	// hole there for a condition that is meant to be temporary.
	CodeMutationPhasesPending = "GOM0001"

	// CodeSnapshotNotRemoved reports a snapshot directory that survived
	// cleanup. It is a warning rather than an error: the run's results are
	// unaffected, and the remedy is to delete a directory in the temporary
	// area.
	CodeSnapshotNotRemoved Code = "GOM4040"
	// CodeScratchNotRemoved reports the same for the per-run scratch
	// directory.
	CodeScratchNotRemoved Code = "GOM4041"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the block this
// package owns, with [CodeMutationPhasesPending] as the documented exception.
var codes = []Code{
	CodeWorkspaceRoot,
	CodeTestCommand,
	CodeScratchDir,
	CodeBaselineBuildFailed,
	CodeBaselineTestFailed,
	CodeBaselineTimedOut,
	CodeTimeoutTooSmall,
	CodeInterrupted,
	CodeSnapshotNotRemoved,
	CodeScratchNotRemoved,
}

// Codes returns every diagnostic code this package can report, in numeric
// order, so that `doctor` can print the table without reading the source.
func Codes() []Code { return slices.Clone(codes) }

// OutputTailLines is how many trailing lines of a failed command's output an
// [Error] keeps.
//
// internal/runner retains a megabyte, which is right for a report and wrong for
// a terminal: the useful part of a failed `go test` is the last few assertions,
// and a megabyte of scrollback buries them. Fifty lines is enough for a stack
// trace plus the failure that caused it.
const OutputTailLines = 50

// An Error is one orchestration failure carrying a stable [Code].
//
// The child process output, when there was one, is kept in [Error.Output]
// rather than folded into the message. A failure line has to stay one line —
// that is what makes it greppable and what lets the command line prefix it —
// while the output underneath it is many, and the two are printed differently.
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// Message states the problem in one line, without the code and without the
	// output.
	Message string
	// Output is the tail of the failed command's combined output, already
	// trimmed to [OutputTailLines] and with trailing carriage returns removed.
	// It is empty when the failure had no child process behind it.
	Output string
	// Err is the underlying cause, or nil. It stays reachable through
	// errors.Is, which is how the command line recognises a cancellation.
	Err error
}

// Error renders "GOM4011: <message>", with the cause appended when there is
// one. The output tail is deliberately not part of it.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the [Code] carried by err, or the empty Code if err did not
// come from this package.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// OutputOf returns the retained command output carried by err, or "" when
// there is none. It saves the renderer an errors.As dance for the one field it
// has to lay out differently from the message.
func OutputOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Output
	}
	return ""
}

// tail trims a child's combined output down to what is worth printing: the
// last [OutputTailLines] lines, without trailing blank lines and without the
// carriage returns a Windows child leaves behind.
//
// Carriage returns are stripped here rather than left for the renderer because
// a terminal is not the only consumer: the same string goes into a report,
// where a stray CR is invisible in the diff that finally notices it.
func tail(output []byte) string {
	text := strings.TrimRight(string(output), "\r\n \t")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > OutputTailLines {
		lines = lines[len(lines)-OutputTailLines:]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}
