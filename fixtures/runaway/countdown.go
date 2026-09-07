// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package runaway is the fixture the memory bound is proved against.
//
// It holds one function whose loop condition, when a mutant negates it, makes
// the loop run forever appending to a slice. That is not a contrived shape: it
// is exactly what happened to this repository's own `internal/config` under
// `negate-loop-condition`, where a mutant of `for parser.NextExpression()`
// reached eleven gigabytes in twelve seconds and took a CI runner down before
// the per-mutant timeout — a number derived from a suite that finishes in
// milliseconds — could expire.
//
// The three balanced-tier mutants here have predetermined fates and each one is
// needed:
//
//   - `negate-loop-condition` on the loop is the runaway. The negated condition
//     is false for every n the loop would have run for, so it is a *survivor*
//     of the interesting kind — until [TestCountdown] reaches the row where
//     nothing is to be counted, at which point the condition is true and stays
//     true and the process allocates until something stops it. The memory bound
//     is what stops it.
//   - `gt-to-ge` on the same comparison is the control. It runs one iteration
//     too many and is caught by an assertion, which is what proves the runaway
//     above was killed by the bound and not by the suite going red for some
//     unrelated reason.
//   - `return-nil` on the result is the second control, and it is why the table
//     compares with slices.Equal: nil and an empty slice are equal there, so a
//     count of nothing does not settle it and the rows that count something do.
//
// The countdown decrements with `--` rather than with `remaining - 1`
// deliberately. `sub-to-add` on the latter is a second runaway — the counter
// would climb away from zero forever — and one runaway is what this fixture is
// for. A second would double what the bound has to be shown stopping and prove
// nothing the first does not.
package runaway

// Countdown returns n, n-1, … 1, and an empty slice for a non-positive n.
func Countdown(n int) []int {
	out := []int{}
	remaining := n
	for remaining > 0 {
		out = append(out, remaining)
		remaining--
	}
	return out
}
