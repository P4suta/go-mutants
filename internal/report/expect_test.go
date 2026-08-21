// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

// TestStateOf is the expectations state machine, every input.
//
// The table is exhaustive over what a run can know about an id: absent from the
// catalogue, rejected by validation, or catalogued with each of the six
// outcomes. Only survival fulfils an expectation; only absence is stale;
// everything else is unfulfilled, which is two different things at once by
// design — see the fourth column, and [TestExpectationFailureDistinguishes] for
// why the document folds them and the decision does not.
func TestStateOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		given report.Disposition
		want  report.ExpectationState
	}{
		{
			name:  "not in the catalogue",
			given: report.Disposition{},
			want:  report.StateStale,
		},
		{
			name:  "not in the catalogue, whatever else is set",
			given: report.Disposition{Outcome: report.OutcomeSurvived},
			want:  report.StateStale,
		},
		{
			name:  "survived, as predicted",
			given: report.Disposition{Present: true, Outcome: report.OutcomeSurvived},
			want:  report.StateFulfilled,
		},
		{
			name:  "killed: the ledger is lying",
			given: report.Disposition{Present: true, Outcome: report.OutcomeKilled},
			want:  report.StateUnfulfilled,
		},
		{
			name:  "confirmed timeout: also a detection",
			given: report.Disposition{Present: true, Outcome: report.OutcomeTimedOut},
			want:  report.StateUnfulfilled,
		},
		{
			name:  "inconclusive: nothing was learned",
			given: report.Disposition{Present: true, Outcome: report.OutcomeInconclusive},
			want:  report.StateUnfulfilled,
		},
		{
			name:  "errored: the harness failed, not the prediction",
			given: report.Disposition{Present: true, Outcome: report.OutcomeErrored},
			want:  report.StateUnfulfilled,
		},
		{
			name:  "never run: this run did not look",
			given: report.Disposition{Present: true, Outcome: report.OutcomeNotRun},
			want:  report.StateUnfulfilled,
		},
		{
			name:  "rejected: the mutant does not compile",
			given: report.Disposition{Present: true, Rejected: true},
			want:  report.StateUnfulfilled,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := report.StateOf(c.given); got != c.want {
				t.Errorf("StateOf(%+v) = %q, want %q", c.given, got, c.want)
			}
		})
	}
}

// TestEvaluateKeepsLedgerOrder proves the ledger is reported in the order it
// was written, which is the order its author reads it in.
func TestEvaluateKeepsLedgerOrder(t *testing.T) {
	t.Parallel()

	ledger := []config.Expectation{
		{ID: "c", Reason: "third"},
		{ID: "a", Reason: "first"},
		{ID: "b", Reason: "second"},
	}
	known := map[string]report.Disposition{
		"a": {Present: true, Outcome: report.OutcomeSurvived},
		"b": {Present: true, Outcome: report.OutcomeKilled},
	}
	got := report.Evaluate(ledger, known)
	want := []report.Expectation{
		{ID: "c", Reason: "third", State: report.StateStale},
		{ID: "a", Reason: "first", State: report.StateFulfilled},
		{ID: "b", Reason: "second", State: report.StateUnfulfilled},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Evaluate = %+v, want %+v", got, want)
	}
}

// TestEvaluateOfAnEmptyLedgerIsAnEmptyArray proves that no ledger produces `[]`
// rather than `null`.
func TestEvaluateOfAnEmptyLedgerIsAnEmptyArray(t *testing.T) {
	t.Parallel()

	if got := report.Evaluate(nil, nil); got == nil || len(got) != 0 {
		t.Errorf("Evaluate(nil, nil) = %v, want an empty non-nil slice", got)
	}
}

// TestFixtureExpectations checks the three states against a real run, joined to
// the mutants they name.
func TestFixtureExpectations(t *testing.T) {
	t.Parallel()

	r := buildFixture(t)
	if len(r.Expectations) != 3 {
		t.Fatalf("the fixture reports %d expectations, want 3", len(r.Expectations))
	}
	want := []report.ExpectationState{report.StateFulfilled, report.StateUnfulfilled, report.StateStale}
	for i, e := range r.Expectations {
		if e.State != want[i] {
			t.Errorf("expectation %d (%s) is %q, want %q", i, e.ID, e.State, want[i])
		}
		if e.Reason == "" {
			t.Errorf("expectation %d carries no reason", i)
		}
	}
	if r.Expectations[2].ID != staleID {
		t.Errorf("the stale row names %q, want %q", r.Expectations[2].ID, staleID)
	}
}

// TestExpectationFailureDistinguishes is the reason the state machine may fold
// two situations into "unfulfilled" without doing any harm.
//
// The document's three values are what a reader sees; the exit decision is made
// from the outcomes. A ledger row whose mutant the tests caught is a contract
// failure and exits 2. A row whose mutant was never measured — because
// `--mutant` narrowed the run, or because validation rejected it — is not,
// however unfulfilled it looks: escalating that would make every narrowed run
// fail on every unrelated ledger row.
func TestExpectationFailureDistinguishes(t *testing.T) {
	t.Parallel()

	base := fixtureOptions(t)
	mutants := base.Catalog.Mutants()

	cases := []struct {
		name   string
		ledger []config.Expectation
		// narrow replaces every result with a not-run one, as a `--mutant` run
		// or an interruption would.
		narrow bool
		want   bool
	}{
		{
			name:   "a fulfilled ledger is no failure",
			ledger: []config.Expectation{{ID: mutants[1].ID, Reason: "survives on purpose"}},
			want:   false,
		},
		{
			name:   "a killed expectation is a contract failure",
			ledger: []config.Expectation{{ID: mutants[0].ID, Reason: "expected to survive"}},
			want:   true,
		},
		{
			name:   "a confirmed timeout is a contract failure too",
			ledger: []config.Expectation{{ID: mutants[2].ID, Reason: "expected to survive"}},
			want:   true,
		},
		{
			name:   "a stale id is a contract failure",
			ledger: []config.Expectation{{ID: staleID, Reason: "long gone"}},
			want:   true,
		},
		{
			name:   "an inconclusive mutant proves nothing either way",
			ledger: []config.Expectation{{ID: mutants[3].ID, Reason: "expected to survive"}},
			want:   false,
		},
		{
			name:   "an errored mutant is the harness failing, not the ledger",
			ledger: []config.Expectation{{ID: mutants[5].ID, Reason: "expected to survive"}},
			want:   false,
		},
		{
			name:   "a rejected mutant is not a failed prediction",
			ledger: []config.Expectation{{ID: mutants[7].ID, Reason: "expected to survive"}},
			want:   false,
		},
		{
			name:   "a narrowed run does not escalate the whole ledger",
			ledger: []config.Expectation{{ID: mutants[0].ID, Reason: "expected to survive"}},
			narrow: true,
			want:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			opts := fixtureOptions(t)
			opts.Config.Mutation.Expect = c.ledger
			if c.narrow {
				for i := range opts.Results {
					opts.Results[i] = report.MutantResult{
						ID:           opts.Results[i].ID,
						Outcome:      mutation.OutcomeNotRun,
						NotRunReason: report.NotRunOutOfSelection,
					}
				}
				opts.Selected = 1
			}
			r, err := report.Build(opts)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if got := r.ExpectationFailure(); got != c.want {
				t.Errorf("ExpectationFailure() = %v, want %v (states %+v)", got, c.want, r.Expectations)
			}
		})
	}
}

// TestExpectedSurvivorsLeaveTheDenominator proves the wiring between the ledger
// and the score: a survivor the ledger predicted is neither a detection nor a
// miss, so adding the expectation raises the score without changing a single
// outcome.
func TestExpectedSurvivorsLeaveTheDenominator(t *testing.T) {
	t.Parallel()

	opts := fixtureOptions(t)
	mutants := opts.Catalog.Mutants()

	opts.Config.Mutation.Expect = nil
	without, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build without a ledger: %v", err)
	}

	opts.Config.Mutation.Expect = []config.Expectation{
		{ID: mutants[1].ID, Reason: "read only by the debug logger"},
		{ID: mutants[4].ID, Reason: "the branch is unreachable in practice"},
	}
	with, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build with a ledger: %v", err)
	}

	if with.Summary.Survived != without.Summary.Survived {
		t.Errorf("the ledger changed the survivor count: %d then %d",
			without.Summary.Survived, with.Summary.Survived)
	}
	if without.Summary.ScorePercent == nil || with.Summary.ScorePercent == nil {
		t.Fatal("one of the runs has no score")
	}
	// Two detections out of four become two out of two: both survivors leave
	// the denominator, and nothing else moves.
	if got := *without.Summary.ScorePercent; got != 50 {
		t.Errorf("score without the ledger = %v, want 50", got)
	}
	if got := *with.Summary.ScorePercent; got != 100 {
		t.Errorf("score with the ledger = %v, want 100", got)
	}
}
