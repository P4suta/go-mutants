// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

// Score combines hits and misses into the number this fixture calls a score.
//
// KILLED, and the whole integer-arithmetic family in one expression: `+`, `-`,
// `*`, `/`, and `%` each appear exactly once. None of the divisors is a
// constant zero and none becomes one under a swap, which is what keeps this
// function's mutants ordinary mutants rather than the compile failures
// fixtures/rejectable exists for.
func Score(hits, misses int) int {
	return hits*2 + misses/2 - hits%3
}

// Mix blends two ratios, and is the float family's whole row.
//
// KILLED. The operands are `float64`, which is the only thing that tells these
// four rules apart from the integer ones: both families match the same tokens
// and are chosen by the operand types alone. Every constant here is exact in
// binary, so TestMix can compare what comes back without a tolerance.
func Mix(a, b float64) float64 {
	return a*0.5 + b/2 - a
}

// Weigh is the fixture's deliberately under-tested arithmetic.
//
// SURVIVES, all three of its mutants. TestWeigh asserts only that weighing
// nothing weighs nothing, and at zero the multiplication, the addition, and the
// whole returned expression all agree with the `0` that `return-zero-numeric`
// would put there. Adding a single non-zero row would kill all three, which is
// exactly the point: the survivors name the missing row.
func Weigh(a, b int) int {
	return a*2 + b
}

// Orphan is never called, from a test or from anywhere else in this module.
//
// SURVIVES, uncovered. No test binary reaches the line, so coverage-guided
// selection settles both of its mutants without executing either of them, and
// the report says why they survived rather than only that they did. Calling it
// from a test would leave the module compiling and the suite green while
// quietly removing the one function that proves the narrowing happened.
func Orphan(v int) int {
	return v + 1
}
