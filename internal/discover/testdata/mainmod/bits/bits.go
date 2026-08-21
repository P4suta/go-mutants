// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package bits holds one live example of every bitwise rule.
package bits

// Mask is a named integer type, so that the bitwise gate is shown reading the
// underlying type here too.
type Mask uint8

// Ops exercises the four non-shift bitwise rules.
func Ops(a, b uint, out []uint) {
	out[0] = a & b
	out[1] = a | b
	out[2] = a ^ b
	out[3] = a &^ b
}

// Shifts exercises the two shift rules. Only the operator moves: the count is
// an operand of a different kind, and swapping a shift's direction is the
// mutation the rule names.
func Shifts(a, n uint, out []uint) {
	out[0] = a << n
	out[1] = a >> n
}

// Named proves the gate reads through a named type.
func Named(a, b Mask, out []Mask) {
	out[0] = a & b
}
