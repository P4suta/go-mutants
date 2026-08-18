// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"fmt"
	"strconv"
)

// Result is one mutant's outcome together with whether the expectations
// ledger predicted it.
//
// ExpectedSurvivor comes from `[[mutation.expect]]`: the user has declared,
// with a reason, that this mutant is known to survive. A fulfilled
// expectation leaves the score alone in both directions — it is neither a
// detection to be proud of nor a survivor to be nagged about. An expectation
// that is *not* fulfilled (the mutant was killed, or its ID no longer exists)
// is not represented here at all: it is a contract failure the expectations
// phase reports through Signals, because it means the ledger is stale.
type Result struct {
	Outcome          Outcome
	ExpectedSurvivor bool
}

// Tally is the counted breakdown of a run.
//
// A tally covers every *catalogued* mutant, not only the executed ones: what
// was never run is counted in NotRun rather than left out. That is a contract,
// not a convention. Total() is what `policy.require_mutants` reads, so a tally
// assembled from executed results alone would report an empty shard or an
// untouched `--changed` run to Decide as a run that discovered nothing; see
// Decide.
//
// Survivors are split at count time, not at score time, because "survived"
// and "survived, and we said it would" are different facts that the report,
// the console summary, and the policy gate all need separately.
type Tally struct {
	// Killed is the number of mutants a test failure caught.
	Killed int
	// TimedOut is the number of confirmed timeouts, which count as
	// detections.
	TimedOut int
	// UnexpectedSurvivors are survivors the expectations ledger did not
	// predict. These are the ones that drag the score down.
	UnexpectedSurvivors int
	// ExpectedSurvivors are survivors covered by a fulfilled expectation.
	// Excluded from the score entirely.
	ExpectedSurvivors int
	// Inconclusive is the number of undecidable results.
	Inconclusive int
	// Errored is the number of harness failures.
	Errored int
	// NotRun is the number of mutants that were never executed: deferred to
	// another shard, outside the `--changed` diff, filtered out, or left
	// over when a signal cut the run short. They are catalogued mutants and
	// are counted here, which is what keeps Total() a statement about the
	// catalogue.
	NotRun int
}

// Record folds one result into the tally. Callers record one result per
// catalogued mutant, including an OutcomeNotRun result for every mutant the
// run did not execute; see Tally.
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

// TallyOf counts a slice of results. The slice is one result per catalogued
// mutant; see Tally.
func TallyOf(results []Result) (Tally, error) {
	var t Tally
	for i, r := range results {
		if err := t.Record(r); err != nil {
			return Tally{}, fmt.Errorf("result %d: %w", i, err)
		}
	}
	return t, nil
}

// Detected returns killed plus confirmed timeouts.
func (t Tally) Detected() int { return t.Killed + t.TimedOut }

// Survived returns every survivor, expected or not.
func (t Tally) Survived() int { return t.UnexpectedSurvivors + t.ExpectedSurvivors }

// Denominator returns the number of mutants the score is computed over:
// detections plus unexpected survivors.
//
// Everything else is excluded deliberately. Expected survivors are accounted
// for by the ledger, inconclusive results are by definition unmeasured,
// errored mutants say something about the harness rather than the tests, and
// not-run mutants were never given a chance. Including any of them would let
// an unrelated infrastructure problem move a number that is supposed to
// describe the test suite.
func (t Tally) Denominator() int { return t.Detected() + t.UnexpectedSurvivors }

// Total returns every counted mutant, which is the size of the catalogue when
// callers honour Tally's contract. `policy.require_mutants` gates on it being
// zero.
func (t Tally) Total() int {
	return t.Detected() + t.Survived() + t.Inconclusive + t.Errored + t.NotRun
}

// Score is a mutation score as the two integers it is derived from.
//
// The percentage is computed on demand rather than stored, so a Score can
// never disagree with itself, and the undefined case is structural: a zero
// denominator has no percentage at all. There is no sentinel percentage,
// because both plausible sentinels are lies — 0% would read as "your tests
// caught nothing" and 100% as "your tests caught everything", when the truth
// is that nothing was measured.
type Score struct {
	// Detected is killed plus confirmed timeouts.
	Detected int
	// Denominator is detections plus unexpected survivors.
	Denominator int
}

// NoScore is the score of a run with an empty denominator.
var NoScore = Score{}

// ScoreOf derives the score from a tally.
func ScoreOf(t Tally) Score {
	return Score{Detected: t.Detected(), Denominator: t.Denominator()}
}

// Defined reports whether the score has a percentage. It is false exactly
// when the denominator is zero.
func (s Score) Defined() bool { return s.Denominator > 0 }

// Percent returns the score as a percentage in [0,100] and true, or 0 and
// false when the score is undefined. Callers that render a number must check
// the second result; that is the whole reason it exists.
func (s Score) Percent() (float64, bool) {
	if !s.Defined() {
		return 0, false
	}
	return float64(s.Detected) / float64(s.Denominator) * 100, true
}

// String renders the score for humans: two decimal places, or "n/a" when
// undefined.
func (s Score) String() string {
	p, ok := s.Percent()
	if !ok {
		return "n/a"
	}
	return strconv.FormatFloat(p, 'f', 2, 64) + "%"
}

// Validate reports whether the score is internally coherent.
func (s Score) Validate() error {
	if s.Detected < 0 || s.Denominator < 0 {
		return fmt.Errorf("mutation: negative score components %d/%d", s.Detected, s.Denominator)
	}
	if s.Detected > s.Denominator {
		return fmt.Errorf("mutation: score detects %d of %d", s.Detected, s.Denominator)
	}
	return nil
}
