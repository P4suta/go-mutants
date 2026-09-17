// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"fmt"
	"strconv"
)

type Result struct {
	Outcome          Outcome
	ExpectedSurvivor bool
}

type Tally struct {
	Killed              int
	TimedOut            int
	UnexpectedSurvivors int
	ExpectedSurvivors   int
	Inconclusive        int
	Errored             int
	NotRun              int
}

func (t *Tally) Record(r Result) error {
	switch r.Outcome {
	case OutcomeKilled:
		t.Killed++
	case OutcomeTimedOut:
		t.TimedOut++
	case OutcomeSurvived:
		if r.ExpectedSurvivor {
			t.ExpectedSurvivors++
		} else {
			t.UnexpectedSurvivors++
		}
	case OutcomeInconclusive:
		t.Inconclusive++
	case OutcomeErrored:
		t.Errored++
	case OutcomeNotRun:
		t.NotRun++
	default:
		return fmt.Errorf("%w: %d", ErrUnknownOutcome, uint8(r.Outcome))
	}
	return nil
}

func TallyOf(results []Result) (Tally, error) {
	var t Tally
	for i, r := range results {
		if err := t.Record(r); err != nil {
			return Tally{}, fmt.Errorf("result %d: %w", i, err)
		}
	}
	return t, nil
}

func (t Tally) Detected() int { return t.Killed + t.TimedOut }

func (t Tally) Survived() int { return t.UnexpectedSurvivors + t.ExpectedSurvivors }

func (t Tally) Denominator() int { return t.Detected() + t.UnexpectedSurvivors }

func (t Tally) Total() int {
	return t.Detected() + t.Survived() + t.Inconclusive + t.Errored + t.NotRun
}

type Score struct {
	Detected    int
	Denominator int
}

var NoScore = Score{}

func ScoreOf(t Tally) Score {
	return Score{Detected: t.Detected(), Denominator: t.Denominator()}
}

func (s Score) Defined() bool { return s.Denominator > 0 }

func (s Score) Percent() (float64, bool) {
	if !s.Defined() {
		return 0, false
	}
	return float64(s.Detected) / float64(s.Denominator) * 100, true
}

func (s Score) String() string {
	p, ok := s.Percent()
	if !ok {
		return "n/a"
	}
	return strconv.FormatFloat(p, 'f', 2, 64) + "%"
}

func (s Score) Validate() error {
	if s.Detected < 0 || s.Denominator < 0 {
		return fmt.Errorf("mutation: negative score components %d/%d", s.Detected, s.Denominator)
	}
	if s.Detected > s.Denominator {
		return fmt.Errorf("mutation: score detects %d of %d", s.Detected, s.Denominator)
	}
	return nil
}
