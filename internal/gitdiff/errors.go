// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitdiff

import (
	"errors"
	"slices"
	"strings"
)

// A Code is a stable, user-facing diagnostic code.
//
// This package owns GOM7710 upwards, inside the GOM77xx block internal/tui also
// draws from — the dashboard holds GOM7701 and nothing else. The two are
// neighbours rather than one block split in half: a code is a user-facing
// identity, so what matters is that a number means one thing forever, and the
// tens digit is what keeps the two allocations from ever meeting.
type Code string

// The git codes.
const (
	// CodeGitUnavailable reports a git that could not be run at all: not on
	// PATH, not executable, or killed before it answered. It is separate from
	// [CodeNotARepository] because the remedy is different — install git, or
	// run somewhere else — and because a machine with no git is a machine where
	// `--changed` can never work rather than one where this directory is wrong.
	CodeGitUnavailable Code = "GOM7710"
	// CodeNotARepository reports a workspace that is not inside a git working
	// tree. `--changed` is a question about version control history, and there
	// is none.
	CodeNotARepository Code = "GOM7711"
	// CodeNoUpstream reports a bare `--changed` in a repository whose HEAD has
	// no upstream branch: a detached HEAD, or a branch that has never been
	// pushed or tracked. The remedy is to name the ref, so the message does.
	CodeNoUpstream Code = "GOM7712"
	// CodeUnknownRef reports a ref that could not be resolved, or that shares no
	// history with HEAD, or a repository with no commits at all. All three are
	// the same failure to a user — there is no base to diff against — and the
	// message says which one it was.
	CodeUnknownRef Code = "GOM7713"
	// CodeDiffFailed reports a `git diff` that would not run or exited
	// non-zero.
	CodeDiffFailed Code = "GOM7714"
	// CodeMalformedDiff reports diff output this package could not read. It is
	// always a bug — in this parser, or in an assumption about git's output
	// format — and it is reported rather than skipped, because a hunk header
	// silently ignored is a set of changed lines silently missing from the
	// selection, which shows up as an inexplicably small run.
	CodeMalformedDiff Code = "GOM7715"
	// CodeUntrackedUnreadable reports that the untracked files could not be
	// established: the `git ls-files` that names them would not run, or one of
	// the files it named could not be read. It is separate from
	// [CodeDiffFailed] because the diff itself succeeded — what failed is the
	// half of the changed set a diff cannot see — and because a user reading it
	// should look at their working tree rather than at their history.
	CodeUntrackedUnreadable Code = "GOM7716"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the range this
// package owns.
var codes = []Code{
	CodeGitUnavailable,
	CodeNotARepository,
	CodeNoUpstream,
	CodeUnknownRef,
	CodeDiffFailed,
	CodeMalformedDiff,
	CodeUntrackedUnreadable,
}

// Codes returns every diagnostic code this package can report, in numeric
// order, so that `doctor` can print the table without reading the source.
func Codes() []Code { return slices.Clone(codes) }

// An Error is one git failure carrying a stable [Code].
//
// It mirrors the shape internal/engine and internal/report use — code, one-line
// message, optional child output, optional cause — so that internal/cli lays
// them all out the same way without the packages sharing an error identity.
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// Message states the problem in one line, without the code and without the
	// output.
	Message string
	// Output is what git said on the failure path, trimmed. It is kept apart
	// from the message because it is the only part of the error that is not
	// ours to word.
	Output string
	// Err is the underlying cause, or nil.
	Err error
}

// Error renders "GOM7713: <message>", with the cause appended when there is
// one. The output is deliberately not part of it.
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

// OutputOf returns git's own words carried by err, or "" when there are none.
func OutputOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Output
	}
	return ""
}
