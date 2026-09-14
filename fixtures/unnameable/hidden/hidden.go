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

// tally is the second unnameable type, and it is unexported *and* numeric.
//
// The difference is what keeps the fixture armed. A `*counter` can be passed
// around outside this package but never combined, so every expression another
// file can write over one has a sayable type -- the call that produced it, or
// an `int` it yields, and a closure returning either is something the guard
// forms can write. Arithmetic over a `tally` has the `tally` type itself, which
// is the one shape where an edit's own expression, and every expression
// enclosing it, is something no other file can name.
type tally int

// Get returns a tally. Nothing outside this package can declare one, and
// nothing outside it can name the type of `Get(1) + Get(2)` either.
func Get(n int) tally { return tally(n) }

// Count reads a tally back as an ordinary int, so that a caller can do
// something with one.
func Count(t tally) int { return int(t) }
