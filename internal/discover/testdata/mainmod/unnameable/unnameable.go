// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package unnameable holds the refusal the reserved skip reason was named for:
// a Form D site whose declared type cannot be written down in this file.
package unnameable

import "example.com/mini/hidden"

// Counted declares a variable of a type this file cannot spell. The value is
// a *hidden.counter, and `counter` is not exported, so there is no source form
// of the declaration Form D would have to emit — not through the import this
// file already has, and discovery never adds one to make a type sayable.
//
// The addition inside the call is an ordinary integer-arithmetic candidate,
// and it is what makes the refusal observable: without an edit at this site
// there would be no hint to compute and nothing to record.
func Counted(a, b int) int {
	c := hidden.New(a + b)
	return c.Value()
}
