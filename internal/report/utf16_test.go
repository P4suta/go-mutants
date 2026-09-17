// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
)

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

func TestUTF16PositionCountsBytesNeitherAsRunesNorAsBytes(t *testing.T) {
	t.Parallel()

	src := []byte(utf16Source)
	got := report.UTF16Position(src, 8)
	if got.Column != 6 {
		t.Errorf("column after \"a¥b🎉\" = %d, want 6 (a byte count would say 9, a rune count 5)", got.Column)
	}
}

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

func TestUTF16OffsetAtRoundTrips(t *testing.T) {
	t.Parallel()

	src := []byte("package p\n\nconst ¥ = \"🎉\"\nvar x = 1\n")
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

func TestUTF16PositionOnALongFile(t *testing.T) {
	t.Parallel()

	const lines = 5000
	src := []byte(strings.Repeat("x🎉\n", lines))
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

func TestUTF16OffsetAtClampsALineTheFileDoesNotHave(t *testing.T) {
	t.Parallel()

	src := []byte("package p\nvar x = 1\n")
	lastLineStart := len(src)
	for _, line := range []int{3, 4, 99, 1 << 20} {
		if got := report.UTF16OffsetAt(src, line, 1); got != lastLineStart {
			t.Errorf("UTF16OffsetAt(line %d) = %d, want %d — the last line's start", line, got, lastLineStart)
		}
	}
	for _, line := range []int{0, -1, -1 << 20} {
		if got := report.UTF16OffsetAt(src, line, 1); got != 0 {
			t.Errorf("UTF16OffsetAt(line %d) = %d, want 0 — the first line's start", line, got)
		}
	}
	if got := report.UTF16OffsetAt(src, 1, 1<<20); got != len(src) {
		t.Errorf("UTF16OffsetAt(column past the end) = %d, want %d", got, len(src))
	}
}

func TestUTF16CountsTheLastBasicMultilingualPlaneRune(t *testing.T) {
	t.Parallel()

	for s, want := range map[string]int{
		"\uFFFE":     1,
		"\uFFFF":     1,
		"\U00010000": 2,
		"\U0001F389": 2,
	} {
		if got := report.UTF16Units(s); got != want {
			t.Errorf("UTF16Units(%+q) = %d, want %d", s, got, want)
		}
	}
}
