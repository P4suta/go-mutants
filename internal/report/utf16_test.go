// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
)

// The conversion from byte offsets to UTF-16 positions is the one piece of
// arithmetic in the projection that can be wrong without anything noticing:
// the published schema requires a line and a column to be at least 1 and
// nothing more, so an off-by-a-multibyte-rune document validates, renders, and
// highlights the wrong half of a line.
//
// So it is pinned as a table over the three cases that differ, plus the one
// file shape that looks like it should need a special case and must not.

// The single-rune widths every count in this file is built from.
func TestUTF16UnitsPerRune(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		text string
		want int
	}{
		"ascii":                {text: "a", want: 1},
		"latin-1 supplement":   {text: "é", want: 1},
		"currency sign":        {text: "¥", want: 1},
		"cjk ideograph":        {text: "漢", want: 1},
		"astral emoji":         {text: "🎉", want: 2},
		"astral mathematical":  {text: "𝛼", want: 2},
		"combining sequence":   {text: "é", want: 2},
		"emoji then ascii":     {text: "🎉x", want: 3},
		"replacement of a bad": {text: string([]byte{0xff}), want: 1},
		"empty":                {text: "", want: 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := report.UTF16Units(tc.text); got != tc.want {
				t.Errorf("UTF16Units(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

// utf16Source mixes every width on one line and then starts a second, so that
// a conversion that forgot to reset the column at a newline and one that
// counted bytes are both visible in the same table.
//
//	byte  0        1  2   3   4     5  6  7     8   9  10 11
//	rune  a        ¥      b         🎉             c
//	utf16 1        2      3         4  5          6
const utf16Source = "a¥b🎉c\nz\n"

func TestUTF16PositionTable(t *testing.T) {
	t.Parallel()

	src := []byte(utf16Source)
	for name, tc := range map[string]struct {
		offset int
		line   int
		column int
	}{
		"start of file":              {offset: 0, line: 1, column: 1},
		"after one ascii byte":       {offset: 1, line: 1, column: 2},
		"after a two-byte rune":      {offset: 3, line: 1, column: 3},
		"after the ascii after it":   {offset: 4, line: 1, column: 4},
		"after a four-byte emoji":    {offset: 8, line: 1, column: 6},
		"at the newline":             {offset: 9, line: 1, column: 7},
		"start of the second line":   {offset: 10, line: 2, column: 1},
		"end of the second line":     {offset: 11, line: 2, column: 2},
		"end of file":                {offset: 12, line: 3, column: 1},
		"past the end is clamped":    {offset: 9999, line: 3, column: 1},
		"before the start is zeroed": {offset: -3, line: 1, column: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := report.UTF16Position(src, tc.offset)
			if got.Line != tc.line || got.Column != tc.column {
				t.Errorf("UTF16Position(offset %d) = %d:%d, want %d:%d",
					tc.offset, got.Line, got.Column, tc.line, tc.column)
			}
		})
	}
}

// TestUTF16PositionCountsBytesNeitherAsRunesNorAsBytes is the assertion the
// table above exists to make, stated once on its own so that a future reader
// sees the three numbers side by side.
func TestUTF16PositionCountsBytesNeitherAsRunesNorAsBytes(t *testing.T) {
	t.Parallel()

	src := []byte(utf16Source)
	// The offset just past "a¥b🎉": 1 + 2 + 1 + 4 = 8 bytes, 4 runes, 5 UTF-16
	// code units — so the column is 6, and a byte count would say 9 and a rune
	// count 5.
	got := report.UTF16Position(src, 8)
	if got.Column != 6 {
		t.Errorf("column after \"a¥b🎉\" = %d, want 6 (a byte count would say 9, a rune count 5)", got.Column)
	}
}

// TestUTF16PositionOnCRLF pins that a carriage return is an ordinary character
// on the line it terminates and never a line of its own.
//
// A viewer that split the same source on '\n' counts it exactly this way, so
// anything cleverer here would put the report and the page it is rendered in
// out of step on every file written on Windows — which, in a project whose
// .gitattributes exists because line endings change mutant identities, is not a
// hypothetical file.
func TestUTF16PositionOnCRLF(t *testing.T) {
	t.Parallel()

	src := []byte("ab\r\ncd\r\n")
	for name, tc := range map[string]struct {
		offset int
		line   int
		column int
	}{
		"before the carriage return": {offset: 2, line: 1, column: 3},
		"at the newline":             {offset: 3, line: 1, column: 4},
		"first byte of line two":     {offset: 4, line: 2, column: 1},
		"end of file":                {offset: 8, line: 3, column: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := report.UTF16Position(src, tc.offset)
			if got.Line != tc.line || got.Column != tc.column {
				t.Errorf("UTF16Position(offset %d) = %d:%d, want %d:%d",
					tc.offset, got.Line, got.Column, tc.line, tc.column)
			}
		})
	}
}

// TestUTF16OffsetAtRoundTrips proves the reverse conversion — the one a
// rejected mutant's (line, byte column) has to make — lands where the forward
// one started, for every offset in a file with all three widths in it.
func TestUTF16OffsetAtRoundTrips(t *testing.T) {
	t.Parallel()

	src := []byte("package p\n\nconst ¥ = \"🎉\"\nvar x = 1\n")
	// Byte columns are what discovery records, so the round trip is
	// offset -> (line, byte column) -> offset, and it must be the identity on
	// every rune boundary.
	line, column := 1, 1
	for offset := 0; offset < len(src); offset++ {
		if got := report.UTF16OffsetAt(src, line, column); got != offset {
			t.Fatalf("UTF16OffsetAt(%d, %d) = %d, want %d", line, column, got, offset)
		}
		if src[offset] == '\n' {
			line, column = line+1, 1
			continue
		}
		column++
	}
}

// TestUTF16PositionOnALongFile is the performance property stated as a
// correctness one: the index is built once and every lookup is a search, so a
// file with many lines and many mutants stays linear rather than quadratic. The
// assertion is on the answers, since a wrong answer is the way an "optimised"
// lookup usually breaks.
func TestUTF16PositionOnALongFile(t *testing.T) {
	t.Parallel()

	const lines = 5000
	src := []byte(strings.Repeat("x🎉\n", lines))
	// Each line is 1 + 4 + 1 = 6 bytes and 4 UTF-16 code units of text.
	for _, n := range []int{0, 1, 17, 2499, lines - 1} {
		offset := n * 6
		got := report.UTF16Position(src, offset)
		if got.Line != n+1 || got.Column != 1 {
			t.Errorf("UTF16Position(offset %d) = %d:%d, want %d:1", offset, got.Line, got.Column, n+1)
		}
		if end := report.UTF16Position(src, offset+5); end.Line != n+1 || end.Column != 4 {
			t.Errorf("end of line %d = %d:%d, want %d:4", n+1, end.Line, end.Column, n+1)
		}
	}
}
