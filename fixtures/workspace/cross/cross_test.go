// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cross

import "testing"

// TestDiffering pins both answers, which is what kills this module's mutants
// and the sibling module's alike.
//
// The two rows are the two a mutant of [lib.Differs] can disagree on: `!=`
// turned into `==` swaps them both, and a returned `true` or `false` breaks one
// each. Killing those from here is the whole point of the fixture.
func TestDiffering(t *testing.T) {
	for _, row := range []struct {
		a, b int
		want bool
	}{
		{a: 1, b: 2, want: true},
		{a: 2, b: 2, want: false},
	} {
		if got := Differing(row.a, row.b); got != row.want {
			t.Errorf("Differing(%d, %d) = %v, want %v", row.a, row.b, got, row.want)
		}
	}
}
