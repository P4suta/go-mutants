// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import "testing"

// TestFold pins every branch and the boundary between them, so that every
// mutant of [Fold] dies.
func TestFold(t *testing.T) {
	for _, row := range []struct {
		a, b, want int
	}{
		{a: 3, b: 1, want: 2},
		{a: 1, b: 3, want: 4},
		// The one row `gt-to-ge` disagrees on.
		{a: 2, b: 2, want: 4},
	} {
		if got := Fold(row.a, row.b); got != row.want {
			t.Errorf("Fold(%d, %d) = %d, want %d", row.a, row.b, got, row.want)
		}
	}
}
