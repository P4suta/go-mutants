// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"errors"
	"slices"
	"strings"
)

// A Code is a stable, user-facing diagnostic code.
//
// This package owns the GOM74xx block. Its codes divide into two kinds, and the
// division is the point of the phase: a candidate that will not compile is not
// an error at all — it is a [Rejection], data the run reports and carries on
// from — while a code below means go-mutants could not do the isolating.
type Code string

// The validation codes.
const (
	// CodeOptions reports [Options] that cannot be validated against: no
	// snapshot, no catalogue, no module path, or no located toolchain.
	CodeOptions Code = "GOM7401"
	// CodeSourceUnreadable reports a catalogued file whose pristine bytes could
	// not be read out of the snapshot before instrumentation rewrote it. The
	// bytes are read first and kept for the whole phase, because every rebuild
	// during the search composes its guards against them.
	CodeSourceUnreadable Code = "GOM7402"

	// CodeBuildFailed reports a `go build` that could not be run at all — no
	// toolchain, no permission, a process that could not be supervised. It is
	// never a compile error, which is this phase's ordinary input rather than a
	// failure.
	CodeBuildFailed Code = "GOM7410"
	// CodeBuildTimedOut reports a build that did not answer within
	// [Options.BuildTimeout].
	CodeBuildTimedOut Code = "GOM7411"
	// CodeInterrupted reports validation cancelled through its context.
	CodeInterrupted Code = "GOM7412"

	// CodeNotMutantInduced reports a snapshot that does not build with every
	// guard removed. Nothing this phase can reject would fix it, so isolating
	// is refused rather than attempted: bisecting a tree that was already broken
	// would blame the first candidate it happened to test and reject a whole
	// file's mutants for somebody else's compile error.
	CodeNotMutantInduced Code = "GOM7420"
	// CodeStillFailing reports a snapshot that does not build although every
	// catalogued file has been isolated and each accepted subset compiled on its
	// own. That is candidates in different files interacting, which the search
	// assumes away, so it is reported rather than papered over.
	CodeStillFailing Code = "GOM7421"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the GOM74xx block.
var codes = []Code{
	CodeOptions,
	CodeSourceUnreadable,
	CodeBuildFailed,
	CodeBuildTimedOut,
	CodeInterrupted,
	CodeNotMutantInduced,
	CodeStillFailing,
}

// Codes returns every diagnostic code this package can report, in numeric
// order, so that `doctor` can print the table without reading the source.
func Codes() []Code { return slices.Clone(codes) }

// An Error is one validation failure carrying a stable [Code].
//
// It mirrors the shape the neighbouring packages use — code, one-line message,
// optional output, optional cause — so that a single renderer can lay them all
// out the same way without the packages sharing an error identity.
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// Message states the problem in one line, without the code.
	Message string
	// Output is the compiler output that goes with the failure, when there is
	// one. It is what makes [CodeNotMutantInduced] actionable: the user needs
	// the compiler's own words, not the news that a build failed.
	Output string
	// Err is the underlying cause, or nil. It stays reachable through
	// errors.Is and errors.As.
	Err error
}

// Error renders "GOM7420: <message>", with the compiler output on following
// lines and the cause appended when there is one.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	if e.Output != "" {
		b.WriteString("\n")
		b.WriteString(e.Output)
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
