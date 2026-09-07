// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build special

package tagged

// Special is the candidate a run only sees when the `special` build tag is on.
//
// It returns `false` rather than `true` so that the two candidates in this
// module are mutated by different rules — `false-to-true` here and
// `true-to-false` in plain.go — which is what lets a catalogue of two be
// checked by rule as well as by count.
func Special() bool {
	return false
}
