// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

import "testing"

// TestBetween pins the open range on both bounds and inside it.
//
// The two bound rows are the only inputs at which `>` and `>=` — and `<` and
// `<=` — disagree, so each of them is carrying a kill on its own. The inside row
// is what kills the negation of the whole condition and the `true` it returns.
func TestBetween(t *testing.T) {
	cases := []struct {
		name string
		v    int
		want bool
	}{
		{"at the low bound", 0, false},
		{"inside", 5, true},
		{"at the high bound", 10, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Between(c.v, 0, 10); got != c.want {
				t.Errorf("Between(%d, 0, 10) = %v, want %v", c.v, got, c.want)
			}
		})
	}
}

// TestOutside is the same discipline on the closed range: both ends and one
// input between them.
func TestOutside(t *testing.T) {
	cases := []struct {
		name string
		v    int
		want bool
	}{
		{"at the low end", 0, true},
		{"between the ends", 5, false},
		{"at the high end", 10, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Outside(c.v, 0, 10); got != c.want {
				t.Errorf("Outside(%d, 0, 10) = %v, want %v", c.v, got, c.want)
			}
		})
	}
}

func TestMissing(t *testing.T) {
	names := map[string]bool{"alpha": true}
	if Missing(names, "alpha") {
		t.Error("Missing(names, \"alpha\") = true, want false: alpha is in the set")
	}
	if !Missing(names, "beta") {
		t.Error("Missing(names, \"beta\") = false, want true: beta is not in the set")
	}
}

// TestToggle is the fixture's under-tested boolean, and the omission is the
// point.
//
// It calls Toggle with both inputs, so both of its returns are covered and
// every mutant of it is really executed, and then checks nothing about what
// came back. All three of Toggle's mutants survive that, which is what a
// mutation run is for: the missing assertion is invisible to `go test` and to
// coverage alike, and only a survivor names it.
func TestToggle(t *testing.T) {
	Toggle(true)
	Toggle(false)
}
