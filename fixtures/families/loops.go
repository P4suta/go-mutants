// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

// Steps counts the iterations a bounded loop makes, up to a cap of its own.
//
// KILLED. It carries `negate-loop-condition`, which is also the reason
// TestSteps must never pass a limit of zero or below: `!(i < limit && steps <
// 64)` is true at `i == 0` when the limit is not positive, the body runs, and
// the post statement drives `i` further from the bound on every pass. With a
// positive limit the same mutant skips the loop and settles instantly.
//
// # Two bounds, because one is not enough any more
//
// The cap is not decoration and the loop is not contrived for its own sake. A
// counted loop has exactly one thing keeping it finite, and *every* edit to
// that one thing is a loop that never ends: reverse the step and `i` runs away
// from the bound, delete the step and it never reaches it. Those two edits used
// to be unreachable — a `for` post statement was a position no guard form could
// express — and Form F reaches them now.
//
// So a single-bound counted loop cannot satisfy this package's promise that
// every loop here terminates under every mutant of it. The promise is not
// weakened; the loop is given a second bound instead. `steps < 64` is advanced
// by the body and `i < limit` by the post statement, so no *single* edit can
// remove both, and every mutant is applied alone. Reverse or delete the post
// statement and the cap stops it; reverse or delete `steps++` and the count
// stops it; turn the `&&` into `||` and both still advance.
//
// Nothing in this file is refused any more. The `i++` post statement was the
// package's only pair of `unnameable-decl-type` skips and is now a pair of
// mutants; the `i := 0` initialiser beside it never was one, because `0` is an
// integer literal and no rule proposes an edit there. A refusal appearing here
// would mean a guard form had begun swallowing sites -- which is what the
// engine's integration suite asserts, against an empty list.
func Steps(limit int) int {
	steps := 0
	for i := 0; i < limit && steps < 64; i++ {
		steps++
	}
	return steps
}

// Remaining is the stock left after taken units are removed one at a time.
//
// KILLED. `for range taken` has no condition for any rule to rewrite, so the
// number of passes is fixed whatever happens to the body — which is what lets
// the body carry the decrement half of the arithmetic-assignment family, and
// the deletion of it, with no way for either mutant to turn the loop into one
// that does not stop.
func Remaining(stock, taken int) int {
	for range taken {
		stock--
	}
	return stock
}

// Net is credits less debits.
//
// KILLED. Both compound assignments of the arithmetic-assignment family are
// here, each inside a range loop whose length is a fact about its slice rather
// than about anything a rule can reach.
func Net(credits, debits []int) int {
	total := 0
	for _, c := range credits {
		total += c
	}
	for _, d := range debits {
		total -= d
	}
	return total
}

// Drift is the fixture's deliberately under-tested accumulator.
//
// SURVIVES, both of its mutants. TestDrift accumulates a slice of zeros: the
// loop body really runs, so the compound assignment is covered and executed,
// and adding zero and subtracting zero come to the same thing. The total it
// returns is then the zero `return-zero-numeric` would have returned anyway.
// A single non-zero element would kill both.
func Drift(steps []int) int {
	total := 0
	for _, s := range steps {
		total += s
	}
	return total
}
