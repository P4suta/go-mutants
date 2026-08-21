// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

import "errors"

// ErrTooHigh reports a level above what this fixture accepts, and ErrSecond is
// the failure Wrap falls through to.
//
// Package-level variables, whose initialisers are a documented v1 exclusion.
// There is nothing in an `errors.New` call for a rule to match, so these cost
// the fixture neither a mutant nor a skip; they are here because the
// error-swallowing family needs error values that are not `nil` to swallow.
var (
	ErrTooHigh = errors.New("families: level is too high")
	ErrSecond  = errors.New("families: the second check failed")
)

// Check reports whether a level is acceptable.
//
// KILLED. `return-err-to-nil` is the first half of the error-swallowing family
// and the single highest-yield rule in the catalogue for Go: a function that
// returns the error it found becoming one that returns `nil` is the failure
// mode Go suites miss most often. The `return nil` below is not a second
// candidate — a replacement identical to the original is not a mutation.
func Check(level int) error {
	if level > 100 {
		return ErrTooHigh
	}
	return nil
}

// Wrap returns the first of two failures, or nil when neither happened.
//
// KILLED. `nil-error-branch` is the other half of the family: it replaces the
// whole `err != nil` comparison with `false`, so the branch stops firing
// without the comparison operator being touched at all. That is a different
// mutant from `neq-to-eq` on the same line, and both are here — together with
// the negation of the whole condition, which is a third.
func Wrap(first, second error) error {
	if first != nil {
		return first
	}
	return second
}

// Label names the state a code is in.
//
// KILLED. Both returns are string literals that are not the empty string, so
// each is a `return-empty-string` site; a function returning `""` already would
// produce no candidate there and no skip either.
func Label(code int) string {
	if code == 0 {
		return "ok"
	}
	return "error"
}
