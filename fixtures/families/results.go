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

// Index maps each name to the position it was given in.
//
// KILLED. The map that comes back is two candidates, not one: `return-nil` from
// the return-replacement family and `return-empty-map` from the neutral-value
// family, which is the whole point of that family existing. `nil` and
// `map[string]int{}` are different programs — one panics on write and reads as
// `null` in JSON, the other does neither — and no amount of `len()` tells them
// apart. Comparing the map's contents kills both, and the deleted assignment
// in the loop with them.
func Index(names []string) map[string]int {
	out := map[string]int{}
	for i, name := range names {
		out[name] = i
	}
	return out
}

// Tags returns the tags it was handed.
//
// SURVIVED, one of its two mutants, and it is the fixture's demonstration of
// why the neutral-value family is worth its noise. [TestTags] asserts only that
// the result is not nil — the check a great many Go tests actually make.
// `return-nil` dies to it instantly. `return-empty-slice` does not: `[]string{}`
// is not nil, so the assertion holds while the function has stopped returning
// anything at all.
//
// That asymmetry is the family's entire argument, and it is stated here as a
// fate rather than as a sentence in a document. A test added here that compared
// the contents would kill the survivor and leave the argument unmade.
func Tags(tags []string) []string {
	return tags
}
