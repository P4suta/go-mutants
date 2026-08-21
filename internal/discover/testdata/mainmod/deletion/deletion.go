// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package deletion holds one live example of every statement-deletion rule,
// and the one call the family refuses to delete.
package deletion

// Log is the call this fixture deletes. Its body is empty on purpose: what is
// interesting is the statement that calls it, not what it does.
func Log(string) {}

// Record holds a deletable call, a deletable assignment, and a deletable
// increment, in that order.
func Record(n int, m map[string]int, key string, out []int) {
	Log("start")
	total := 0
	total = total + n
	total++
	m[key] = n
	out[0] = total
}

// Grow keeps the append assignment the family is remembered for: deleting it
// is the classic equivalent-mutant trap in code that accumulates.
func Grow(xs []int, n int) []int {
	xs = append(xs, n)
	return xs
}

// MustPositive is the exclusion. Deleting the panic leaves a path that reaches
// the closing brace without returning, so the file stops compiling — and it
// stops compiling in exactly the defensive code where a deleted call would
// otherwise have been an interesting mutant. It is not a recorded skip: a
// terminating panic is never a candidate, the way a test file is never
// mutated.
func MustPositive(n int) int {
	if n < 0 {
		panic("negative")
	}
	return n
}

// MustNeverHappen is that same exclusion written the one way a parenthesis
// hides it. `(panic)(reason)` is legal Go and it terminates the function just
// as `panic(reason)` does: this function promises an int and never returns one,
// which compiles only because the call is terminating. Deleting it would leave
// the closing brace reachable without a return, so the exclusion has to see
// through the parenthesis to hold at all.
func MustNeverHappen(reason string) int {
	(panic)(reason)
}
