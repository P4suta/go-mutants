// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestTallyOf(t *testing.T) {
	t.Parallel()

	results := []Result{
		{Outcome: OutcomeKilled},
		{Outcome: OutcomeKilled},
		{Outcome: OutcomeTimedOut},
		{Outcome: OutcomeSurvived},
		{Outcome: OutcomeSurvived, ExpectedSurvivor: true},
		{Outcome: OutcomeInconclusive},
		{Outcome: OutcomeErrored},
		{Outcome: OutcomeNotRun},
		// An expectation flag on a non-survivor is meaningless and must not
		// move any counter: the ledger only ever predicts survival.
		{Outcome: OutcomeKilled, ExpectedSurvivor: true},
	}
	want := Tally{
		Killed:              3,
		TimedOut:            1,
		UnexpectedSurvivors: 1,
		ExpectedSurvivors:   1,
		Inconclusive:        1,
		Errored:             1,
		NotRun:              1,
	}

	got, err := TallyOf(results)
	if err != nil {
		t.Fatalf("TallyOf() error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("tally mismatch (-want +got):\n%s", diff)
	}
	if got.Detected() != 4 {
		t.Errorf("Detected() = %d, want 4", got.Detected())
	}
	if got.Survived() != 2 {
		t.Errorf("Survived() = %d, want 2", got.Survived())
	}
	if got.Denominator() != 5 {
		t.Errorf("Denominator() = %d, want 5", got.Denominator())
	}
	if got.Total() != len(results) {
		t.Errorf("Total() = %d, want %d", got.Total(), len(results))
	}
}

func TestTallyOfRejectsUnknownOutcomes(t *testing.T) {
	t.Parallel()

	_, err := TallyOf([]Result{{Outcome: OutcomeKilled}, {Outcome: Outcome(42)}})
	if !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("TallyOf() error = %v, want ErrUnknownOutcome", err)
	}
}

func TestTallyOfEmptyRun(t *testing.T) {
	t.Parallel()

	got, err := TallyOf(nil)
	if err != nil {
		t.Fatalf("TallyOf(nil) error = %v", err)
	}
	if diff := cmp.Diff(Tally{}, got); diff != "" {
		t.Fatalf("empty tally mismatch (-want +got):\n%s", diff)
	}
	if got.Total() != 0 || got.Denominator() != 0 {
		t.Fatalf("empty tally is not empty: %+v", got)
	}
}

// TestSurvivorsSplitByTheLedger pins which of the two survivor counters a
// survivor lands in. The counts are deliberately lopsided: the tallies above
// hold one of each, and one of each is exactly the shape a rule that sent
// every survivor to the wrong counter would still produce.
func TestSurvivorsSplitByTheLedger(t *testing.T) {
	t.Parallel()

	results := []Result{
		{Outcome: OutcomeSurvived},
		{Outcome: OutcomeSurvived},
		{Outcome: OutcomeSurvived},
		{Outcome: OutcomeSurvived, ExpectedSurvivor: true},
	}
	got, err := TallyOf(results)
	if err != nil {
		t.Fatalf("TallyOf() error = %v", err)
	}
	want := Tally{UnexpectedSurvivors: 3, ExpectedSurvivors: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("tally mismatch (-want +got):\n%s", diff)
	}
	// Only the three the ledger did not predict reach the denominator; the
	// declared one is accounted for and leaves the score alone.
	if got.Denominator() != 3 {
		t.Errorf("Denominator() = %d, want 3", got.Denominator())
	}
	if got.Survived() != 4 {
		t.Errorf("Survived() = %d, want 4", got.Survived())
	}
}

func TestScoreOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		tally       Tally
		wantScore   Score
		wantDefined bool
		wantPercent float64
		wantString  string
	}{
		{
			name:        "everything detected",
			tally:       Tally{Killed: 7, TimedOut: 3},
			wantScore:   Score{Detected: 10, Denominator: 10},
			wantDefined: true,
			wantPercent: 100,
			wantString:  "100.00%",
		},
		{
			name:        "nothing detected",
			tally:       Tally{UnexpectedSurvivors: 4},
			wantScore:   Score{Detected: 0, Denominator: 4},
			wantDefined: true,
			wantPercent: 0,
			wantString:  "0.00%",
		},
		{
			name:        "confirmed timeouts count as detections",
			tally:       Tally{Killed: 1, TimedOut: 1, UnexpectedSurvivors: 2},
			wantScore:   Score{Detected: 2, Denominator: 4},
			wantDefined: true,
			wantPercent: 50,
			wantString:  "50.00%",
		},
		{
			name: "excluded outcomes do not move the score",
			tally: Tally{
				Killed:              3,
				UnexpectedSurvivors: 1,
				ExpectedSurvivors:   5,
				Inconclusive:        6,
				Errored:             7,
				NotRun:              8,
			},
			wantScore:   Score{Detected: 3, Denominator: 4},
			wantDefined: true,
			wantPercent: 75,
			wantString:  "75.00%",
		},
		{
			name:        "repeating decimal",
			tally:       Tally{Killed: 2, UnexpectedSurvivors: 1},
			wantScore:   Score{Detected: 2, Denominator: 3},
			wantDefined: true,
			wantPercent: 200.0 / 3.0,
			wantString:  "66.67%",
		},
		{
			name:       "nothing measured at all",
			tally:      Tally{},
			wantScore:  NoScore,
			wantString: "n/a",
		},
		{
			name:       "only expected survivors",
			tally:      Tally{ExpectedSurvivors: 3},
			wantScore:  NoScore,
			wantString: "n/a",
		},
		{
			name:       "only excluded outcomes",
			tally:      Tally{Inconclusive: 2, Errored: 1, NotRun: 9},
			wantScore:  NoScore,
			wantString: "n/a",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := ScoreOf(tc.tally)
			if diff := cmp.Diff(tc.wantScore, got); diff != "" {
				t.Fatalf("score mismatch (-want +got):\n%s", diff)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if got.Defined() != tc.wantDefined {
				t.Fatalf("Defined() = %v, want %v", got.Defined(), tc.wantDefined)
			}
			percent, ok := got.Percent()
			if ok != tc.wantDefined {
				t.Fatalf("Percent() ok = %v, want %v", ok, tc.wantDefined)
			}
			if ok && math.Abs(percent-tc.wantPercent) > 1e-9 {
				t.Errorf("Percent() = %v, want %v", percent, tc.wantPercent)
			}
			if !ok && percent != 0 {
				t.Errorf("Percent() = %v for an undefined score, want 0", percent)
			}
			if got.String() != tc.wantString {
				t.Errorf("String() = %q, want %q", got.String(), tc.wantString)
			}
		})
	}
}

// TestNoScoreIsNotZeroPercent is the whole reason Score has no float field:
// "nothing was measured" and "nothing was caught" must be distinguishable.
func TestNoScoreIsNotZeroPercent(t *testing.T) {
	t.Parallel()

	nothingMeasured := ScoreOf(Tally{NotRun: 12})
	nothingCaught := ScoreOf(Tally{UnexpectedSurvivors: 12})

	if nothingMeasured.Defined() {
		t.Fatal("a run with an empty denominator must have no percentage")
	}
	if !nothingCaught.Defined() {
		t.Fatal("a run with survivors has a percentage")
	}
	if nothingMeasured == nothingCaught {
		t.Fatal("an unmeasured run and a 0% run must not be the same value")
	}
	if nothingMeasured.String() == nothingCaught.String() {
		t.Fatalf("both render as %q", nothingMeasured.String())
	}
	if nothingMeasured != NoScore {
		t.Errorf("ScoreOf(unmeasured) = %+v, want NoScore", nothingMeasured)
	}
}

func TestScoreValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		score   Score
		wantErr bool
	}{
		{name: "empty", score: Score{}},
		{name: "ordinary", score: Score{Detected: 3, Denominator: 4}},
		{name: "detected exceeds denominator", score: Score{Detected: 5, Denominator: 4}, wantErr: true},
		{name: "negative", score: Score{Detected: -1, Denominator: 4}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.score.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestTallyRecordIsIncremental(t *testing.T) {
	t.Parallel()

	var tally Tally
	for _, r := range []Result{
		{Outcome: OutcomeKilled},
		{Outcome: OutcomeSurvived},
		{Outcome: OutcomeSurvived, ExpectedSurvivor: true},
	} {
		if err := tally.Record(r); err != nil {
			t.Fatalf("Record(%+v) error = %v", r, err)
		}
	}
	want := Tally{Killed: 1, UnexpectedSurvivors: 1, ExpectedSurvivors: 1}
	if diff := cmp.Diff(want, tally); diff != "" {
		t.Fatalf("tally mismatch (-want +got):\n%s", diff)
	}
	if err := tally.Record(Result{Outcome: Outcome(99)}); !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("Record() error = %v, want ErrUnknownOutcome", err)
	}
	if diff := cmp.Diff(want, tally); diff != "" {
		t.Fatalf("a rejected record changed the tally (-want +got):\n%s", diff)
	}
}
