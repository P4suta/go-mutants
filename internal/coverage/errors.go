// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage

import (
	"errors"
	"slices"
	"strings"
)

// A Code is a stable, user-facing diagnostic code.
//
// This package owns the GOM76xx block. Two of the three codes here are never
// returned as errors at all: they are the warnings internal/engine publishes
// when coverage-guided selection does not happen, and they live here rather
// than in the engine's own GOM40xx block because they name conditions about
// coverage. One condition, one identifier, defined next to the rule it is about.
type Code string

// The coverage codes.
const (
	// CodeMalformedProfile reports a `go tool covdata textfmt` document this
	// package could not read: a missing mode line, a block record that is not
	// in the documented shape, or a coordinate that is not a number. It is
	// infrastructure trouble rather than anything the user did — the profile
	// was written by the toolchain moments earlier — and internal/engine turns
	// it into [CodeUnavailable] rather than into a failed run.
	CodeMalformedProfile Code = "GOM7600"

	// CodeCustomTestCommand reports coverage-guided selection switched off
	// because `test.command` is not `go test` over package patterns.
	//
	// The built-in `go test ./...` is the trivial case of that shape and a
	// narrowing such as `go test ./internal/...` is another, so a custom command
	// is not by itself a reason to switch anything off. What a recognised command
	// has in common is that go-mutants can state in full which suites it runs.
	// The mapping is between a *test binary* and the lines it reached, and it is
	// only sound because go-mutants compiled those binaries itself and knows
	// which package each one belongs to.
	//
	// An unrecognised command is an opaque program whose coverage cannot be
	// attributed to them: `./scripts/test.sh`, `gotestsum`, or a `go test`
	// carrying anything that is not a pattern — `-run` alone makes the command a
	// fraction of the suite it names — may run a subset, a superset, several
	// suites, or something that is not `go test` at all. There is no honest way
	// to attribute such a run's coverage to the per-package binaries the
	// execution phase actually starts. Guessing would silently skip mutants that
	// a test does cover, which is a kill lost and a score inflated — so the run
	// says so and measures every mutant against every binary instead.
	//
	// internal/engine's testScope is where a command is read, and it carries the
	// argument for why the reading is spelling-strict.
	CodeCustomTestCommand Code = "GOM7601"

	// CodeUnavailable reports a coverage pass that did not produce anything
	// usable — the profiling run failed, `go tool covdata` was not there or
	// exited non-zero, the output could not be parsed, or every profile came
	// back empty — and the run continuing with coverage off.
	//
	// It is always a warning and never a failure. Coverage-guided selection is
	// an optimisation: with it the run executes fewer mutants, and without it
	// the run executes all of them and reaches exactly the same verdicts more
	// slowly. Failing a run because an optimisation was unavailable would trade
	// a correct slow answer for no answer at all.
	CodeUnavailable Code = "GOM7602"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the GOM76xx block.
var codes = []Code{
	CodeMalformedProfile,
	CodeCustomTestCommand,
	CodeUnavailable,
}

// Codes returns every diagnostic code this package can report, in numeric
// order, so that `doctor` can print the table without reading the source.
func Codes() []Code { return slices.Clone(codes) }

// An Error is one coverage failure carrying a stable [Code].
//
// It mirrors the shape internal/engine, internal/execute and internal/discover
// use — code, a one-line message, an optional cause — so that a caller
// rendering any of them needs one path and not four.
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// Message states the problem in one line, without the code.
	Message string
	// Err is the underlying cause, or nil.
	Err error
}

// Error renders "GOM7600: <message>", with the cause appended when there is one.
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
