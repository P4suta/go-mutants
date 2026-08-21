// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache

import (
	"errors"
	"slices"
	"strings"
)

// A Code is a stable, user-facing diagnostic code. This package owns the
// GOM79xx block.
type Code string

// The outcome cache codes.
//
// Every one of them is a warning in a run rather than a failure of one. The
// cache is an optimisation: a run that cannot read it, cannot write it, or
// cannot even find the directory it lives in reaches exactly the verdicts it
// would have reached without it, only more slowly. The one place these become
// errors is `cache status|gc|clean`, where operating on the cache is the whole
// of what was asked for.
const (
	// CodeUnavailable reports a cache that could not be opened: no operating
	// system cache directory, a directory that could not be created, or one the
	// ownership marker refuses. The run continues with the cache off.
	CodeUnavailable Code = "GOM7901"
	// CodeExecutableUnreadable reports that the running executable could not be
	// located or read, so its digest cannot enter the key. Without it a key
	// would silently span two builds of go-mutants, so the run continues with
	// the cache off rather than with a key that means less than it says.
	CodeExecutableUnreadable Code = "GOM7902"
	// CodeInvalidContext reports a key context missing a field the recipe
	// requires — no workspace digest, no test command. It is a caller bug: a key
	// computed from a half-filled context would collide across runs that have
	// nothing in common.
	CodeInvalidContext Code = "GOM7903"
	// CodeCorruptEntry reports an entry that is on disk and is not an entry:
	// truncated JSON, an unknown version, an outcome this build does not know,
	// or a document filed under somebody else's id. It is read as a miss, and
	// reported once per run rather than once per entry.
	CodeCorruptEntry Code = "GOM7904"
	// CodeEntryNotWritten reports an outcome that could not be stored. Nothing
	// about the run changes; the next run simply measures the mutant again.
	CodeEntryNotWritten Code = "GOM7905"

	// CodeScanFailed reports a cache directory that could not be listed, which
	// is what `cache status` and `cache gc` walk.
	CodeScanFailed Code = "GOM7910"
	// CodeNotRemoved reports an entry or a directory `cache gc` or `cache clean`
	// could not delete. The deletion is the whole of what those commands do, so
	// this one is reported rather than absorbed.
	CodeNotRemoved Code = "GOM7911"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the GOM79xx block.
var codes = []Code{
	CodeUnavailable,
	CodeExecutableUnreadable,
	CodeInvalidContext,
	CodeCorruptEntry,
	CodeEntryNotWritten,
	CodeScanFailed,
	CodeNotRemoved,
}

// Codes returns every diagnostic code this package can report, in numeric
// order, so that `doctor` can print the table without reading the source.
func Codes() []Code { return slices.Clone(codes) }

// An Error is one cache failure carrying a stable [Code].
//
// It mirrors the shape internal/engine and internal/report use — code, one-line
// message, optional cause — so that one renderer lays them all out alike
// without the packages sharing an error identity.
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// Message states the problem in one line, without the code.
	Message string
	// Err is the underlying cause, or nil. It stays reachable through errors.Is
	// and errors.As.
	Err error
}

// Error renders "GOM7904: <message>", with the cause appended when there is
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
