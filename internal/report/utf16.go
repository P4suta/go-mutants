// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import "sort"

// This file converts go-mutants' coordinates into the ones the
// mutation-testing-report format uses, and the conversion is the whole of the
// difference between a report a viewer highlights correctly and one that
// highlights the wrong half of a line.
//
// go-mutants locates a mutant by a half-open byte range into the file, because
// it splices bytes: see [mutation.Span]. The published format locates one by a
// half-open pair of (line, column) positions, both 1-based — and its column is
// counted in **UTF-16 code units**, because the viewer is JavaScript and a
// JavaScript string index is a UTF-16 code unit index. The three counts differ
// the moment a line is not pure ASCII:
//
//	"a¥b"   bytes 1,2,1   runes 1,1,1   UTF-16 units 1,1,1
//	"a🎉b"  bytes 1,4,1   runes 1,1,1   UTF-16 units 1,2,1
//
// A projection that handed over byte columns would place every mutant after a
// multi-byte rune too far right, and one that handed over rune columns would
// place every mutant after an astral character — an emoji in a test name, a
// mathematical symbol in a comment — too far left. Both are silently valid
// against the schema, which only requires the numbers to be at least 1.
//
// CRLF needs no special case and deliberately gets none. A line ends at its
// '\n'; the '\r' before it is an ordinary character on the line it terminates,
// which is exactly how a JavaScript viewer that split the same source on '\n'
// would count it.

// A Position is a 1-based line and a 1-based UTF-16 column, as the published
// format spells one.
type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// A Location is a half-open range of positions: start inclusive, end exclusive.
type Location struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// A sourceIndex answers positional questions about one file's bytes.
//
// It is built once per file and queried once per mutant, rather than walking
// the file from the top for every coordinate: a large file with many mutants
// would otherwise cost O(file × mutants), which is quadratic in exactly the
// files a mutation report is most useful for.
type sourceIndex struct {
	// src is the file's bytes, held as a string so that ranging over it yields
	// runes without copying.
	src string
	// lineStarts holds the byte offset of the first byte of each line, so
	// lineStarts[0] is always 0 and the slice has one entry per line.
	lineStarts []int
}

// newSourceIndex indexes one file's bytes.
func newSourceIndex(src []byte) *sourceIndex {
	text := string(src)
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return &sourceIndex{src: text, lineStarts: starts}
}

// size is the file's length in bytes.
func (x *sourceIndex) size() int { return len(x.src) }

// position converts a byte offset into the published coordinate.
//
// An offset past the end of the file is clamped to the end rather than
// refused: this is the last step of a report, the caller has already checked
// that the span matches the source it came from, and a clamp produces a
// coordinate that is merely imprecise where a panic would lose the whole run's
// results. An offset in the middle of a multi-byte rune counts that rune as
// passed, which is the only answer available and cannot arise from a span
// go-mutants minted.
func (x *sourceIndex) position(offset int) Position {
	if offset < 0 {
		offset = 0
	}
	if offset > len(x.src) {
		offset = len(x.src)
	}
	line := x.lineAt(offset)
	start := x.lineStarts[line]
	return Position{Line: line + 1, Column: 1 + utf16Units(x.src[start:offset])}
}

// lineAt returns the 0-based index of the line containing a byte offset.
func (x *sourceIndex) lineAt(offset int) int {
	// The first line whose start is greater than the offset begins after it,
	// so the line before that one contains it. Offset 0 always lands on line 0
	// because lineStarts[0] is 0 and the search cannot return 0 for it.
	next := sort.SearchInts(x.lineStarts, offset+1)
	if next <= 0 {
		return 0
	}
	return next - 1
}

// offsetAt converts the 1-based line and 1-based *byte* column go-mutants
// records elsewhere — a rejected mutant carries no span, only the coordinate
// discovery printed — back into a byte offset.
//
// Out-of-range input is clamped rather than refused, for the reason [position]
// clamps: a document that is about to be written is not the place to discover
// that a line number is one past the end of a file somebody edited during the
// run.
func (x *sourceIndex) offsetAt(line, byteColumn int) int {
	index := line - 1
	if index < 0 {
		index = 0
	}
	if index >= len(x.lineStarts) {
		index = len(x.lineStarts) - 1
	}
	offset := x.lineStarts[index] + max(byteColumn-1, 0)
	if offset > len(x.src) {
		offset = len(x.src)
	}
	return offset
}

// utf16Units counts the UTF-16 code units in s.
//
// It counts rather than encodes: the standard library's utf16.Encode would
// allocate a slice as long as the line to produce a number [utf16Length]
// derives from each rune's value alone.
func utf16Units(s string) int {
	units := 0
	for _, r := range s {
		units += utf16Length(r)
	}
	return units
}

// utf16Length is how many UTF-16 code units one rune occupies, and the single
// rule this file counts by.
//
// A rune outside the Basic Multilingual Plane — an emoji, a rare CJK
// ideograph, a musical symbol — is encoded as a surrogate pair and occupies
// two. Everything else occupies one, the U+FFFD an invalid byte decodes to
// included: ranging over a Go string never yields a lone surrogate, so an
// unpaired one is not a case that can arrive here.
func utf16Length(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}
