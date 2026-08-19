// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package failingbaseline

import "testing"

// TestDoubleIsDeliberatelyWrong always fails. go-mutants' integration suite
// asserts that a run against this workspace stops at the baseline and quotes
// this failure back, so the assertion below must stay false and the message
// must stay recognisable.
func TestDoubleIsDeliberatelyWrong(t *testing.T) {
	if got := Double(21); got != 43 {
		t.Errorf("this fixture fails on purpose: Double(21) = %d, want 43", got)
	}
}
