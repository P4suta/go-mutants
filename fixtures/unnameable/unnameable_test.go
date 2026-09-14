// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package unnameable

import "testing"

// TestCounted kills the candidate beside the skipped one, which is how the
// fixture states that a refusal at one site does not settle the file.
func TestCounted(t *testing.T) {
	if got := Counted(2, 3); got != 5 {
		t.Errorf("Counted(2, 3) = %d, want 5", got)
	}
}

// TestUnsayable covers the function the refusal lives in, so that the skip is a
// site a test reaches rather than one nothing would have measured anyway.
//
// A refusal in unreached code proves nothing: the mutant would have been an
// uncovered survivor either way, and the run would look the same with the
// refusal and without it.
func TestUnsayable(t *testing.T) {
	if got := Unsayable(2, 3); got != 2 {
		t.Errorf("Unsayable(2, 3) = %d, want 2", got)
	}
}
