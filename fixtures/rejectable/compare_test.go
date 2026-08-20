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

// TestMatches pins the trapped comparison in this file.
//
// The mutant here is rejected rather than run, but the function it was proposed
// for is ordinary code and stays part of the suite: a fixture whose trapped
// functions were untested would let a broken restoration — pristine bytes
// written back wrong — pass unnoticed.
func TestMatches(t *testing.T) {
	if !Matches("go", "go") {
		t.Error(`Matches("go", "go") = false, want true`)
	}
	if Matches("go", "rust") {
		t.Error(`Matches("go", "rust") = true, want false`)
	}
}
