// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

// Mask folds two masks together four ways, one per bitwise pair rule.
//
// KILLED. Each pair is bound to its own name rather than being written as one
// expression, and that is not tidiness: `&` and `&^` bind tighter than `|` and
// `^`, so in a single chain a swapped operator would regroup the expression as
// well as change it, and the mutant would stop being about the operator. It
// also puts four of the bitwise rules on `:=` sites, which is the declaration
// rewrite form rather than the statement one.
//
// The four values are summed rather than combined with `|`, because `|` absorbs:
// with the masks TestMask uses, three of the four swaps would produce the same
// number as the original and the fixture would claim kills it cannot deliver.
func Mask(a, b uint8) uint8 {
	both := a & b
	either := a | b
	odd := a ^ b
	rest := a &^ b
	return both + either + odd + rest
}

// Shift moves v in both directions at once.
//
// KILLED. A shift is gated on its left operand alone — the count is an operand
// of a different kind, and no rule ever rewrites it — which is why `n` is a
// `uint` here and carries no mutants of its own.
func Shift(v uint8, n uint) uint8 {
	return v<<n + v>>n
}

// Salt is the fixture's deliberately under-tested bit twiddling.
//
// SURVIVES, all three of its mutants. TestSalt calls it and throws the result
// away, which is the same gap TestToggle leaves in the boolean family and the
// same gap TestWeigh leaves in the arithmetic one. Four under-tested functions
// rather than one is deliberate — [Drift] is the fourth — because a per-family
// table with a single survivor row could not tell "the run reports survivors"
// from "the run reports this one".
func Salt(v uint8, n uint) uint8 {
	return v>>n ^ 1
}
