// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import "slices"

// A Code is a stable, user-facing diagnostic code.
//
// This package owns the GOM77xx block. It has very few codes because it has
// very little that can go wrong on its own: a dashboard reports what the engine
// says, and every failure of the run itself already has a code from the package
// that noticed it.
//
// GOM76xx belongs to internal/coverage, which spends three codes in it. A
// diagnostic code is a user-facing identity — it is what a person searches for
// and what a script matches on — so one number can only ever mean one thing,
// whichever package emits it.
type Code string

const (
	// CodeProgram reports a terminal the dashboard could not drive: raw mode
	// refused, the output handle rejected a mode change, the input reader
	// failed. The run is unaffected — it has been draining and executing all
	// along — so this is reported after the stream has been consumed, the
	// closing summary is still printed by internal/cli, and the exit status is
	// still the run's own; see internal/cli's reportDashboardFailure.
	CodeProgram Code = "GOM7701"
)

// String returns the code as it is printed.
func (c Code) String() string { return string(c) }

// codes is every code this package can emit, in numeric order. The package
// tests assert that the list is complete, unique, and inside the GOM77xx block.
var codes = []Code{
	CodeProgram,
}

// Codes returns every diagnostic code this package can report, in numeric
// order, so that `doctor` can print the table without reading the source.
func Codes() []Code { return slices.Clone(codes) }

// An Error is one dashboard failure carrying a stable [Code].
//
// It mirrors the shape the other packages use — code, one-line message,
// optional cause — so that internal/cli lays them all out the same way without
// the packages sharing an error identity.
type Error struct {
	// Code is the stable diagnostic code.
	Code Code
	// Message states the problem in one line, without the code.
	Message string
	// Err is the underlying cause, or nil. It stays reachable through
	// errors.Is and errors.As.
	Err error
}

// Error renders "GOM7701: <message>", with the cause appended when there is one.
func (e *Error) Error() string {
	s := string(e.Code) + ": " + e.Message
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }
