// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package hidden exports a constructor whose result type is not exported. It
// exists for one reason: a variable of that type cannot be declared by name
// anywhere outside this package, which is the shape a Form D site has to
// refuse.
package hidden

// counter is the unnameable type.
type counter struct{ n int }

// New returns one. The type it returns is this package's own and unexported,
// so `var c *hidden.counter` is not something another file may write.
func New(n int) *counter { return &counter{n: n} }

// Value reads it back.
func (c *counter) Value() int { return c.n }
