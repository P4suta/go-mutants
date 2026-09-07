// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package hidden exports a constructor whose result type is not exported.
//
// That is the only thing it is for: a variable of `*hidden.counter` cannot be
// declared by name anywhere outside this package, which is the shape the guard
// form used for a short variable declaration has to refuse rather than guess
// at. Exporting `counter`, or returning an interface instead, disarms the
// fixture while leaving the module compiling and its tests green.
package hidden

// counter is the unnameable type.
type counter struct{ n int }

// New returns one.
func New(n int) *counter { return &counter{n: n} }

// Value reads it back.
func (c *counter) Value() int { return c.n }
