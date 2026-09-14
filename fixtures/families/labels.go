// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

// Search counts how many values it looked at before it found one.
//
// KILLED. `drop-break-label` is the first half of the labeled-branch family,
// and this function exists to make the label observable: with it, the search
// stops the moment it matches; without it, a bare `break` leaves only the inner
// loop and the scan carries on through the next row. A function that merely
// reported *whether* it found the value would be the same program either way,
// which is the trap this family's structural gate cannot catch — the gate knows
// that the label names a construct the bare form would not bind to, and it
// cannot know that nobody would notice.
//
// Both loops are `for range`, which has no condition to negate, so this
// function keeps the fixture's promise that every loop here terminates under
// every mutant of it.
func Search(rows [][]int, want int) int {
	scanned := 0
outer:
	for _, row := range rows {
		for _, v := range row {
			scanned++
			if v == want {
				break outer
			}
		}
	}
	return scanned
}

// Tally sums every value except the one it is told to skip, and abandons the
// rest of a row when it meets it.
//
// KILLED. `drop-continue-label` is the other half. The label matters only when
// the skipped value is not the last of its row, which is exactly what
// [TestTally] hands it: with the label the rest of that row is abandoned, and
// without it only the one value is.
func Tally(rows [][]int, skip int) int {
	total := 0
outer:
	for _, row := range rows {
		for _, v := range row {
			if v == skip {
				continue outer
			}
			total += v
		}
	}
	return total
}
