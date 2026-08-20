// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package rejectable

import "testing"

// TestAbove and the two tests below cover the three functions in flag.go, one
// each. Two of them are trapped and one is not, and the suite treats them
// identically on purpose: the tests describe the fixture's behaviour, and which
// of its candidates survive validation is a fact about the guards rather than
// about the code, so nothing here should have to change when that answer does.
func TestAbove(t *testing.T) {
	if !Above(2, 1) {
		t.Error("Above(2, 1) = false, want true")
	}
	if Above(1, 2) {
		t.Error("Above(1, 2) = true, want false")
	}
}

// TestReady asserts the one thing the boolean literal in Ready can say.
func TestReady(t *testing.T) {
	if !Ready() {
		t.Error("Ready() = false, want true")
	}
}

// TestEnabled covers the healthy candidate that shares a file with both traps.
//
// It is the control: when validation has done its work this function's mutant
// is still in the tree, so the instrumented baseline runs this test through a
// guard while the two above it run through plain restored bytes.
func TestEnabled(t *testing.T) {
	if !Enabled(1, 2) {
		t.Error("Enabled(1, 2) = false, want true")
	}
	if Enabled(3, 3) {
		t.Error("Enabled(3, 3) = true, want false")
	}
}
