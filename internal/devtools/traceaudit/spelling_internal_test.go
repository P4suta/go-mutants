// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package traceaudit

import "testing"

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
