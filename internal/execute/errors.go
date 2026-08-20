// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"errors"
	"slices"
	"strings"
)

// A Code is a stable, user-facing diagnostic code.
//
// This package owns the GOM75xx block. It does not re-code the failures of the
// packages it drives: a process that could not be supervised comes back with
// internal/runner's GOM72xx code inside [Error.Err], because one condition
// deserves one identifier and a user searching for the wrong one finds nothing.
// The codes here name conditions this package is the first to notice.
type Code string

// The build codes.
const (
	// CodeOptions reports [Options] that cannot be executed against: no
	// toolchain, no snapshot root, or no binary directory. It is a programming
	// error in the caller rather than anything the user did.
	CodeOptions Code = "GOM7501"
	// CodeBinDir reports a binary directory that could not be created. The
	// build stops rather than scattering test binaries into whatever directory
	// happened to be current.
	CodeBinDir Code = "GOM7502"
	// CodeListFailed reports a `go list` over the snapshot that failed. The
	// snapshot has already passed a baseline build by the time this runs, so a
	// listing failure is infrastructure trouble rather than a fact about the
	// user's code.
	CodeListFailed Code = "GOM7503"
	// CodeListUnreadable reports `go list -json` output this package could not
	// decode. Same category as [CodeListFailed]: the toolchain answered with
	// something that is not the documented format.
	CodeListUnreadable Code = "GOM7504"
	// CodeTestBuildFailed reports a package whose test binary would not
	// compile. The instrumented tree has already been proved to build and to
	// pass its tests, so this means the instrumented rewrite broke a _test.go
	// file's view of the package — which is a go-mutants bug, and is reported
	// with the compiler's own output attached.
	CodeTestBuildFailed Code = "GOM7505"
)

// The scheduling codes.
const (
	// CodeNoTestBinaries reports an attempt to measure mutants against an empty
	// set of test binaries. Every mutant would be reported as survived, which
	// is a flattering green produced by having run nothing at all, so the run
	// refuses instead.
	CodeNoTestBinaries Code = "GOM7510"
	// CodeMutantInvalid reports a [MutantRun] that cannot be executed: no
	// activation identity, or no timeout. A mutant with no timeout is refused
	// rather than run unbounded — the one thing worse than a mutant reported
	// wrongly is a run that never ends.
	CodeMutantInvalid Code = "GOM7511"
	// CodeScratchDir reports a per-worker temporary directory that could not be
	// created, or a [Options.ScratchDir] that could not be resolved against the
	// working directory. Without one two mutants running at once would share the
	// user's temporary directory, and an unresolved one would point a child's
	// TMPDIR at a path inside the snapshot — so the attempt fails rather than
	// proceeding without the isolation it promised.
	CodeScratchDir Code = "GOM7512"
	// CodeMutantStart reports a test binary that could not be started or
	// supervised for one mutant. It is never a statement about the tests; the
	// mutant's outcome is errored, and the underlying GOM72xx cause stays
	// reachable through [Error.Err].
	CodeMutantStart Code = "GOM7513"
	// CodeStaleCatalog reports a test binary that exited with
	// [instrument.UnknownMutantExit]: the generated runtime was handed an
	// activation identity it does not know. The catalogue and the instrumented
	// tree have drifted apart, and every outcome measured from here on would be
	// a fiction.
	CodeStaleCatalog Code = "GOM7514"
	// CodeInterrupted reports a schedule stopped by a cancelled context, which
	// in practice means Ctrl-C or SIGTERM. The results measured so far are
	// returned alongside it, with everything that never ran marked
	// [mutation.OutcomeNotRun].
	CodeInterrupted Code = "GOM7520"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the GOM75xx
// block.
var codes = []Code{
	CodeOptions,
	CodeBinDir,
	CodeListFailed,
	CodeListUnreadable,
	CodeTestBuildFailed,
	CodeNoTestBinaries,
	CodeMutantInvalid,
	CodeScratchDir,
	CodeMutantStart,
	CodeStaleCatalog,
	CodeInterrupted,
}

// Codes returns every diagnostic code this package can report, in numeric
// order, so that `doctor` can print the table without reading the source.
func Codes() []Code { return slices.Clone(codes) }

// OutputTailLines is how many trailing lines of a failed command's output an
// [Error] keeps.
//
// internal/runner retains a megabyte, which is right for a report and wrong for
// a terminal: the useful part of a failed compile or a failed test is the last
// few diagnostics, and a megabyte of scrollback buries them.
const OutputTailLines = 50

// An Error is one execution failure carrying a stable [Code].
//
// It mirrors the shape internal/engine, internal/discover and
// internal/instrument use — code, one-line message, optional output, optional
// cause — so a single renderer can lay them all out the same way without the
// packages sharing an error identity.
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
	// errors.Is, which is how a caller recognises a cancellation.
	Err error
}

// Error renders "GOM7505: <message>", with the cause appended when there is
// one. The output tail is deliberately not part of it, because a failure line
// has to stay one line.
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
// there is none.
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
