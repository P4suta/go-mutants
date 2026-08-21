// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

// Steps counts the iterations a bounded loop makes.
//
// KILLED. It carries `negate-loop-condition`, which is also the reason
// TestSteps must never pass a limit of zero or below: `!(i < limit)` is true at
// `i == 0` when the limit is not positive, the body runs, and the post
// statement drives `i` further from the bound on every pass — a loop that never
// ends and a mutant reported as a timeout five times the baseline later. With a
// positive limit the same mutant skips the loop and settles instantly.
//
// The `i := 0` initialiser and the `i++` post statement are positions where a
// block is not legal Go, so the candidates on them are recorded as
// `unnameable-decl-type` skips instead of being catalogued. The `steps++` in
// the body is an ordinary statement and carries both of its rules.
func Steps(limit int) int {
	steps := 0
	for i := 0; i < limit; i++ {
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
