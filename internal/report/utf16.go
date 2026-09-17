// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import "sort"

type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type Location struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type sourceIndex struct {
	src        string
	lineStarts []int
}

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

func (x *sourceIndex) size() int { return len(x.src) }

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

func (x *sourceIndex) lineAt(offset int) int {
	next := sort.SearchInts(x.lineStarts, offset+1)
	if next <= 0 {
		return 0
	}
	return next - 1
}

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

func utf16Units(s string) int {
	units := 0
	for _, r := range s {
		units += utf16Length(r)
	}
	return units
}

func utf16Length(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}
