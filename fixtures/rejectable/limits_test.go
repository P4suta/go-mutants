// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package rejectable

import "testing"

// TestLevel and TestRatio cover the two functions in limits.go, one each.
//
// Both are trapped and the suite treats them exactly as it treats the untrapped
// code in compare.go, on purpose: the tests describe the fixture's behaviour,
// and which of its candidates survive validation is a fact about the mutants
// rather than about the code, so nothing here should have to change when that
// answer does.
func TestLevel(t *testing.T) {
	if got := Level(); got != 100 {
		t.Errorf("Level() = %d, want 100", got)
	}
}

// TestRatio is the control for the declaration guard.
//
// When validation has done its work the two healthy candidates in this function
// are still in the tree, so the instrumented baseline runs this test through a
// hoisted declaration and its guard while the rejected multiplication runs
// through plain restored bytes.
func TestRatio(t *testing.T) {
	if got := Ratio(9); got != 1 {
		t.Errorf("Ratio(9) = %d, want 1", got)
	}
	if got := Ratio(0); got != 1 {
		t.Errorf("Ratio(0) = %d, want 1", got)
	}
}
