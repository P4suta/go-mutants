// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package unnameable holds the refusal the reserved skip reason was named for:
// an edit whose every possible site has a type that cannot be written down in
// this file.
//
// The fixture is a refusal beside a candidate, and both halves are the point.
// Without the candidate there would be nothing to prove the pass carried on;
// without the refusal there would be no skip. What makes the refusal a refusal
// is spelled out on [Unsayable], and it is narrower than it looks: nearly every
// position in Go that holds an edit also holds, somewhere around it, an
// expression whose type is perfectly sayable.
package unnameable

import "fixture.example/unnameable/hidden"

// Counted adds two numbers through a value whose type this file cannot spell.
//
// This used to be the refusal and is now the candidate. The declaration is
// still one no guard form may rewrite — `counter` is not exported, so there is
// no source form of the `var` it would need — but the *initialiser expression*
// is an `int`, and a closure returning an `int` stands exactly where it stood.
func Counted(a, b int) int {
	c := hidden.New(a + b)
	return c.Value()
}

// Unsayable is the refusal, and every part of it is load-bearing.
//
// The search for a rewrite site walks outward from the edit and takes the first
// expression it can use, so a refusal needs an edit whose own expression *and*
// every expression around it is unusable. `hidden.Get` returns an unexported
// numeric type, so the `+` between two of them has that type — the one shape
// where the edit's own expression cannot be named. Putting it in a `switch` tag
// removes every other escape: a tag has no statement around it for a statement
// form to stand in, and its type is not boolean, so neither boolean selector
// has anything to select. What is left is the form that writes the type out,
// and there is no way to write it.
//
// Move this edit anywhere else and the refusal goes away, which is exactly what
// happened to [Counted].
func Unsayable(a, b int) int {
	switch hidden.Get(a) + hidden.Get(b) {
	default:
		return hidden.Count(hidden.Get(a))
	}
}
