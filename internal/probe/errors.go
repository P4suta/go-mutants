// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package probe

import "errors"

// A Code is the stable identifier of one failure this package can report.
//
// The block is 71xx, between internal/snapshot's 70xx and internal/runner's
// 72xx, and is this package's alone. A code is allocated once and never reused,
// even after the condition it named is gone. See docs/errors.md.
type Code string

// The diagnostic codes.
const (
	// CodeUnavailable reports that a probe pass could not be made: the tree
	// would not build, a pass failed, or the runtime could not write its log.
	//
	// It is always a warning and never fails a run, which is internal/coverage's
	// rule and internal/coverage's argument. Probing is an optimisation: a run
	// that cannot make one measures every mutant against every covering binary,
	// which is exactly what a run without probing does. Turning that into a
	// failure would make an optional saving a required step.
	CodeUnavailable Code = "GOM7101"

	// CodeNothingToProbe reports that a pass was asked for over no mutants or
	// no binaries.
	//
	// It is separated from [CodeUnavailable] because it is not a failure at
	// all: a run whose every mutant was already settled by coverage has nothing
	// left for a probe to say, and paying for a tree to say it would be the one
	// case where the optimisation is pure loss.
	CodeNothingToProbe Code = "GOM7102"

	// CodeInconsistent reports an infection log naming an index the catalogue
	// cannot explain.
	//
	// It is a bug in go-mutants rather than a condition of the tree under test:
	// the log's header carries the catalogue's digest and the reader bounds
	// every index by its size, so an index that survives both and still names
	// nothing means the catalogue and the tree were built from different
	// passes. Every fact of the run is then discarded and the run measures
	// everything, because a repaired set is a set nobody can vouch for.
	CodeInconsistent Code = "GOM7103"
)

// Codes returns every code this package can report, in numeric order.
func Codes() []Code {
	return []Code{CodeUnavailable, CodeNothingToProbe, CodeInconsistent}
}

// ErrInconsistent is what [Settle] returns when a log names an index the
// catalogue cannot explain. See [CodeInconsistent].
var ErrInconsistent = errors.New("probe: an infection log names a mutant this catalogue does not hold")

// An Error is a failure this package reports, with the code that names it.
type Error struct {
	Code    Code
	Message string
	Err     error
}

// Error renders the failure as "GOMnnnn: message".
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return string(e.Code) + ": " + e.Message + ": " + e.Err.Error()
	}
	return string(e.Code) + ": " + e.Message
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
