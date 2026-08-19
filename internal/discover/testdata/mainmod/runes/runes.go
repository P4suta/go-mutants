// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package runes puts multi-byte characters in front of a candidate on its own
// line, which is the only fixture here where a candidate's byte column, its
// rune column, and its display column are three different numbers.
//
// [Located.Column] is documented as the byte offset, and that is not an
// accident: every `file:line:col` consumer downstream — an editor, a
// `::warning file=` annotation, a jump-to-mutant — counts bytes.
package runes

// Arrow reports how the two compare. The characters ahead of each operator are
// there to move its byte column away from its rune column, and nothing else.
func Arrow(a, b int) string {
	if label := "α → ω"; a > b {
		return label
	}
	if label := "∞ ≤ 0"; a < b {
		return label
	}
	return "…"
}
