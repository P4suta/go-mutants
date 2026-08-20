// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"errors"
	"slices"
	"strconv"
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
	// CodeInstrumentedBaselineFailed reports the semantic preservation gate:
	// the instrumented snapshot, with no mutant activated, no longer passes the
	// tests the pristine one passed a moment earlier. Every guard is supposed
	// to take the branch holding the original bytes when nothing is active, so
	// a red suite here is go-mutants having changed the program's meaning — and
	// every outcome measured afterwards would be a statement about that change
	// rather than about a mutant.
	CodeInstrumentedBaselineFailed Code = "GOM4013"
	// CodeWorkspaceDrift reports a snapshot that stopped matching its manifest
	// in a way instrumentation did not cause. Every worker shares one snapshot,
	// so a test that writes into its own package directory corrupts the tree
	// each later mutant is measured against; the run stops and names the files
	// rather than reporting outcomes nobody could reproduce.
	CodeWorkspaceDrift Code = "GOM4014"

	// CodeTimeoutTooSmall reports an explicit `test.timeout` that is not above
	// the slowest baseline run. Such a timeout would expire during ordinary
	// work, so every mutant would be reported as a timeout and the run would
	// measure nothing.
	CodeTimeoutTooSmall Code = "GOM4020"
	// CodeUnknownOperator reports a `mutation.operators` entry that names
	// neither an operator family nor a rule in the v1 catalogue. internal/config
	// refuses one wherever it was written, so it is unreachable from the command
	// line; it is reported rather than assumed away because a Config can also be
	// built in process, and an unknown name must never quietly select nothing.
	CodeUnknownOperator Code = "GOM4021"

	// CodeInterrupted reports a run stopped by a cancelled context, which in
	// practice means Ctrl-C or SIGTERM. It is an error so that the sequence
	// unwinds and the snapshot is cleaned up, and the command line maps it to
	// exit 130 or 143 rather than to the infrastructure code.
	CodeInterrupted Code = "GOM4030"
)

// Warning codes this package emits. They live in the same block as the errors
// because they are the same kind of promise to the user.
//
// GOM0001 is retired and is deliberately not redefined. It reported a run that
// ended after the baseline because the mutation phases were not implemented,
// and its own documentation said it would disappear, code and all, when they
// landed. They have. The number stays spent, because a code means one thing
// forever.
const (
	// CodeSnapshotNotRemoved reports a snapshot directory that survived
	// cleanup. It is a warning rather than an error: the run's results are
	// unaffected, and the remedy is to delete a directory in the temporary
	// area.
	CodeSnapshotNotRemoved Code = "GOM4040"
	// CodeScratchNotRemoved reports the same for the per-run scratch
	// directory.
	CodeScratchNotRemoved Code = "GOM4041"
	// CodeReportNotPublished reports a run whose results could not be written
	// to the history store. It is a warning rather than an error on the
	// interrupted path only: a partial run that could not be filed has still
	// told the user everything it learned on the console, and turning a failed
	// write into the reason the run failed would bury the interruption that
	// actually ended it.
	CodeReportNotPublished Code = "GOM4042"
	// CodeSelectedMutantRejected reports a `--mutant` prefix that resolved
	// against the catalogue but named a mutant compile validation had refused.
	//
	// Nothing is executed in that case, and every other silence is working as
	// designed: the catalogue is whole, so `policy.require_mutants` is satisfied;
	// the denominator is empty, so `minimum_score` cannot be missed; there are no
	// survivors, so `strict` has nothing to fail on. The run therefore exits 0
	// having measured nothing, which is exactly the shape
	// [mutation.Policy.RequireMutants] calls the most dangerous kind of green —
	// and the user who wrote the flag asked a direct question ("why did this one
	// survive?") that deserves a direct answer.
	//
	// It stays a warning rather than becoming an error because a rejection is
	// data and not a failure: the mutant does not compile once guarded, which is
	// a true and reportable fact about the catalogue, and it is the same fact a
	// whole-catalogue run states in `rejected[]` without failing. What was
	// missing was somebody saying it out loud when it is the only thing the run
	// had to say.
	CodeSelectedMutantRejected Code = "GOM4043"
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
	CodeInstrumentedBaselineFailed,
	CodeWorkspaceDrift,
	CodeTimeoutTooSmall,
	CodeUnknownOperator,
	CodeInterrupted,
	CodeSnapshotNotRemoved,
	CodeScratchNotRemoved,
	CodeReportNotPublished,
	CodeSelectedMutantRejected,
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

// A SelectionError reports a `--mutant` prefix that did not resolve to exactly
// one catalogued mutant: the wrong alphabet, nothing matching, or several
// matching.
//
// It is the one error this package raises that carries no GOM code, and the
// omission is deliberate rather than an oversight. The mistake is in how the
// run was invoked, not in the orchestration, and the catalogue it has to be
// checked against only exists half way through a run — so the engine reports
// the fact and internal/cli, which owns the invocation vocabulary, codes it in
// its own GOM10xx block. Giving it a GOM40xx code as well would mean two
// identifiers for one condition and a user searching for the wrong one.
//
// The internal/mutation sentinel stays reachable through errors.Is, so a caller
// can tell "no such mutant" from "several such mutants" without parsing text.
type SelectionError struct {
	// Prefix is the value the user wrote.
	Prefix string
	// Err is the underlying [mutation.ErrInvalidPrefix],
	// [mutation.ErrMutantNotFound], or [mutation.ErrAmbiguousPrefix], with the
	// catalogue's own explanation attached.
	Err error
}

// Error renders the prefix and what the catalogue said about it.
func (e *SelectionError) Error() string {
	return "--mutant " + strconv.Quote(e.Prefix) + " did not select one mutant: " + e.Err.Error()
}

// Unwrap returns the underlying cause.
func (e *SelectionError) Unwrap() error { return e.Err }

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
