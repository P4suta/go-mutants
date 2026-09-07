// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package hidden

import "testing"

// TestCounterRoundTrips kills the one mutant this package holds, so that the
// fixture's survivors are zero and its whole story is the skip.
func TestCounterRoundTrips(t *testing.T) {
	if got := New(5).Value(); got != 5 {
		t.Errorf("New(5).Value() = %d, want 5", got)
	}
}
