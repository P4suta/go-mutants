// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"errors"
	"slices"
	"strings"
)

// A Code is a stable, user-facing diagnostic code.
//
// This package owns the GOM73xx block. Most of these codes report an internal
// invariant violation rather than anything the user did: by the time bytes
// reach the instrumenter the tree has already loaded, type-checked and passed
// its baseline, so a flattener or splicer failure is a bug in go-mutants. They
// are still coded and still surfaced, because the alternative to failing with
// an identifier the user can quote in a bug report is failing silently with a
// wrong edit.
type Code string

// The instrumentation codes.
const (
	// CodeUntokenizable reports source that go/scanner could not tokenize:
	// an illegal character, an unterminated literal, invalid UTF-8. Flatten
	// works at the token level and refuses to guess what such bytes meant.
	CodeUntokenizable Code = "GOM7301"
	// CodeRawStringConversion reports a string or rune literal carrying a line
	// break whose value could not be recovered, and so could not be re-spelled
	// without the line break. Flattening it any other way would change what the
	// literal denotes.
	CodeRawStringConversion Code = "GOM7302"
	// CodeNotIdentical reports that the flattened output does not re-tokenize
	// to the token stream it was built from. It is the flattener's own
	// self-check failing — a missing separator between two tokens that fused
	// into a third, say — and it always means a bug in this package.
	CodeNotIdentical Code = "GOM7303"
	// CodeNotFlat reports flattened output that still contains a line break.
	// Same category as [CodeNotIdentical]: the one-line postcondition is
	// checked rather than assumed, because everything downstream of it — line
	// preservation, and with it the coverage mapping — rests on it holding.
	CodeNotFlat Code = "GOM7304"

	// CodeSpliceSpan reports a splice whose span is malformed or reaches past
	// the end of the source it is being applied to.
	CodeSpliceSpan Code = "GOM7310"
	// CodeSpliceMismatch reports a splice whose span does not cover the bytes
	// the splice says it replaces. See the package documentation: this is the
	// check that stands between a stale span and a corrupted source file.
	CodeSpliceMismatch Code = "GOM7311"
	// CodeSpliceOverlap reports two splices that would edit the same bytes, or
	// two splices with the same span. Either way the result would depend on
	// the order the caller happened to list them in, so neither is applied.
	CodeSpliceOverlap Code = "GOM7312"
	// CodeSpanStraddles reports a span handed to [OffsetMap.MapSpan] that
	// starts or ends inside replaced bytes, where no exact translation into
	// output coordinates exists.
	CodeSpanStraddles Code = "GOM7313"

	// CodeOptions reports [Options] that cannot be instrumented against: no
	// snapshot root, no module path, no catalogue, or a catalogue naming a
	// path that leaves the snapshot.
	CodeOptions Code = "GOM7320"
	// CodeSourceUnreadable reports a snapshot file the catalogue names that
	// could not be read back.
	CodeSourceUnreadable Code = "GOM7321"
	// CodeUnparsable reports Go source that go/parser rejected. It covers both
	// directions: a snapshot file that does not parse — which means the tree
	// drifted after discovery type-checked it — and, as a postcondition,
	// instrumented output that does not parse, which is always a bug in this
	// package.
	CodeUnparsable Code = "GOM7322"
	// CodeUnsupportedGuard reports a guard hint naming a rewrite this phase
	// cannot express: a form it does not emit, a statement Form S may not bury
	// in a block, or a declaration Form D cannot turn into an assignment.
	// Discovery refuses such a site rather than hinting at it, so this means
	// the two have drifted apart.
	CodeUnsupportedGuard Code = "GOM7323"
	// CodeSiteNotFound reports a guard hint whose site span names no node of
	// the kind the form needs — no expression for Form C, no statement for the
	// other two — or a declaration missing the very token that declares. The
	// hint and the file disagree about what is there, so nothing is edited.
	CodeSiteNotFound Code = "GOM7324"
	// CodeSiteConflict reports rewrite sites the interval forest refused to
	// place. Two expressions of one file either nest or are disjoint, so this
	// always means a site span was computed wrong: a bug in this package.
	CodeSiteConflict Code = "GOM7325"
	// CodeLineDrift reports instrumentation that would move a line: a splice
	// set that is not [LinePreserving], or output whose line count differs from
	// the file it was built from. It is the machine-checked form of the
	// invariant the package documentation opens with, and it is a bug in this
	// package rather than anything a user did.
	CodeLineDrift Code = "GOM7326"
	// CodeImportInjection reports a file whose runtime import could not be
	// placed, because neither a package clause nor an import declaration was
	// found where the parsed file says one is. Another internal invariant: a
	// file that parsed has a package clause.
	CodeImportInjection Code = "GOM7327"
	// CodeWriteFailed reports a snapshot the instrumenter could not write to,
	// either an instrumented file or the generated runtime package.
	CodeWriteFailed Code = "GOM7328"
	// CodeMissingGuard reports a catalogued mutant with no guard hint. Choosing
	// a rewrite site needs type information this package does not have and does
	// not want, so a missing hint is a refusal rather than a fallback: see
	// [Hints].
	CodeMissingGuard Code = "GOM7329"
	// CodeInfectionLog reports an infection log [ReadInfectionLog] will not
	// read: an empty file, a missing header, a header naming another catalogue
	// or another array width, a repeated header that differs from the first, an
	// index that is not a decimal uint32, an index at or past the catalogue's
	// size — which is every index there is when the catalogue is empty — or a
	// last line the process that wrote it never finished. It also reports a
	// negative catalogue size, which is the caller's own bug. An infection fact
	// is a licence not to run a test, so a log that cannot be read whole yields
	// nothing rather than the part of itself that still parses.
	CodeInfectionLog Code = "GOM7330"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the GOM73xx
// block.
var codes = []Code{
	CodeUntokenizable,
	CodeRawStringConversion,
	CodeNotIdentical,
	CodeNotFlat,
	CodeSpliceSpan,
	CodeSpliceMismatch,
	CodeSpliceOverlap,
	CodeSpanStraddles,
	CodeOptions,
	CodeSourceUnreadable,
	CodeUnparsable,
	CodeUnsupportedGuard,
	CodeSiteNotFound,
	CodeSiteConflict,
	CodeLineDrift,
	CodeImportInjection,
	CodeWriteFailed,
	CodeMissingGuard,
	CodeInfectionLog,
}

// Codes returns every diagnostic code this package can report, in numeric
// order, so that `doctor` can print the table without reading the source.
func Codes() []Code { return slices.Clone(codes) }

// An Error is one instrumentation failure carrying a stable [Code].
//
// It mirrors the shape internal/discover, internal/engine and internal/gocmd
// use — code, one-line message, optional cause — so a single renderer can lay
// them all out the same way, without the packages sharing an error identity.
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// Message states the problem in one line, without the code.
	Message string
	// Err is the underlying cause, or nil. It stays reachable through
	// errors.Is and errors.As.
	Err error
}

// Error renders "GOM7311: <message>", with the cause appended when there is
// one.
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
