// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package inner

import "testing"

func TestDoubled(t *testing.T) {
	if got := Doubled(3); got != 6 {
		t.Errorf("Doubled(3) = %d, want 6", got)
	}
}

func TestAbove(t *testing.T) {
	for _, c := range []struct {
		n, limit int
		want     bool
	}{{2, 1, true}, {1, 1, false}, {0, 1, false}} {
		if got := Above(c.n, c.limit); got != c.want {
			t.Errorf("Above(%d, %d) = %t, want %t", c.n, c.limit, got, c.want)
		}
	}
}
