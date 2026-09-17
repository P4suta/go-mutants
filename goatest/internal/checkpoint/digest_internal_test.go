// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package checkpoint

import (
	"strings"
	"testing"
)

const hexDigitRuns = 4

func TestACheckpointDigestIsSixtyFourLowercaseHexDigits(t *testing.T) {
	t.Parallel()
	full := strings.Repeat("0123456789abcdef", hexDigitRuns)
	for _, test := range []struct {
		name  string
		input string
		want  bool
	}{
		{name: "every digit and letter it admits", input: full, want: true},
		{name: "the lowest digest", input: strings.Repeat("0", len(full)), want: true},
		{name: "the highest digest", input: strings.Repeat("f", len(full)), want: true},
		{name: "nothing at all"},
		{name: "one character short", input: full[:len(full)-1]},
		{name: "one character long", input: full + "0"},
		{name: "the same digits in capitals", input: strings.ToUpper(full)},
		{name: "a letter past f", input: full[:len(full)-1] + "g"},
		{name: "the character below zero", input: full[:len(full)-1] + "/"},
		{name: "the character above nine", input: full[:len(full)-1] + ":"},
		{name: "the character below a", input: full[:len(full)-1] + "`"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := validSHA256(test.input); got != test.want {
				t.Fatalf("validSHA256(%q) = %t, want %t", test.input, got, test.want)
			}
		})
	}
}

func TestStrictlyIncreasingHoldsOnlyForAscendingDistinctValues(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		values []uint32
		want   bool
	}{
		{name: "nothing at all", want: true},
		{name: "one value", values: []uint32{7}, want: true},
		{name: "two ascending values", values: []uint32{1, 2}, want: true},
		{name: "two equal values", values: []uint32{2, 2}},
		{name: "two descending values", values: []uint32{2, 1}},
		{name: "an ascending run that repeats at the end", values: []uint32{1, 2, 3, 3}},
		{name: "an ascending run that dips at the end", values: []uint32{1, 2, 3, 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := strictlyIncreasing(test.values); got != test.want {
				t.Fatalf("strictlyIncreasing(%v) = %t, want %t", test.values, got, test.want)
			}
		})
	}
}
