// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package compare holds one live example of every rule the discovery phase
// implements, all of them in ordinary statement context so that nothing here
// is suppressed for any reason.
package compare

// Classify exercises all six comparison rules.
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
// type-argument suppression must not reach an ordinary index expression.
func Indexed(m map[bool]int) int {
	return m[true]
}
