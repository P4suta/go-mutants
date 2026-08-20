// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"github.com/P4suta/go-mutants/internal/config"
)

// A Disposition is everything one run knows about one mutant id. It is the
// input to the expectations state machine, and it is a small closed set on
// purpose: a ledger row can only ever be judged against "not in the catalogue",
// "in the catalogue but refused by validation", or "in the catalogue with an
// outcome".
type Disposition struct {
	// Present is whether the catalogue holds a mutant with this id at all.
	Present bool
	// Rejected is whether validation refused it, in which case it has no
	// outcome and never had one.
	Rejected bool
	// Outcome is what happened to it. It is meaningful only when Present is
	// true and Rejected is false.
	Outcome Outcome
}

// StateOf is the expectations state machine, whole.
//
// The ledger says "this mutant is known to survive, and here is why". A run can
// agree, disagree, or fail to find out, and the three states say which:
//
//   - fulfilled — the mutant survived. The ledger was right, the mutant leaves
//     the score's denominator, and nobody is nagged about it.
//   - stale — the id is not in the catalogue. The code it described has been
//     edited or deleted, so the row now documents nothing at all. This is a
//     contract failure and exits 2: an unexplained id in a checked-in ledger is
//     worse than no ledger, because it looks like a decision somebody made.
//   - unfulfilled — everything else, and deliberately two different things at
//     once. Either the tests caught the mutant, which means the ledger is lying
//     to whoever reads it, or the run never measured it: `--mutant` selected
//     something else, validation rejected it, the outcome was inconclusive or
//     errored, or a signal cut the run short.
//
// Folding "caught" and "never measured" into one document value is a
// deliberate, and recoverable, choice. The alternative is a fourth enum member
// that every consumer has to learn in order to ignore, when the answer is
// already in the same document: the mutant's own row carries the outcome, and
// `killed` or `timed-out` against an unfulfilled expectation is the ledger
// being wrong while `not-run` is the run not having looked. What must not be
// folded is the *decision*, and it is not: [Report.ExpectationFailure] reads
// the outcomes rather than these three values, so a narrowed run does not
// escalate every unrelated ledger row to exit 2.
func StateOf(d Disposition) ExpectationState {
	switch {
	case !d.Present:
		return StateStale
	case d.Rejected:
		return StateUnfulfilled
	case d.Outcome == OutcomeSurvived:
		return StateFulfilled
	default:
		return StateUnfulfilled
	}
}

// Evaluate checks a ledger against what one run learned.
//
// Rows keep the order they were written in the configuration file. That order
// is already deterministic — internal/config refuses a duplicate id, so no two
// rows can be reordered by sorting on anything they have in common — and it is
// the order the author reads them in when the report tells them one has gone
// stale.
func Evaluate(ledger []config.Expectation, known map[string]Disposition) []Expectation {
	out := make([]Expectation, 0, len(ledger))
	for _, entry := range ledger {
		out = append(out, Expectation{
			ID:     entry.ID,
			Reason: entry.Reason,
			State:  StateOf(known[entry.ID]),
		})
	}
	return out
}
