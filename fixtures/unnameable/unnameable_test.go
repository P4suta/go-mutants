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
