// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The two spellings of one outcome, which this audit compared as strings until
// something ran it.
package traceaudit

import "testing"

// TestOneOutcomeIsOneOutcomeInEitherSpelling pins what the audit joins across.
//
// docs/library.md's "The outcome vocabulary is not the report's" says the live
// API is snake_case and the published report is kebab-case, that the difference
// is deliberate, and that both spellings are frozen. A recording carries the
// API's spelling and a report carries the published one, so an audit reading
// both has to join them.
//
// It did not. Every timed-out mutant was a disagreement, and the first run of
// this package anywhere reported three violations on a run whose two documents
// agreed about everything. The table below is the half that matters: the pairs
// that differ only in the separator are one outcome, and the pairs that name
// different outcomes are two.
func TestOneOutcomeIsOneOutcomeInEitherSpelling(t *testing.T) {
	t.Parallel()

	same := [][2]string{
		{"timed-out", "timed_out"},
		{"timed_out", "timed-out"},
		{"not-run", "not_run"},
		{"killed", "killed"},
		{"survived", "survived"},
		{"inconclusive", "inconclusive"},
		{"errored", "errored"},
	}
	for _, pair := range same {
		if !sameOutcome(pair[0], pair[1]) {
			t.Errorf("%q and %q read as different outcomes; they are one outcome in two spellings",
				pair[0], pair[1])
		}
	}

	// The other direction, and the reason this is not `return true`: an audit
	// that joined every pair would agree with any recording at all, which is
	// the failure mode of a check that was written to stop agreeing wrongly.
	different := [][2]string{
		{"killed", "survived"},
		{"timed-out", "killed"},
		{"not_run", "errored"},
		{"survived", "inconclusive"},
	}
	for _, pair := range different {
		if sameOutcome(pair[0], pair[1]) {
			t.Errorf("%q and %q read as one outcome; they are two, and an audit that joins them "+
				"agrees with a recording that disagrees", pair[0], pair[1])
		}
	}
}
