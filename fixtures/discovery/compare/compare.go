// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package compare holds one live candidate for every rule the discovery phase
// implements, every one of them in ordinary statement context so that nothing
// here is suppressed for any reason.
//
// It is the control group of this fixture: the packages beside it exist to
// prove that discovery refuses the positions it says it refuses, and a run
// that refused everything would satisfy those without finding a single mutant.
package compare

// Classify exercises all six comparison rules, one per statement so that a
// missing candidate names the line it went missing on.
func Classify(a, b int) string {
	if a == b {
		return "eq"
	}
	if a != b {
		return "ne"
	}
	if a < b {
		return "lt"
	}
	if a <= b {
		return "le"
	}
	if a > b {
		return "gt"
	}
	if a >= b {
		return "ge"
	}
	return "none"
}

// Flags exercises both boolean-literal rules.
func Flags() (bool, bool) {
	on := true
	off := false
	return on, off
}

// Indexed proves that a boolean literal used as a map key is value code: the
// type-argument suppression must not reach an ordinary index expression, which
// is the same syntax node as an explicit instantiation.
func Indexed(m map[bool]int) int {
	return m[true]
}
