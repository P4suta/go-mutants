// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import "testing"

func TestCompareCoverageBlocksUsesStartBeforeEnd(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		first, second CoverageBlock
	}{
		{
			name:   "start line before contradictory ends",
			first:  CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 20, EndColumn: 9},
			second: CoverageBlock{StartLine: 3, StartColumn: 1, EndLine: 4, EndColumn: 1},
		},
		{
			name:   "start column before contradictory ends",
			first:  CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 20, EndColumn: 9},
			second: CoverageBlock{StartLine: 2, StartColumn: 6, EndLine: 4, EndColumn: 1},
		},
		{
			name:   "end line",
			first:  CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 4, EndColumn: 9},
			second: CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 5, EndColumn: 1},
		},
		{
			name:   "end column",
			first:  CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 4, EndColumn: 8},
			second: CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 4, EndColumn: 9},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := compareCoverageBlocks(test.first, test.second); got >= 0 {
				t.Fatalf("compareCoverageBlocks(first, second) = %d, want negative", got)
			}
			if got := compareCoverageBlocks(test.second, test.first); got <= 0 {
				t.Fatalf("compareCoverageBlocks(second, first) = %d, want positive", got)
			}
		})
	}

	equal := CoverageBlock{StartLine: 2, StartColumn: 5, EndLine: 4, EndColumn: 9}
	if got := compareCoverageBlocks(equal, equal); got != 0 {
		t.Errorf("equal coverage blocks compare as %d", got)
	}
}

func TestParseCoverageSpanRefusesEveryHalfItCannotRead(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		span string
		want bool
	}{
		{name: "a span of two positions", span: "10.2,12.16", want: true},
		{name: "a span that ends where it starts", span: "10.2,10.2", want: true},
		{name: "a span with no comma", span: "10.2"},
		{name: "a start with no column", span: "10,12.16"},
		{name: "an end with no column", span: "10.2,12"},
		{name: "a start on no line", span: "x.2,12.16"},
		{name: "an end on no line", span: "10.2,x.16"},
		{name: "a start on line zero", span: "0.2,12.16"},
		{name: "an end that closes on an earlier line", span: "12.2,10.16"},
		{name: "an end that closes in an earlier column", span: "10.4,10.2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			block, ok := parseCoverageSpan(test.span)
			if ok != test.want {
				t.Fatalf("parseCoverageSpan(%q) = (%+v, %t), want %t", test.span, block, ok, test.want)
			}
			if !ok && block != (CoverageBlock{}) {
				t.Errorf("a span it refused answered with %+v, want no block at all", block)
			}
		})
	}
}
