// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package caller

import "testing"

// TestBelow never asks about the boundary, which is what makes this binary one
// that runs Gate's line and cannot tell the mutant from the original.
//
// Both rows are away from v == lo on purpose. A row at the boundary would make
// this binary name the mutant too, and the fixture's narrowing claim would
// become "covered by two and narrowed to two", which is no narrowing at all.
func TestBelow(t *testing.T) {
	if !Below(1, 2) {
		t.Error("Below(1, 2) = false, want true")
	}
	if Below(3, 2) {
		t.Error("Below(3, 2) = true, want false")
	}
}
