// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package killable

import "testing"

// TestIsReady asserts the one thing the boolean literal in IsReady can say.
//
// It is also the control in the kill experiment: when a mutant somewhere else in
// this module is activated, this test still has to pass. A run where everything
// fails at once is a broken tree, not a detected mutant.
func TestIsReady(t *testing.T) {
	if !IsReady() {
		t.Error("IsReady() = false, want true")
	}
}
