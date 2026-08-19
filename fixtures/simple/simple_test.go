// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package simple

import "testing"

func TestClamp(t *testing.T) {
	cases := []struct {
		name            string
		v, lo, hi, want int
	}{
		{"below", -5, 0, 10, 0},
		{"at the low bound", 0, 0, 10, 0},
		{"inside", 4, 0, 10, 4},
		{"at the high bound", 10, 0, 10, 10},
		{"above", 40, 0, 10, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Clamp(c.v, c.lo, c.hi); got != c.want {
				t.Errorf("Clamp(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
			}
		})
	}
}

func TestSum(t *testing.T) {
	cases := []struct {
		name   string
		values []int
		want   int
	}{
		{"empty", nil, 0},
		{"one", []int{7}, 7},
		{"several", []int{1, 2, 3, 4}, 10},
		{"negatives", []int{5, -2, -1}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Sum(c.values); got != c.want {
				t.Errorf("Sum(%v) = %d, want %d", c.values, got, c.want)
			}
		})
	}
}

func TestInRange(t *testing.T) {
	cases := []struct {
		name      string
		v, lo, hi int
		want      bool
	}{
		{"below", -1, 0, 10, false},
		{"at the low bound", 0, 0, 10, true},
		{"inside", 5, 0, 10, true},
		{"at the high bound", 10, 0, 10, true},
		{"above", 11, 0, 10, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := InRange(c.v, c.lo, c.hi); got != c.want {
				t.Errorf("InRange(%d, %d, %d) = %t, want %t", c.v, c.lo, c.hi, got, c.want)
			}
		})
	}
}
