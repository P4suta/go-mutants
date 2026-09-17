// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package rootmodule

import "testing"

func TestPositive(t *testing.T) {
	for _, c := range []struct {
		n    int
		want bool
	}{{1, true}, {0, false}, {-1, false}} {
		if got := Positive(c.n); got != c.want {
			t.Errorf("Positive(%d) = %t, want %t", c.n, got, c.want)
		}
	}
}
