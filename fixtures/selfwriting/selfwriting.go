// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package selfwriting is the drift gate's specimen: a module whose own test
// suite writes into the package directory it runs in.
//
// It is an ordinary and entirely reasonable thing for a test to do — a golden
// file regenerated on the way past, a fixture rebuilt, a generator run as a
// test — and it is fatal to a mutation run. Every mutant is measured against
// the snapshot the baseline was measured against, so a suite that edits that
// tree makes the second mutant a measurement of a different program from the
// first, and the score a mixture of two.
//
// The code here is deliberately dull. What is under test is the gate rather
// than the operators, and one small function is enough to give the run
// something to catalogue on the way to it.
package selfwriting

// Trim clamps a negative value to zero.
func Trim(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
