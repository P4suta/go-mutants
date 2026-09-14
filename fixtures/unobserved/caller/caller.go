// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package caller is the second test binary, and the only reason [core.Gate]'s
// mutant is covered by two of them.
//
// It holds nothing mutable of its own, so the catalogue is package core's
// alone and this package's job is to be a binary that reaches a line without
// being able to see what a mutant would do to it.
package caller

import "fixture.example/unobserved/core"

// Below asks core whether v is under lo.
func Below(v, lo int) bool {
	return core.Gate(v, lo)
}
