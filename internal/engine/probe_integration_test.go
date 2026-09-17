// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/trace"
)

// The probe phase's own claim, and it is a claim about two runs rather than
// about one: probing changes how much a run does and not what it concludes.
//
// That is the only thing worth asserting about an optimisation whose whole
// contract is "same answer, less work", and it is why both halves are measured
// in one test. A run that agreed and did no less work would be paying for
// nothing; one that did less work and disagreed would be wrong.

// probedRun runs a fixture with probing set one way and returns what it
// concluded and what it started.
func probedRun(t *testing.T, probing config.Probing) ([]string, map[string]int, RunOutcome) {
	t.Helper()

	sink := trace.NewMemorySink(0)
	opts := options(t, "unobserved")
	opts.TraceSink = sink
	opts.Config.Test.BaselineRuns = 1
	opts.Config.Test.Probing = probing
	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run with probing %s: %v", probing, err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s with probing %s, want %s", outcome.Status, probing, StatusOK)
	}
	settled := slices.Clone(results(events))
	slices.Sort(settled)
	return settled, execKinds(sink.Events()), outcome
}

// TestProbingReachesTheSameVerdictsForLessWork is the whole contract.
//
// The verdicts are compared as strings so that a disagreement names the mutant
// and both answers rather than reporting a count. The work is compared as
// counted child processes, which is the only comparison this repository makes
// about cost: a duration is a fact about the machine that day, and a process
// count is a fact about the run.
func TestProbingReachesTheSameVerdictsForLessWork(t *testing.T) {
	t.Parallel()

	quiet, quietKinds, quietOut := probedRun(t, config.ProbingOff)
	probed, probedKinds, probedOut := probedRun(t, config.ProbingOn)

	if !slices.Equal(quiet, probed) {
		t.Errorf("probing changed the run's verdicts\n with: %s\nwithout: %s",
			strings.Join(probed, "\n         "), strings.Join(quiet, "\n         "))
	}
	if quietOut.Probe.Binaries != 0 {
		t.Errorf("a run with probing off reports %+v, want nothing", quietOut.Probe)
	}
	if probedOut.Probe.Binaries == 0 {
		t.Fatalf("a run with probing on probed no binaries: %+v", probedOut.Probe)
	}
	if probedOut.Probe.Settled == 0 && probedOut.Probe.Narrowed == 0 {
		t.Fatalf("the probe settled and narrowed nothing, so the run below proves nothing: %+v",
			probedOut.Probe)
	}

	// The counted saving. A mutant the probe settled is a mutant-run the
	// executor never starts, so the probing run has strictly fewer of them --
	// and strictly more probe-runs, which is what it paid.
	if probedKinds[trace.ExecKindMutantRun] >= quietKinds[trace.ExecKindMutantRun] {
		t.Errorf("probing started %d mutant runs and not probing started %d: the saving is the point",
			probedKinds[trace.ExecKindMutantRun], quietKinds[trace.ExecKindMutantRun])
	}
	if probedKinds[trace.ExecKindProbeRun] == 0 {
		t.Errorf("the probing run started no probe runs, so whatever it saved it did not save by probing")
	}
	if quietKinds[trace.ExecKindProbeRun] != 0 {
		t.Errorf("the run with probing off started %d probe runs", quietKinds[trace.ExecKindProbeRun])
	}
}

// TestProbingChangesNoVerdictOverTheWholeOperatorCorpus is the same claim made
// where it is hardest to hold.
//
// `fixtures/unobserved` is two mutants chosen to exercise the two answers a
// probe can give, which makes the test above precise and makes it narrow. The
// families fixture is the other kind of evidence: every rule the registry
// implements, with a live candidate each, so every probe form and every refusal
// is exercised at once — and the claim is the only one that matters, which is
// that not one verdict moved.
//
// Nothing about the *saving* is asserted here. Whether this fixture has mutants
// a probe can settle is a fact about the fixture, and a test that required one
// would fail the day somebody tightened a test in it. What cannot change is the
// answer.
func TestProbingChangesNoVerdictOverTheWholeOperatorCorpus(t *testing.T) {
	t.Parallel()

	verdicts := func(probing config.Probing) []string {
		t.Helper()
		opts := options(t, "families")
		opts.Config.Test.BaselineRuns = 1
		opts.Config.Test.Probing = probing
		outcome, events, err := collect(t, t.Context(), opts)
		if err != nil {
			t.Fatalf("Run with probing %s: %v", probing, err)
		}
		if outcome.Status != StatusOK {
			t.Fatalf("status = %s with probing %s, want %s", outcome.Status, probing, StatusOK)
		}
		settled := slices.Clone(results(events))
		slices.Sort(settled)
		return settled
	}

	quiet := verdicts(config.ProbingOff)
	if len(quiet) == 0 {
		t.Fatal("the families fixture settled no mutants at all")
	}
	if probed := verdicts(config.ProbingOn); !slices.Equal(quiet, probed) {
		t.Errorf("probing changed the run's verdicts\n with: %s\nwithout: %s",
			strings.Join(probed, "\n         "), strings.Join(quiet, "\n         "))
	}
}

// TestAProbeSettledSurvivorIsNotAnUncoveredOne keeps the pair of fields a
// reader tells two remedies apart with.
//
// An uncovered mutant's lines are never run and the remedy is a test that
// reaches them. An unobserved one's lines are run, and the remedy is an
// assertion in a test that already runs them. A report that called the second
// "uncovered" would send somebody to write a test that exists.
func TestAProbeSettledSurvivorIsNotAnUncoveredOne(t *testing.T) {
	t.Parallel()

	opts := options(t, "unobserved")
	opts.Config.Test.BaselineRuns = 1
	opts.Config.Test.Probing = config.ProbingOn
	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Probe.Settled == 0 {
		t.Skip("this fixture's probe settled nothing, so there is no row to look at")
	}
	unobserved := 0
	for _, m := range outcome.Report.Mutants {
		if !m.Unobserved {
			continue
		}
		unobserved++
		if m.Uncovered {
			t.Errorf("mutant %s is marked both uncovered and unobserved", m.DisplayID)
		}
		if m.Attempts != 0 || len(m.Executions) != 0 {
			t.Errorf("mutant %s is unobserved and reports %d attempts and %d executions",
				m.DisplayID, m.Attempts, len(m.Executions))
		}
	}
	if unobserved != outcome.Probe.Settled {
		t.Errorf("the run settled %d mutants and the report marks %d unobserved",
			outcome.Probe.Settled, unobserved)
	}
}
