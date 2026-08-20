// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package caller

import "testing"

// TestChanged is what kills the mutant in another package.
//
// Both rows are needed: `!=` mutated to `==` swaps the two answers, so a suite
// that only asked about unequal arguments would pass with the mutant live. It
// is also the only test in the module that reaches [core.Differs] at all, which
// is what makes this binary the one and only cover for that mutant.
func TestChanged(t *testing.T) {
	if !Changed(1, 2) {
		t.Error("Changed(1, 2) = false, want true")
	}
	if Changed(1, 1) {
		t.Error("Changed(1, 1) = true, want false")
	}
}
