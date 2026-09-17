// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package labels holds one live candidate for each labeled-branch rule, the
// two shapes the family refuses, and the one statement it records a reason for.
//
// Every function here is contrived, and deliberately so: a labelled branch is
// rare in real Go, and the point of this package is that each of the five
// answers the family can give is reachable and is reached by something a reader
// can point at.
package labels

// Find leaves an outer loop from inside an inner one.
//
// The label is load-bearing: a bare `break` would leave the inner range and go
// on scanning the next row, which is a different program. That is the mutant.
func Find(rows [][]int, want int) bool {
outer:
	for _, row := range rows {
		for _, v := range row {
			if v == want {
				break outer
			}
		}
	}
	return false
}

// Skip continues an outer loop from inside an inner one, which is the other
// rule and the same argument.
func Skip(rows [][]int, bad int) int {
	total := 0
outer:
	for _, row := range rows {
		for _, v := range row {
			if v == bad {
				continue outer
			}
			total += v
		}
	}
	return total
}

// Split is the one function that separates the two rules, and it is the reason
// there are two.
//
// A `switch` is breakable and not continuable. So `break loop` here is a real
// mutant — a bare `break` would leave the switch and carry on round the loop —
// and `continue loop` at the same depth is equivalent, because `continue` was
// never going to bind to the switch. One candidate comes out of this function,
// not two.
func Split(values []int) int {
	total := 0
loop:
	for _, v := range values {
		switch {
		case v < 0:
			break loop
		case v == 0:
			continue loop
		default:
			total += v
		}
	}
	return total
}

// Only labels the loop the branch would bind to anyway, which makes both
// statements the same program token for token. No candidate and no skip: there
// is nothing here to decline.
func Only(values []int, want int) bool {
only:
	for _, v := range values {
		if v == want {
			break only
		}
	}
	return false
}

// Retry is the `goto`, which is where the reserved `label-or-goto` reason
// becomes a fact. Retargeting it is not stable — Go forbids jumping over a
// declaration or into a block — and removing it would leave this function
// reaching its closing brace without returning.
func Retry(attempts int) int {
	n := 0
again:
	n++
	if n < attempts {
		goto again
	}
	return n
}

// Cascade is the `fallthrough`, which is neither mutated nor recorded. It has
// to be the final statement of a case clause, so no guard form can wrap it and
// there is no edit to decline — unlike the `goto` above, which could be edited
// and is refused with an argument.
func Cascade(n int) string {
	out := ""
	switch n {
	case 2:
		out += "high"
		fallthrough
	case 1:
		out += "low"
	}
	return out
}
