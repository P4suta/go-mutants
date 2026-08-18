// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestDecide(t *testing.T) {
	t.Parallel()

	strict := Policy{Strict: true, RequireMutants: true}
	floor := Policy{MinimumScore: 80, RequireMutants: true}
	permissive := Policy{}

	tests := []struct {
		name        string
		tally       Tally
		policy      Policy
		signals     Signals
		wantCode    ExitCode
		wantReasons []FailureReason
	}{
		{
			name:     "clean run with the shipped defaults",
			tally:    Tally{Killed: 8, UnexpectedSurvivors: 2},
			policy:   DefaultPolicy(),
			wantCode: ExitOK,
		},
		{
			name:     "survivors do not fail a build that did not ask",
			tally:    Tally{Killed: 1, UnexpectedSurvivors: 99},
			policy:   DefaultPolicy(),
			wantCode: ExitOK,
		},
		{
			name:        "infrastructure error",
			tally:       Tally{Killed: 4},
			policy:      DefaultPolicy(),
			signals:     Signals{InfrastructureError: true},
			wantCode:    ExitInfrastructure,
			wantReasons: []FailureReason{ReasonInfrastructure},
		},
		{
			name:        "errored mutants",
			tally:       Tally{Killed: 4, Errored: 1},
			policy:      DefaultPolicy(),
			wantCode:    ExitInfrastructure,
			wantReasons: []FailureReason{ReasonErroredMutants},
		},
		{
			name:        "unfulfilled or stale expectation",
			tally:       Tally{Killed: 4},
			policy:      DefaultPolicy(),
			signals:     Signals{ExpectationFailure: true},
			wantCode:    ExitInfrastructure,
			wantReasons: []FailureReason{ReasonExpectationFailure},
		},
		{
			name:    "every tier-two reason at once, in a fixed order",
			tally:   Tally{Killed: 1, Errored: 2},
			policy:  DefaultPolicy(),
			signals: Signals{InfrastructureError: true, ExpectationFailure: true},
			wantReasons: []FailureReason{
				ReasonInfrastructure,
				ReasonErroredMutants,
				ReasonExpectationFailure,
			},
			wantCode: ExitInfrastructure,
		},
		{
			// The documented non-rule: an unreproduced timeout says
			// something about scheduling noise, not about the tests.
			name:     "inconclusive alone never fails the run",
			tally:    Tally{Killed: 3, Inconclusive: 5},
			policy:   DefaultPolicy(),
			wantCode: ExitOK,
		},
		{
			name:     "inconclusive alone under strict",
			tally:    Tally{Killed: 3, Inconclusive: 5},
			policy:   strict,
			wantCode: ExitOK,
		},
		{
			name:        "strict with unexpected survivors",
			tally:       Tally{Killed: 3, UnexpectedSurvivors: 1},
			policy:      strict,
			wantCode:    ExitPolicyFailure,
			wantReasons: []FailureReason{ReasonUnexpectedSurvivors},
		},
		{
			name:     "strict with only expected survivors",
			tally:    Tally{Killed: 3, ExpectedSurvivors: 4},
			policy:   strict,
			wantCode: ExitOK,
		},
		{
			name:        "score below the floor",
			tally:       Tally{Killed: 7, UnexpectedSurvivors: 3},
			policy:      floor,
			wantCode:    ExitPolicyFailure,
			wantReasons: []FailureReason{ReasonBelowMinimumScore},
		},
		{
			name:     "score exactly at the floor passes",
			tally:    Tally{Killed: 8, UnexpectedSurvivors: 2},
			policy:   floor,
			wantCode: ExitOK,
		},
		{
			name:     "score above the floor",
			tally:    Tally{Killed: 9, UnexpectedSurvivors: 1},
			policy:   floor,
			wantCode: ExitOK,
		},
		{
			// The other documented non-rule: with no denominator there is
			// no percentage to be below a floor. require_mutants is the
			// gate for an empty run, which is why it defaults to true.
			name:        "a floor cannot be missed when nothing was measured",
			tally:       Tally{},
			policy:      Policy{MinimumScore: 80},
			wantCode:    ExitOK,
			wantReasons: nil,
		},
		{
			name:        "require_mutants on an empty run",
			tally:       Tally{},
			policy:      DefaultPolicy(),
			wantCode:    ExitPolicyFailure,
			wantReasons: []FailureReason{ReasonNoMutants},
		},
		{
			name:     "require_mutants off on an empty run",
			tally:    Tally{},
			policy:   permissive,
			wantCode: ExitOK,
		},
		{
			// Mutants that exist but were deferred to another shard are
			// still mutants; require_mutants is about discovery finding
			// nothing at all.
			name:     "require_mutants with everything deferred",
			tally:    Tally{NotRun: 40},
			policy:   DefaultPolicy(),
			wantCode: ExitOK,
		},
		{
			name:     "require_mutants with only expected survivors",
			tally:    Tally{ExpectedSurvivors: 3},
			policy:   DefaultPolicy(),
			wantCode: ExitOK,
		},
		{
			name:     "every mutant excluded from the score but present",
			tally:    Tally{Inconclusive: 2, NotRun: 3},
			policy:   Policy{Strict: true, MinimumScore: 90, RequireMutants: true},
			wantCode: ExitOK,
		},
		{
			name:   "both policy gates fail together",
			tally:  Tally{Killed: 1, UnexpectedSurvivors: 9},
			policy: Policy{Strict: true, MinimumScore: 80, RequireMutants: true},
			wantReasons: []FailureReason{
				ReasonUnexpectedSurvivors,
				ReasonBelowMinimumScore,
			},
			wantCode: ExitPolicyFailure,
		},
		{
			// A run that cannot be trusted is not scored: the policy gates
			// are not consulted, so the exit code says "fix the harness",
			// not "write more tests".
			name:        "tier two suppresses the policy gates",
			tally:       Tally{Killed: 1, UnexpectedSurvivors: 9, Errored: 1},
			policy:      Policy{Strict: true, MinimumScore: 99, RequireMutants: true},
			wantCode:    ExitInfrastructure,
			wantReasons: []FailureReason{ReasonErroredMutants},
		},
		{
			name:        "infrastructure failure on an empty run",
			tally:       Tally{},
			policy:      DefaultPolicy(),
			signals:     Signals{InfrastructureError: true},
			wantCode:    ExitInfrastructure,
			wantReasons: []FailureReason{ReasonInfrastructure},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := Decide(tc.tally, tc.policy, tc.signals)
			if got.Code != tc.wantCode {
				t.Errorf("Code = %v, want %v (failures: %+v)", got.Code, tc.wantCode, got.Failures)
			}
			if diff := cmp.Diff(tc.wantReasons, got.Reasons(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("reasons mismatch (-want +got):\n%s", diff)
			}
			if got.OK() != (tc.wantCode == ExitOK) {
				t.Errorf("OK() = %v, want %v", got.OK(), tc.wantCode == ExitOK)
			}
			if diff := cmp.Diff(ScoreOf(tc.tally), got.Score); diff != "" {
				t.Errorf("verdict score mismatch (-want +got):\n%s", diff)
			}
			for _, reason := range tc.wantReasons {
				if !got.Has(reason) {
					t.Errorf("Has(%q) = false", reason)
				}
			}
			for _, f := range got.Failures {
				if strings.TrimSpace(f.Detail) == "" {
					t.Errorf("failure %q has no detail", f.Reason)
				}
			}
		})
	}
}

// TestEveryFailureReasonIsReachable makes sure the table above exercises the
// whole reason set: a reason nobody can produce is dead documentation.
func TestEveryFailureReasonIsReachable(t *testing.T) {
	t.Parallel()

	all := []FailureReason{
		ReasonInfrastructure,
		ReasonErroredMutants,
		ReasonExpectationFailure,
		ReasonUnexpectedSurvivors,
		ReasonBelowMinimumScore,
		ReasonNoMutants,
	}
	produced := map[FailureReason]bool{}

	for _, v := range []Verdict{
		Decide(Tally{Killed: 1}, DefaultPolicy(), Signals{InfrastructureError: true}),
		Decide(Tally{Errored: 1}, DefaultPolicy(), Signals{}),
		Decide(Tally{Killed: 1}, DefaultPolicy(), Signals{ExpectationFailure: true}),
		Decide(Tally{UnexpectedSurvivors: 1}, Policy{Strict: true}, Signals{}),
		Decide(Tally{UnexpectedSurvivors: 1}, Policy{MinimumScore: 1}, Signals{}),
		Decide(Tally{}, DefaultPolicy(), Signals{}),
	} {
		for _, reason := range v.Reasons() {
			produced[reason] = true
		}
	}
	for _, reason := range all {
		if !produced[reason] {
			t.Errorf("no scenario produces %q", reason)
		}
		if reason.String() != string(reason) {
			t.Errorf("String() = %q, want %q", reason.String(), string(reason))
		}
	}
}

func TestFailureDetailsAreDeterministic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tally   Tally
		policy  Policy
		signals Signals
		want    string
	}{
		{
			name:   "one survivor is singular",
			tally:  Tally{Killed: 1, UnexpectedSurvivors: 1},
			policy: Policy{Strict: true},
			want:   "policy.strict is set and 1 mutant survived unexpectedly",
		},
		{
			name:   "several survivors are plural",
			tally:  Tally{Killed: 1, UnexpectedSurvivors: 3},
			policy: Policy{Strict: true},
			want:   "policy.strict is set and 3 mutants survived unexpectedly",
		},
		{
			name:   "the score gate quotes both numbers",
			tally:  Tally{Killed: 2, UnexpectedSurvivors: 1},
			policy: Policy{MinimumScore: 80},
			want:   "score 66.67% is below policy.minimum_score 80.00%",
		},
		{
			name:   "one errored mutant is singular",
			tally:  Tally{Errored: 1},
			policy: DefaultPolicy(),
			want:   "1 mutant could not be executed by the harness",
		},
		{
			name:   "several errored mutants are plural",
			tally:  Tally{Errored: 2},
			policy: DefaultPolicy(),
			want:   "2 mutants could not be executed by the harness",
		},
		{
			name:   "an empty run explains the gate that failed",
			tally:  Tally{},
			policy: DefaultPolicy(),
			want:   "policy.require_mutants is set and the run produced no mutants",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			v := Decide(tc.tally, tc.policy, tc.signals)
			if len(v.Failures) != 1 {
				t.Fatalf("Failures = %+v, want exactly one", v.Failures)
			}
			if got := v.Failures[0].Detail; got != tc.want {
				t.Fatalf("Detail = %q, want %q", got, tc.want)
			}
			// The same inputs must render the same bytes every time; the
			// detail ends up in a report that gets diffed across runs.
			again := Decide(tc.tally, tc.policy, tc.signals)
			if diff := cmp.Diff(v.Failures, again.Failures); diff != "" {
				t.Fatalf("details are not stable (-first +second):\n%s", diff)
			}
		})
	}
}

func TestDecideResults(t *testing.T) {
	t.Parallel()

	results := []Result{
		{Outcome: OutcomeKilled},
		{Outcome: OutcomeSurvived},
		{Outcome: OutcomeSurvived, ExpectedSurvivor: true},
	}
	got, err := DecideResults(results, Policy{Strict: true}, Signals{})
	if err != nil {
		t.Fatalf("DecideResults() error = %v", err)
	}
	if got.Code != ExitPolicyFailure {
		t.Errorf("Code = %v, want %v", got.Code, ExitPolicyFailure)
	}
	if !got.Has(ReasonUnexpectedSurvivors) {
		t.Errorf("reasons = %v, want the strict survivor reason", got.Reasons())
	}
	want := Score{Detected: 1, Denominator: 2}
	if got.Score != want {
		t.Errorf("Score = %+v, want %+v", got.Score, want)
	}

	if _, err := DecideResults([]Result{{Outcome: Outcome(77)}}, DefaultPolicy(), Signals{}); !errors.Is(err, ErrUnknownOutcome) {
		t.Errorf("DecideResults() error = %v, want ErrUnknownOutcome", err)
	}
}

func TestDefaultPolicy(t *testing.T) {
	t.Parallel()

	p := DefaultPolicy()
	if p.Strict {
		t.Error("strict must default to false: go-mutants does not fail a build unless asked")
	}
	if p.MinimumScore != 0 {
		t.Errorf("MinimumScore = %v, want 0", p.MinimumScore)
	}
	if !p.RequireMutants {
		t.Error("require_mutants must default to true")
	}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}
}

func TestPolicyValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		policy  Policy
		wantErr bool
	}{
		{name: "zero", policy: Policy{}},
		{name: "floor at zero", policy: Policy{MinimumScore: 0}},
		{name: "floor at one hundred", policy: Policy{MinimumScore: 100}},
		{name: "negative floor", policy: Policy{MinimumScore: -1}, wantErr: true},
		{name: "floor above one hundred", policy: Policy{MinimumScore: 100.5}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.policy.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestExitCodes(t *testing.T) {
	t.Parallel()

	want := map[ExitCode]string{
		ExitOK:             "0",
		ExitPolicyFailure:  "1",
		ExitInfrastructure: "2",
		ExitInterrupted:    "130",
		ExitTerminated:     "143",
	}
	for code, name := range want {
		if got := code.String(); got != name {
			t.Errorf("%d.String() = %q, want %q", int(code), got, name)
		}
	}
	// The signal codes are documented here but are never a verdict: Decide
	// only ever speaks about a run that finished.
	codes := map[ExitCode]bool{}
	for _, v := range []Verdict{
		Decide(Tally{Killed: 1}, DefaultPolicy(), Signals{}),
		Decide(Tally{UnexpectedSurvivors: 1}, Policy{Strict: true}, Signals{}),
		Decide(Tally{Errored: 1}, DefaultPolicy(), Signals{}),
	} {
		codes[v.Code] = true
	}
	for _, signalCode := range []ExitCode{ExitInterrupted, ExitTerminated} {
		if codes[signalCode] {
			t.Errorf("Decide returned %v, which only the signal path may produce", signalCode)
		}
	}
}

func TestVerdictAccessors(t *testing.T) {
	t.Parallel()

	clean := Decide(Tally{Killed: 3}, DefaultPolicy(), Signals{})
	if !clean.OK() || len(clean.Failures) != 0 || len(clean.Reasons()) != 0 {
		t.Fatalf("a clean run produced %+v", clean)
	}
	if clean.Has(ReasonNoMutants) {
		t.Error("Has() reported a reason that is not present")
	}

	failed := Decide(Tally{Errored: 1}, DefaultPolicy(), Signals{})
	if failed.OK() {
		t.Fatal("a run with an errored mutant is not OK")
	}
	if !slices.Contains(failed.Reasons(), ReasonErroredMutants) {
		t.Errorf("Reasons() = %v, want the errored-mutants reason", failed.Reasons())
	}
}
