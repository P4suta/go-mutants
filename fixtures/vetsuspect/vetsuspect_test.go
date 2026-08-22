// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package vetsuspect

import "testing"

// The rows are the same four in both tables, and each one is carrying a kill.
// The two dot names are the only inputs at which the equality swaps disagree
// with the source, and the two ordinary names are the only ones at which the
// connective swaps do — a table missing either half would turn a mutant this
// fixture claims to kill into a survivor, and the run would still be green.
var rows = []struct {
	name  string
	s     string
	isDot bool
}{
	{"the current directory", ".", true},
	{"the parent directory", "..", true},
	{"an ordinary name", "vetsuspect.go", false},
	{"the empty name", "", false},
}

func TestIsDot(t *testing.T) {
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := IsDot(r.s); got != r.isDot {
				t.Errorf("IsDot(%q) = %t, want %t", r.s, got, r.isDot)
			}
		})
	}
}

// TestNotDot pins the other trap against the same rows. The expectation is the
// negation of IsDot's, which is a fact about these two functions rather than a
// shortcut: writing it out as `!r.isDot` is what keeps one table honest about
// both.
func TestNotDot(t *testing.T) {
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := NotDot(r.s); got != !r.isDot {
				t.Errorf("NotDot(%q) = %t, want %t", r.s, got, !r.isDot)
			}
		})
	}
}
