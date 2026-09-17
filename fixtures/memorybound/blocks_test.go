// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package memorybound

import "testing"

// TestBlocks pins what Blocks answers, including the case that answers nothing.
//
// That row is the fixture's whole point and it is deliberately first, for the
// reason `runaway`'s table puts its own first: a negated loop condition is
// *false* wherever the original was true, so a mutant of it under-runs on every
// other row and would be caught by an ordinary assertion. Only where the
// original loop ran zero times does the negation run forever, and making that
// row first means the process the bound has to stop is one doing nothing but
// allocating.
//
// The assertions are Errorf rather than Fatalf so that a mutant which fails an
// earlier row still reaches the rows after it. A table that stopped at the
// first failure would make the fixture's outcome depend on which mutant is
// active, which is the one thing a fixture must not do.
//
// The contents are checked at one byte per block rather than over the whole
// block. Comparing a megabyte three times over would make this suite's own cost
// the thing the per-mutant timeout is derived from, and the byte that is
// checked is the one the block is filled with.
func TestBlocks(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want []byte
	}{
		{"nothing to fill", 0, nil},
		{"one", 1, []byte{1}},
		{"three", 3, []byte{3, 2, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Blocks(c.n)
			if len(got) != len(c.want) {
				t.Errorf("Blocks(%d) returned %d blocks, want %d", c.n, len(got), len(c.want))
				return
			}
			for i, block := range got {
				if len(block) != BlockSize {
					t.Errorf("Blocks(%d) block %d is %d bytes, want %d", c.n, i, len(block), BlockSize)
					continue
				}
				if block[0] != c.want[i] {
					t.Errorf("Blocks(%d) block %d is filled with %d, want %d", c.n, i, block[0], c.want[i])
				}
			}
		})
	}
}
