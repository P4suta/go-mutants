// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package assign holds one live example of every arithmetic-assignment rule.
package assign

// Counters exercises `+=`, `-=`, `++`, and `--` over an integer, and `+=` over
// a float: the family's gate is "integer or float underneath", because those
// are the types the operators mean arithmetic for.
func Counters(n int, f float64, out []int) {
	n += 1
	n -= 2
	n++
	n--
	f += 0.5
	out[0] = n
	out[1] = int(f)
}

// Text is the exclusion. `+=` on a string concatenates, and the family may no
// more claim it than the integer family may claim `+` between two strings.
func Text(s string, out []string) {
	s += "!"
	out[0] = s
}
