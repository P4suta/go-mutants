// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package negate holds the condition-negation and boolean-connective families,
// and the named boolean type that separates what `!` may do from what a Form C
// guard may do.
package negate

// Flag is a named boolean type. Go lets `!` negate it, so the negation rules
// apply; the guard form does not follow, because a bool selector evaluates to
// `bool` and `bool` is not assignable to Flag.
type Flag bool

// Both exercises both connective rules and the condition negation over them.
func Both(a, b int, ok bool, out []int) {
	if ok && a > b {
		out[0] = 1
	}
	if ok || a < b {
		out[1] = 2
	}
}

// Removed exercises the negation-removal rule, whose span is the whole unary
// expression and whose replacement is the operand's own bytes.
func Removed(ok bool, out []int) {
	if !ok {
		out[0] = 1
	}
}

// Loop exercises the loop-condition rule, which is the same edit in the one
// other place Go puts a condition.
func Loop(a, b int, out []int) {
	for a < b {
		a++
	}
	out[0] = a
}

// Named is the refusal: negating a condition of a named boolean type is legal
// Go, and no guard form can hold it. An `if` statement is not one of the
// statements the statement guard covers — wrapping the whole `if` would work
// but is not a form v1 emits — so the candidate is refused with
// unnameable-decl-type rather than catalogued for an instrumenter that would
// have to give it back.
func Named(f Flag, out []int) {
	if f {
		out[0] = 1
	}
}

// Assigned is the behaviour change this family made visible elsewhere. The
// comparison's result has to be a Flag, so the type checker records it as one
// and the bool selector is not available; the statement around it declares,
// so the hint is Form D and names the declared type.
// The comparison is written with `>=` rather than `>` so that exactly one
// candidate in this package carries the ge-to-gt rule, which is what lets a
// test name this site without counting lines.
func Assigned(a, b int) Flag {
	var f Flag = a >= b
	return f
}
