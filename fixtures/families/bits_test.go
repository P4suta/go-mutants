// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

import "testing"

// TestMask uses two masks that share no bits, which is what makes all four
// bitwise swaps observable at once.
//
// 0b1100 and 0b0011 give 0, 15, 15, and 12 for the four operations, and every
// swap moves at least one of those four numbers, so the sum moves too. Masks
// that overlapped would make `&` and `|` agree on some bits and cost the
// fixture a kill it claims.
func TestMask(t *testing.T) {
	if got := Mask(0b1100, 0b0011); got != 42 {
		t.Errorf("Mask(0b1100, 0b0011) = %d, want 42", got)
	}
}

func TestShift(t *testing.T) {
	if got := Shift(0b0011_0000, 2); got != 204 {
		t.Errorf("Shift(0b00110000, 2) = %d, want 204", got)
	}
}

// TestSalt is the fixture's under-tested bit twiddling, and the omission is the
// point. It calls Salt so that the line is covered and its mutants are really
// executed, and then throws the answer away.
func TestSalt(t *testing.T) {
	Salt(0b1010_1010, 2)
}
