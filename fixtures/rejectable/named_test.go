// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package rejectable

import "testing"

// TestReady pins the threshold on both sides of it.
//
// The 3 row is the only input at which `>=` and `>` disagree, so it is the one
// carrying the comparison's kill; between them the two rows also kill both
// replacements of the returned value. Killing rather than merely accepting is
// what makes this file evidence: an accepted mutant proves the guard compiled,
// and only an executed one proves the statement form really carried it.
func TestReady(t *testing.T) {
	if Ready(2) {
		t.Error("Ready(2) = true, want false: 2 is below the threshold")
	}
	if !Ready(3) {
		t.Error("Ready(3) = false, want true: 3 is the threshold")
	}
}

func TestAlways(t *testing.T) {
	if !Always() {
		t.Error("Always() = false, want true")
	}
}
