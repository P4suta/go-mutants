// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package rejectable

import "testing"

// TestInRange pins InRange on both sides of both bounds.
//
// The suite exists so that the instrumented baseline has something to prove.
// `go build ./...` never compiles a _test.go file, so a validation phase that
// ended at a green build would not have compiled half the tree; running these
// tests with no mutant active is what says the accepted guards both compile and
// mean what the unmutated code meant.
//
// It is also what makes every healthy candidate in this file a killable one.
// The fixture's claim is that validation removes exactly the traps, and the
// cleanest evidence for it is a run that scores 100 on what is left: a healthy
// mutant nothing kills would sit in the report as a survivor and be
// indistinguishable, at a glance, from a trap that slipped through.
func TestInRange(t *testing.T) {
	cases := []struct {
		name      string
		v, lo, hi int
		want      bool
	}{
		{"below the low bound", -1, 0, 10, false},
		{"at the low bound", 0, 0, 10, true},
		{"inside", 5, 0, 10, true},
		{"at the high bound", 10, 0, 10, true},
		{"above the high bound", 11, 0, 10, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := InRange(c.v, c.lo, c.hi); got != c.want {
				t.Errorf("InRange(%d, %d, %d) = %v, want %v", c.v, c.lo, c.hi, got, c.want)
			}
		})
	}
}

// TestErased pins the trapped multiplication in this file.
//
// The mutant on the `*` is rejected rather than run, but the function it was
// proposed for is ordinary code and stays part of the suite: a fixture whose
// trapped functions were untested would let a broken restoration — pristine
// bytes written back wrong — pass unnoticed, and the two healthy candidates
// sharing the statement need a test to be killed by.
func TestErased(t *testing.T) {
	if got := Erased(7); got != 1 {
		t.Errorf("Erased(7) = %d, want 1", got)
	}
	if got := Erased(0); got != 1 {
		t.Errorf("Erased(0) = %d, want 1", got)
	}
}
