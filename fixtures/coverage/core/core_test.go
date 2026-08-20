// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package core

import "testing"

// TestAboveZero asks about the one input at which `>` and `>=` disagree.
//
// The zero row is the test. Deleting it would leave this suite green, leave the
// mutant in [AboveZero] alive, and turn the fixture's "covered by exactly one
// binary and killed by it" claim into "covered by exactly one binary and
// survived", which is a different fixture.
//
// It deliberately says nothing about [Differs] or [Orphan]: their being
// unreached from here is what the rest of the fixture is about.
func TestAboveZero(t *testing.T) {
	cases := []struct {
		name string
		v    int
		want bool
	}{
		{"below", -1, false},
		{"at zero", 0, false},
		{"above", 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AboveZero(c.v); got != c.want {
				t.Errorf("AboveZero(%d) = %t, want %t", c.v, got, c.want)
			}
		})
	}
}
