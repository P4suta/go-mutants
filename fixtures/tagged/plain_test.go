// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tagged

import "testing"

// TestPlain kills the one candidate every run of this fixture has.
func TestPlain(t *testing.T) {
	if !Plain() {
		t.Error("Plain() = false, want true")
	}
}
