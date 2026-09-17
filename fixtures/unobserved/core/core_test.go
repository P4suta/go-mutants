// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package core

import "testing"

// TestGate asks about the boundary, which is the one input at which `<` and
// `<=` disagree. It is what makes this binary the one that names Gate's mutant,
// and what kills that mutant when it is executed.
func TestGate(t *testing.T) {
	if Gate(2, 2) {
		t.Error("Gate(2, 2) = true, want false")
	}
	if !Gate(1, 2) {
		t.Error("Gate(1, 2) = false, want true")
	}
}

// TestSame runs the settled mutant's line and cannot see it, which is the
// fixture's point: the line is covered, and covering it establishes nothing.
func TestSame(t *testing.T) {
	if got := Same(3); got != 3 {
		t.Errorf("Same(3) = %d, want 3", got)
	}
}
