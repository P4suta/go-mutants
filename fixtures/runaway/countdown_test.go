// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runaway

import (
	"slices"
	"testing"
)

// TestCountdown pins what Countdown answers, including the case that answers
// nothing.
//
// That row is the fixture's whole point and it is deliberately first. A negated
// loop condition is *false* wherever the original was true, so a mutant of it
// under-runs on every other row and would be caught by an ordinary assertion;
// only where the original loop ran zero times does the negation run forever.
// Making it first means the runaway starts before anything else has had a
// chance to fail, so the process the bound has to stop is a process that is
// doing nothing but allocating.
//
// The assertions are Errorf rather than Fatalf so that a mutant which fails an
// earlier row still reaches the rows after it. A table that stopped at the
// first failure would make the fixture's outcome depend on which mutant is
// active, which is the one thing a fixture must not do.
func TestCountdown(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want []int
	}{
		{"nothing to count", 0, nil},
		{"one", 1, []int{1}},
		{"three", 3, []int{3, 2, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Countdown(c.n); !slices.Equal(got, c.want) {
				t.Errorf("Countdown(%d) = %v, want %v", c.n, got, c.want)
			}
		})
	}
}
