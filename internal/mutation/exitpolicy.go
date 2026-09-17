// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"fmt"
	"slices"
	"strconv"
)

type ExitCode int

const (
	ExitOK             ExitCode = 0
	ExitPolicyFailure  ExitCode = 1
	ExitInfrastructure ExitCode = 2
	ExitInterrupted    ExitCode = 130
	ExitTerminated     ExitCode = 143
)

func (c ExitCode) String() string { return strconv.Itoa(int(c)) }

type Policy struct {
	Strict         bool
	MinimumScore   float64
	RequireMutants bool
}

func DefaultPolicy() Policy {
	return Policy{Strict: false, MinimumScore: 0, RequireMutants: true}
}

func (p Policy) Validate() error {
	if p.MinimumScore < 0 || p.MinimumScore > 100 {
		return fmt.Errorf("mutation: minimum_score %v is outside [0,100]", p.MinimumScore)
	}
	return nil
}

type Signals struct {
	InfrastructureError bool
	ExpectationFailure  bool
}

type FailureReason string

const (
	ReasonInfrastructure      FailureReason = "infrastructure-error"
	ReasonErroredMutants      FailureReason = "errored-mutants"
	ReasonExpectationFailure  FailureReason = "expectation-failure"
	ReasonUnexpectedSurvivors FailureReason = "unexpected-survivors"
	ReasonBelowMinimumScore   FailureReason = "below-minimum-score"
	ReasonNoMutants           FailureReason = "no-mutants"
)

func (r FailureReason) String() string { return string(r) }

type Failure struct {
	Reason FailureReason
	Detail string
}

type Verdict struct {
	Code     ExitCode
	Failures []Failure
	Score    Score
}

func (v Verdict) OK() bool { return v.Code == ExitOK && len(v.Failures) == 0 }

func (v Verdict) Reasons() []FailureReason {
	out := make([]FailureReason, 0, len(v.Failures))
	for _, f := range v.Failures {
		out = append(out, f.Reason)
	}
	return out
}

func (v Verdict) Has(r FailureReason) bool {
	return slices.ContainsFunc(v.Failures, func(f Failure) bool { return f.Reason == r })
}

func Decide(t Tally, p Policy, s Signals) Verdict {
	score := ScoreOf(t)
	v := Verdict{Code: ExitOK, Score: score}

	if s.InfrastructureError {
		v.Failures = append(v.Failures, Failure{
			Reason: ReasonInfrastructure,
			Detail: "the run hit an infrastructure, configuration, or baseline failure",
		})
	}
	if t.Errored > 0 {
		v.Failures = append(v.Failures, Failure{
			Reason: ReasonErroredMutants,
			Detail: fmt.Sprintf("%s could not be executed by the harness", countNoun(t.Errored, "mutant")),
		})
	}
	if s.ExpectationFailure {
		v.Failures = append(v.Failures, Failure{
			Reason: ReasonExpectationFailure,
			Detail: "an expectation is unfulfilled or stale",
		})
	}
	if len(v.Failures) > 0 {
		v.Code = ExitInfrastructure
		return v
	}

	if p.Strict && t.UnexpectedSurvivors > 0 {
		v.Failures = append(v.Failures, Failure{
			Reason: ReasonUnexpectedSurvivors,
			Detail: fmt.Sprintf("policy.strict is set and %s survived unexpectedly", countNoun(t.UnexpectedSurvivors, "mutant")),
		})
	}
	if percent, ok := score.Percent(); ok && p.MinimumScore > 0 && percent < p.MinimumScore {
		v.Failures = append(v.Failures, Failure{
			Reason: ReasonBelowMinimumScore,
			Detail: fmt.Sprintf("score %s is below policy.minimum_score %s",
				formatPercent(percent), formatPercent(p.MinimumScore)),
		})
	}
	if p.RequireMutants && t.Total() == 0 {
		v.Failures = append(v.Failures, Failure{
			Reason: ReasonNoMutants,
			Detail: "policy.require_mutants is set and the run produced no mutants",
		})
	}
	if len(v.Failures) > 0 {
		v.Code = ExitPolicyFailure
	}
	return v
}

func DecideResults(results []Result, p Policy, s Signals) (Verdict, error) {
	t, err := TallyOf(results)
	if err != nil {
		return Verdict{}, err
	}
	return Decide(t, p, s), nil
}

func formatPercent(p float64) string {
	return strconv.FormatFloat(p, 'f', 2, 64) + "%"
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
