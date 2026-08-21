// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package returns holds one live example of every return-replacement rule, and
// the four shapes of return the family passes over in silence.
package returns

// Answer is a named string type and Level a named integer one: the family
// reads the declared result's underlying type, so both are mutated.
type Answer string

// Level is the integer half of the same point.
type Level int

// Numeric returns an integer, so the zero is `0`.
func Numeric(a int) int { return a }

// Float returns a float, and `0` is a valid zero for it too.
func Float(a float64) float64 { return a }

// Text returns a string, so the zero is the empty literal.
func Text(a string) string { return a }

// NamedText proves the string rule reads through a named type.
func NamedText(a Answer) Answer { return a }

// NamedLevel does the same for the numeric rule.
func NamedLevel(a Level) Level { return a }

// Boolean returns a bool, which is the one result type with two replacements.
func Boolean(a bool) bool { return a }

// Pointer, Slice, Map, Chan, Func, and Any are the nillable results, and the
// one rule between them is return-nil: none of these is an error.
func Pointer(p *int) *int { return p }

// Slice returns a slice.
func Slice(s []int) []int { return s }

// Map returns a map.
func Map(m map[string]int) map[string]int { return m }

// Chan returns a channel.
func Chan(c chan int) chan int { return c }

// Func returns a function value.
func Func(f func()) func() { return f }

// Any returns an interface that is not error, which is what keeps it here and
// not in the error-swallowing family.
func Any(v any) any { return v }

// Zero is the no-op: the value already is the replacement, so there is no
// candidate — and no skip either, because nothing was declined. The mutation
// and the source would be the same program.
func Zero() int { return 0 }

// None is the same thing for a nillable result.
func None() *int { return nil }

// Bare has named results and returns without naming them. There are no bytes
// to replace, so there is nothing to decide.
func Bare() (n int) {
	n = 1
	return
}

// Multi fills both of its results from one call, so its values and its
// declared results cannot be lined up one to one.
func Multi() (int, Level) { return pair() }

// pair is what Multi returns, and a return the family does line up.
func pair() (int, Level) { return 1, 2 }
