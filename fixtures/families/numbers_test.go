// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

import "testing"

// TestScore pins the whole expression on two rows.
//
// One row would already kill every one of Score's six mutants — each of the
// five operators produces a different number from 10 at (5, 4) — and the second
// is there so that a future edit to the function has two constraints to satisfy
// rather than one.
func TestScore(t *testing.T) {
	if got := Score(5, 4); got != 10 {
		t.Errorf("Score(5, 4) = %d, want 10", got)
	}
	if got := Score(7, 9); got != 17 {
		t.Errorf("Score(7, 9) = %d, want 17", got)
	}
}

// TestMix compares floats exactly, which is safe here and only here: 0.5, 2,
// and 1 are all exact in binary, so 1.5 is the exact value the unmutated
// function produces and a tolerance would only blunt the assertion.
func TestMix(t *testing.T) {
	if got := Mix(1, 4); got != 1.5 {
		t.Errorf("Mix(1, 4) = %v, want 1.5", got)
	}
}

// TestWeigh is the fixture's under-tested arithmetic, and the omission is the
// point.
//
// Weighing nothing weighs nothing under every mutant of Weigh as well as under
// Weigh itself, so all three of its mutants survive this. Adding one non-zero
// row would kill all three; leaving it out is what makes the survivors a
// statement about the test rather than about the code.
func TestWeigh(t *testing.T) {
	if got := Weigh(0, 0); got != 0 {
		t.Errorf("Weigh(0, 0) = %d, want 0", got)
	}
}
