// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package killable

import "testing"

// TestClamp pins what Clamp answers on both sides of both bounds, and on the
// two inputs that sit exactly on them.
//
// The two boundary rows are not padding. v == hi is the only input at which
// `v < hi` and `v <= hi` disagree, and v == lo the only one at which `v > lo`
// and `v >= lo` do, so deleting either row turns the corresponding mutant from
// killed into a survivor while every other row keeps passing. The message names
// the arguments and both values, because when the mutant is live that message is
// the evidence that this mutant — and not a build failure or an unrelated test —
// is what turned the suite red.
func TestClamp(t *testing.T) {
	cases := []struct {
		name            string
		v, lo, hi, want int
	}{
		{"below the low bound", -5, 0, 10, 1},
		{"at the low bound", 0, 0, 10, 1},
		{"just inside the low bound", 1, 0, 10, 1},
		{"inside", 5, 0, 10, 5},
		{"just inside the high bound", 9, 0, 10, 9},
		{"at the high bound", 10, 0, 10, 9},
		{"above the high bound", 40, 0, 10, 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Clamp(c.v, c.lo, c.hi); got != c.want {
				t.Errorf("Clamp(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
			}
		})
	}
}
