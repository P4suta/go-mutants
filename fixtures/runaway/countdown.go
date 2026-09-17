// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package runaway is the fixture a mutant that does not return is proved
// against.
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
//     true and the process allocates until something stops it. What stops it is
//     the count of the loop's own work: [ADR 0013] holds it to
//     `max(1<<20, observed × 1024)` turns and settles it there, in about a
//     tenth of a second.
//
//     This used to say the memory bound was what stopped it, and that was true
//     when it was written. Eight bytes a turn reaches eight megabytes in the
//     million turns the ceiling allows, and the bound the memory tests set is
//     two hundred and fifty-six — so once counting landed, the bound was never
//     reached here again and the three tests pointing at this fixture saw zero
//     mutants killed by it. They point at `memorybound` now, which is this
//     fixture with a megabyte a turn instead of eight bytes.
//
//   - `gt-to-ge` on the same comparison is the control. It runs one iteration
//     too many and is caught by an assertion, which is what proves the runaway
//     above was settled by the ceiling and not by the suite going red for some
//     unrelated reason.
//
//   - `return-nil` on the result is the second control, and it is why the table
//     compares with slices.Equal: nil and an empty slice are equal there, so a
//     count of nothing does not settle it and the rows that count something do.
//
// The countdown decrements with `--` rather than with `remaining - 1`
// deliberately. `sub-to-add` on the latter is a second runaway — the counter
// would climb away from zero forever — and one runaway is what this fixture is
// for. A second would double what the ceiling has to be shown stopping and
// prove nothing the first does not.
//
// [ADR 0013]: https://github.com/P4suta/go-mutants/blob/main/docs/adr/0013-a-mutant-that-does-not-return-is-decided-by-work.md
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
