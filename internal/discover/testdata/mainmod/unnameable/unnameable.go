// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package unnameable holds the refusal the reserved skip reason was named for:
// an edit whose every possible site has a type that cannot be written down in
// this file.
package unnameable

import "example.com/mini/hidden"

// Counted declares a variable of a type this file cannot spell. The value is
// a *hidden.counter, and `counter` is not exported, so there is no source form
// of the declaration Form D would have to emit — not through the import this
// file already has, and discovery never adds one to make a type sayable.
//
// This used to be the package's whole subject and is now the easy half. Form D
// still refuses the declaration; Form E then takes the *initialiser* instead,
// which is an `int` and perfectly sayable, so the candidate lives and the
// refusal moved on. What is pinned here is that the fallback happens: a site
// that is not the declaration.
func Counted(a, b int) int {
	c := hidden.New(a + b)
	return c.Value()
}

// Unsayable is the refusal that is left, and every part of it is load-bearing.
//
// The search for a site walks outward from the edit and takes the first
// expression it can use, so a refusal needs an edit whose own expression *and*
// every expression around it is unusable. `hidden.Get` returns an unexported
// numeric type, so the `+` between two of them has that type — which is the
// one shape where the edit's own expression cannot be named. Putting it in a
// `switch` tag is what removes every other escape: a tag has no statement
// around it for Form S, Form D or Form F to stand in, and its type is not
// boolean, so Form C and Form C' have nothing to select either. What is left is
// Form E, which has to write the type out, and there is no way to write it.
//
// Move this edit anywhere else and the refusal goes away, which is exactly what
// happened to the function above: every other position in this corpus holds an
// expression whose type is perfectly sayable.
func Unsayable(a, b int) int {
	switch hidden.Get(a) + hidden.Get(b) {
	default:
		return a
	}
}
