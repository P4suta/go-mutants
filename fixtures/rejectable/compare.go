// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package rejectable

// InRange reports whether v lies within the inclusive range [lo, hi].
//
// Five healthy candidates in one function — two comparisons and three boolean
// literals — and they are here to be counted. A file with one trap and one
// healthy candidate cannot tell a bisection from a coin toss; this file holds
// six candidates so that isolating the single bad one takes real halving, and
// so that a bug which threw away the other half of a split would leave four
// accepted mutants missing rather than one.
func InRange(v, lo, hi int) bool {
	if v < lo {
		return false
	}
	if v > hi {
		return false
	}
	return true
}

// Matches reports whether a and b are the same string.
//
// TRAP, and the reason this file has one at all: the file the bisection has to
// work hardest on is also the file that must come out of it with every healthy
// mutant intact. A comparison of two strings is untyped-boolean-valued exactly
// as an integer comparison is, so this is the [Flag] trap again in a file where
// it is outnumbered five to one.
func Matches(a, b string) Flag {
	return a == b
}
