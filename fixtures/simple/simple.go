// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package simple is the happy-path fixture: a workspace that builds, whose
// tests pass quickly, and whose functions are worth mutating.
//
// Every function here is chosen for the operator families the v1 catalogue
// starts with — a comparison, an arithmetic operator, a boolean connective, a
// boolean literal, and an early return — so that the same fixture serves the
// baseline tests today and the discovery and kill tests later.
package simple

// Clamp confines v to the inclusive range [lo, hi].
//
// It carries two comparisons and two early returns, and swapping either
// comparison for its neighbour (`<` for `<=`) changes the result only at the
// boundary, which is exactly the mutant a boundary-blind test misses.
func Clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Sum adds the values.
func Sum(values []int) int {
	total := 0
	for _, v := range values {
		total += v
	}
	return total
}

// InRange reports whether v is inside the inclusive range [lo, hi].
//
// The connective is the point: `&&` mutated to `||` survives any test that
// only ever asks about values inside the range.
func InRange(v, lo, hi int) bool {
	return v >= lo && v <= hi
}
