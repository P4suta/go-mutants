// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import (
	"fmt"
	"slices"
	"strconv"
)

// ExitCode is the process exit status go-mutants reports.
type ExitCode int

// The exit codes. The table is part of the CLI contract and is injected into
// the help output, so it lives next to the code that decides it.
const (
	// ExitOK means the run completed and no policy failed.
	ExitOK ExitCode = 0
	// ExitPolicyFailure means the run completed and measured something the
	// user asked to fail on: strict survivors, a score below the floor, or
	// no mutants at all. Opt-in by construction.
	ExitPolicyFailure ExitCode = 1
	// ExitInfrastructure means the run cannot be trusted: an infrastructure,
	// configuration, or baseline failure, an errored mutant, or an
	// expectations ledger that no longer describes reality.
	ExitInfrastructure ExitCode = 2
	// ExitInterrupted is the exit code after Ctrl-C. Decide never returns
	// it; the signal path does, after publishing a partial report.
	ExitInterrupted ExitCode = 130
	// ExitTerminated is the exit code after SIGTERM, likewise set by the
	// signal path.
	ExitTerminated ExitCode = 143
)

// String renders the code as its number.
func (c ExitCode) String() string { return strconv.Itoa(int(c)) }

// Policy is the opt-in gating configuration, from `[policy]`.
//
// Every gate defaults to off or permissive. go-mutants does not fail a build
// unless it was asked to: a mutation score that silently breaks CI the first
// time someone adds a hard-to-test error path teaches people to delete the
// tool, not to write tests.
type Policy struct {
	// Strict fails the run when any unexpected survivor exists. Default
	// false.
	Strict bool
	// MinimumScore is the score floor as a percentage. Zero or negative
	// disables the gate.
	MinimumScore float64
	// RequireMutants fails a run that produced no mutants at all. Default
	// true: an empty run that reports success is the most dangerous kind of
	// green.
	//
	// "No mutants at all" is a property of the catalogue, not of the work a
	// run happened to do, and Decide reads it off Tally.Total(). The tally
	// it is given must therefore account for every catalogued mutant,
	// including every OutcomeNotRun one; see Decide for what goes wrong
	// otherwise.
	RequireMutants bool
}

// DefaultPolicy returns the shipped defaults: not strict, no score floor, and
// mutants required.
func DefaultPolicy() Policy {
	return Policy{Strict: false, MinimumScore: 0, RequireMutants: true}
}

// Validate reports whether the policy is configurable as written. It is the
// configuration layer's check, not Decide's: Decide treats an out-of-range
// floor as the caller's problem already caught here.
func (p Policy) Validate() error {
	if p.MinimumScore < 0 || p.MinimumScore > 100 {
		return fmt.Errorf("mutation: minimum_score %v is outside [0,100]", p.MinimumScore)
	}
	return nil
}

// Signals carries the facts Decide cannot derive from the outcomes alone.
type Signals struct {
	// InfrastructureError is true when the run hit an infrastructure,
	// configuration, snapshot, or baseline failure. The engine sets it; the
	// score is not meaningful when it is true.
	InfrastructureError bool
	// ExpectationFailure is true when the `[[mutation.expect]]` ledger did
	// not describe reality: an expected survivor was killed (unfulfilled) or
	// an expected ID is not in the catalogue any more (stale). Both mean the
	// ledger is lying to whoever reads it, which is a contract failure
	// rather than a test-quality signal.
	ExpectationFailure bool
}

// FailureReason is a machine-readable cause of a non-zero exit. Reasons are
// stable strings so that CI, the JSON report, and the console all name a
// failure the same way.
type FailureReason string

// The v1 failure reasons.
const (
	// ReasonInfrastructure is an infrastructure, configuration, or baseline
	// failure reported by the engine.
	ReasonInfrastructure FailureReason = "infrastructure-error"
	// ReasonErroredMutants is one or more mutants whose harness failed.
	ReasonErroredMutants FailureReason = "errored-mutants"
	// ReasonExpectationFailure is an unfulfilled or stale expectation.
	ReasonExpectationFailure FailureReason = "expectation-failure"
	// ReasonUnexpectedSurvivors is `policy.strict` with survivors present.
	ReasonUnexpectedSurvivors FailureReason = "unexpected-survivors"
	// ReasonBelowMinimumScore is a defined score below `policy.minimum_score`.
	ReasonBelowMinimumScore FailureReason = "below-minimum-score"
	// ReasonNoMutants is `policy.require_mutants` with an empty run.
	ReasonNoMutants FailureReason = "no-mutants"
)

// String returns the reason as it appears in reports.
func (r FailureReason) String() string { return string(r) }

// Failure is one reason a run failed, with a rendered detail for humans.
type Failure struct {
	// Reason is the machine-readable cause.
	Reason FailureReason
	// Detail is a deterministic one-line explanation. It contains no paths,
	// timings, or anything else that varies between two runs of the same
	// workspace, so golden tests can assert it.
	Detail string
}

// Verdict is the decision: an exit code and every reason behind it.
type Verdict struct {
	// Code is the exit status.
	Code ExitCode
	// Failures are the reasons, in a fixed order, most severe first. Empty
	// when Code is ExitOK.
	Failures []Failure
	// Score is the score the decision was made against, for reporting.
	Score Score
}

// OK reports whether the run passed.
func (v Verdict) OK() bool { return v.Code == ExitOK && len(v.Failures) == 0 }

// Reasons returns just the machine-readable reasons, in Failures order.
func (v Verdict) Reasons() []FailureReason {
	out := make([]FailureReason, 0, len(v.Failures))
	for _, f := range v.Failures {
		out = append(out, f.Reason)
	}
	return out
}

// Has reports whether the verdict carries a reason.
func (v Verdict) Has(r FailureReason) bool {
	return slices.ContainsFunc(v.Failures, func(f Failure) bool { return f.Reason == r })
}

// Decide computes the exit code for a completed run.
//
// The two tiers are evaluated in order and do not mix. If anything says the
// run itself is untrustworthy — an infrastructure failure, a mutant whose
// harness errored, or an expectations ledger that no longer matches the
// catalogue — the answer is exit 2 and the policy gates are not consulted at
// all. Gating a build on a score derived from a broken run would be worse
// than not gating it: the number would be arbitrary and the failure would be
// attributed to the tests.
//
// Two deliberate non-rules, both of which have a table row in the tests:
//
//   - Inconclusive results never force exit 2 on their own. A single timeout
//     that did not reproduce is a fact about scheduling noise, not about the
//     tests or the harness; it is excluded from the score and reported, and
//     that is all.
//   - `minimum_score` is only checked when the score is defined. A run with
//     an empty denominator has no percentage to compare, so the floor cannot
//     be "missed"; `require_mutants` is the gate that catches an empty run,
//     which is why it defaults to true.
//
// One contract on the caller, because `require_mutants` is the one gate Decide
// cannot derive from the executed work alone. It fires on Tally.Total() == 0,
// so the tally must account for every *catalogued* mutant and not only the
// ones the run executed: everything deferred, filtered out, or cut short by a
// signal has to be recorded as an OutcomeNotRun result. Both v1 selection
// features depend on it. A `--shard k/n` whose shard drew nothing and a
// `--changed <ref>` run whose diff touched no candidate each still ran
// discovery over the whole tree and each still have a full catalogue behind
// them; a tally built only from executed mutants would report those runs as
// empty and exit 1 with "the run produced no mutants" for a run that did
// nothing wrong. Discovery finding nothing at all is the only thing this gate
// is meant to catch.
//
// The floor is compared against the unrounded percentage while Failure.Detail
// renders two decimals, so a near-tie can legitimately read "score 66.67% is
// below policy.minimum_score 66.67%". Rounding before comparing would be the
// worse trade: it would let a run that is genuinely below the floor pass.
func Decide(t Tally, p Policy, s Signals) Verdict {
	score := ScoreOf(t)
	v := Verdict{Code: ExitOK, Score: score}

	// Tier 2: the run cannot be trusted.
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

	// Tier 1: the run is trustworthy and the user asked to gate on it.
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

// DecideResults counts results and decides in one step. The results have to
// cover every catalogued mutant, not-run ones included, for the reason Decide
// spells out.
func DecideResults(results []Result, p Policy, s Signals) (Verdict, error) {
	t, err := TallyOf(results)
	if err != nil {
		return Verdict{}, err
	}
	return Decide(t, p, s), nil
}

// formatPercent renders a percentage with two decimals, so that details are
// byte-identical between runs and between platforms.
func formatPercent(p float64) string {
	return strconv.FormatFloat(p, 'f', 2, 64) + "%"
}

// countNoun renders "1 mutant" or "3 mutants".
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
