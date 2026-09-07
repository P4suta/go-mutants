// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package lib

import "testing"

// TestDouble kills every mutant of [Double], so that the survivors this fixture
// reports are the orphan package's and nothing else.
func TestDouble(t *testing.T) {
	if got := Double(3); got != 6 {
		t.Errorf("Double(3) = %d, want 6", got)
	}
}
