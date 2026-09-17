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

func TestCoverageBlocksAreComparedFieldByFieldInOrder(t *testing.T) {
	t.Parallel()
	base := CoverageBlock{StartLine: 2, StartColumn: 2, EndLine: 2, EndColumn: 2}
	for _, test := range []struct {
		name  string
		other CoverageBlock
		want  int
	}{
		{name: "the same block", other: base},
		{
			name:  "an earlier start line",
			other: CoverageBlock{StartLine: 1, StartColumn: 9, EndLine: 9, EndColumn: 9}, want: 1,
		},
		{
			name:  "a later start line",
			other: CoverageBlock{StartLine: 3, StartColumn: 1, EndLine: 1, EndColumn: 1}, want: -1,
		},
		{
			name:  "an earlier start column",
			other: CoverageBlock{StartLine: 2, StartColumn: 1, EndLine: 9, EndColumn: 9}, want: 1,
		},
		{
			name:  "a later start column",
			other: CoverageBlock{StartLine: 2, StartColumn: 3, EndLine: 1, EndColumn: 1}, want: -1,
		},
		{
			name:  "an earlier end line",
			other: CoverageBlock{StartLine: 2, StartColumn: 2, EndLine: 1, EndColumn: 9}, want: 1,
		},
		{
			name:  "a later end line",
			other: CoverageBlock{StartLine: 2, StartColumn: 2, EndLine: 3, EndColumn: 1}, want: -1,
		},
		{
			name:  "an earlier end column",
			other: CoverageBlock{StartLine: 2, StartColumn: 2, EndLine: 2, EndColumn: 1}, want: 1,
		},
		{
			name:  "a later end column",
			other: CoverageBlock{StartLine: 2, StartColumn: 2, EndLine: 2, EndColumn: 3}, want: -1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := compareCoverageBlocks(base, test.other); got != test.want {
				t.Fatalf("compareCoverageBlocks(%+v, %+v) = %d, want %d", base, test.other, got, test.want)
			}
			if got := compareCoverageBlocks(test.other, base); got != -test.want {
				t.Fatalf("compareCoverageBlocks(%+v, %+v) = %d, want %d", test.other, base, got, -test.want)
			}
		})
	}
}
