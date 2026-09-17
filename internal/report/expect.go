// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"github.com/P4suta/go-mutants/internal/config"
)

type Disposition struct {
	Present  bool
	Rejected bool
	Outcome  Outcome
}

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
