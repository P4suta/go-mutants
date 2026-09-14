// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

import "testing"

// TestSteps counts a positive number of iterations, and must never count a
// non-positive one.
//
// Three is not arbitrary: with a positive limit every mutant of Steps settles
// immediately, and with a limit of zero or below the negated loop condition
// never stops. A zero row here would not fail the suite — it would make one
// mutant hang until the run's timeout, and turn a fast fixture into a slow one
// with a timeout where a kill should be.
func TestSteps(t *testing.T) {
	if got := Steps(3); got != 3 {
		t.Errorf("Steps(3) = %d, want 3", got)
	}
}

func TestRemaining(t *testing.T) {
	if got := Remaining(10, 3); got != 7 {
		t.Errorf("Remaining(10, 3) = %d, want 7", got)
	}
}

func TestNet(t *testing.T) {
	if got := Net([]int{5, 4}, []int{2}); got != 7 {
		t.Errorf("Net([5 4], [2]) = %d, want 7", got)
	}
}

// TestDrift is the fixture's under-tested accumulator, and the omission is the
// point.
//
// The slice is not empty, so the loop body really runs and the compound
// assignment inside it is covered and executed — this is a survivor the run
// measured, not one it inferred from coverage. What the row leaves out is a
// non-zero element: adding zero and subtracting zero agree, and so does
// returning the zero the accumulator started from.
func TestDrift(t *testing.T) {
	if got := Drift([]int{0, 0}); got != 0 {
		t.Errorf("Drift([0 0]) = %d, want 0", got)
	}
}

// TestSearch pins the count rather than the answer.
//
// Counting is what makes the label observable: a test that asked only whether
// the value was found would pass against a bare `break`, because the value is
// found either way. The row holding the match is not the last one, so the two
// spellings disagree.
func TestSearch(t *testing.T) {
	if got := Search([][]int{{1, 2}, {3, 4}}, 2); got != 2 {
		t.Errorf("Search([[1 2] [3 4]], 2) = %d, want 2", got)
	}
}

// TestTally puts the skipped value in the middle of its row, which is the only
// place the label changes the answer.
func TestTally(t *testing.T) {
	if got := Tally([][]int{{1, 2, 5}, {3, 4}}, 2); got != 8 {
		t.Errorf("Tally([[1 2 5] [3 4]], 2) = %d, want 8", got)
	}
}
