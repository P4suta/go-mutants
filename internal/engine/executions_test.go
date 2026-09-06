// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"errors"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/trace"
)

// TestExecutionsCarryEveryAttemptInOrder pins the shape a retried mutant has in
// the report.
//
// It is a unit test because the corpus cannot produce one on purpose: a second
// attempt happens only when the first timed out, and a fixture whose tests hang
// for the whole derived timeout would cost every run of this suite ten seconds
// to prove one row of a document. What the retry pass produces is
// internal/execute's contract — two attempts, the second alone on the machine
// as worker 0 — and this is the claim that the report carries both of them, in
// the order they were made, with the worker that made each.
func TestExecutionsCarryEveryAttemptInOrder(t *testing.T) {
	t.Parallel()

	result := execute.MutantResult{
		ID: "a1b2c3",
		Attempts: []execute.Attempt{
			{
				Worker:   3,
				Outcome:  mutation.OutcomeTimedOut,
				KilledBy: "example.com/m/internal/alpha",
				Duration: 10 * time.Second,
				Binaries: []string{"example.com/m/internal/alpha"},
			},
			{
				Worker:   0,
				Outcome:  mutation.OutcomeKilled,
				KilledBy: "example.com/m/internal/alpha",
				Duration: 900 * time.Millisecond,
				Binaries: []string{"example.com/m/internal/alpha", "example.com/m/internal/beta"},
			},
		},
		// The verdict the retry pass reaches when a timeout does not reproduce:
		// the two attempts disagree, and disagreement is neither a detection
		// nor a survival. The rows are what let a reader see *that*, which is
		// the whole reason they are in the document.
		Final:    mutation.OutcomeInconclusive,
		Duration: 10*time.Second + 900*time.Millisecond,
	}

	executions := executionsOf(result)
	want := []report.Execution{
		{
			Attempt: 1, Worker: 3, Outcome: report.OutcomeTimedOut,
			KilledBy: "example.com/m/internal/alpha", DurationMS: 10_000,
			Binaries: []string{"example.com/m/internal/alpha"},
		},
		{
			Attempt: 2, Worker: 0, Outcome: report.OutcomeKilled,
			KilledBy: "example.com/m/internal/alpha", DurationMS: 900,
			Binaries: []string{"example.com/m/internal/alpha", "example.com/m/internal/beta"},
		},
	}
	if len(executions) != len(want) {
		t.Fatalf("executions = %+v, want one row per attempt (%d)", executions, len(want))
	}
	for i, got := range executions {
		if got.Attempt != want[i].Attempt || got.Worker != want[i].Worker ||
			got.Outcome != want[i].Outcome || got.KilledBy != want[i].KilledBy ||
			got.DurationMS != want[i].DurationMS {
			t.Errorf("executions[%d] = %+v, want %+v", i, got, want[i])
		}
		if len(got.Binaries) != len(want[i].Binaries) {
			t.Errorf("executions[%d].Binaries = %v, want %v", i, got.Binaries, want[i].Binaries)
		}
	}
	// The rows do not alias what internal/execute handed over: the scheduler
	// reuses a mutant's arguments between passes, and a report holding somebody
	// else's slice is a report that can change after it is written.
	if len(result.Attempts[0].Binaries) > 0 {
		executions[0].Binaries[0] = "rewritten"
		if result.Attempts[0].Binaries[0] == "rewritten" {
			t.Error("the report's binaries alias the attempt's own slice")
		}
	}
}

// TestTheFactsARunDidNotMeasureAreAbsent pins the rendering of a run that
// stopped early.
//
// [report.Options] takes a nil for each of these and means "this run did not
// measure it", and the engine has to mean it too: an interruption between
// cataloguing and the first `go build` publishes a report, and a `validation:
// {builds: 0}` in it would be that run claiming it established what compiles
// without compiling anything. Zero is the discriminator because zero is not a
// measurement any of the three can produce.
func TestTheFactsARunDidNotMeasureAreAbsent(t *testing.T) {
	t.Parallel()

	if got := reportValidation(ValidationFacts{}); got != nil {
		t.Errorf("reportValidation(nothing measured) = %+v, want nothing", got)
	}
	if got := reportValidation(ValidationFacts{Builds: 3}); got == nil || got.Builds != 3 {
		t.Errorf("reportValidation(3 builds) = %+v, want the 3 builds", got)
	}
	if got := reportSnapshot(SnapshotFacts{StableDir: true}); got != nil {
		t.Errorf("reportSnapshot(no files copied) = %+v, want nothing: a snapshot is at least one file", got)
	}
	if got := reportSnapshot(SnapshotFacts{StableDir: true, Files: 12}); got == nil || !got.StableDir || got.Files != 12 {
		t.Errorf("reportSnapshot(12 files) = %+v, want the stable copy of 12 files", got)
	}
	if got := reportTiming(Timing{}); got != nil {
		t.Errorf("reportTiming(no spans) = %+v, want nothing", got)
	}
	timed := reportTiming(Timing{
		Phases: []PhaseDuration{{Phase: PhaseDiscover, Duration: 40 * time.Millisecond}},
		Stages: []StageDuration{{
			Phase: PhaseDiscover, Name: "toolchain",
			Duration: 4 * time.Millisecond, Result: trace.ResultSucceeded,
		}},
	})
	if timed == nil || len(timed.Phases) != 1 || len(timed.Stages) != 1 {
		t.Fatalf("reportTiming(one phase and one stage) = %+v, want both", timed)
	}
	if timed.Phases[0] != (report.PhaseTiming{Name: "discover", DurationMS: 40}) {
		t.Errorf("phases[0] = %+v, want the phase's own name and its span in milliseconds", timed.Phases[0])
	}
	want := report.StageTiming{Phase: "discover", Name: "toolchain", DurationMS: 4, Result: report.StageSucceeded}
	if timed.Stages[0] != want {
		t.Errorf("stages[0] = %+v, want %+v", timed.Stages[0], want)
	}
}

// TestAMutantTheRunCouldNotSettleCarriesNoExecutions is the one outcome that
// has attempts and no rows.
//
// internal/execute produces it for a mutant that timed out once and was
// interrupted before the serial retry could repeat it: the pass really happened
// and is counted, but the run established nothing, so the verdict is not-run.
// A document with rows of per-attempt evidence under an outcome that says
// nothing was measured would be contradicting itself, and [report.Build]
// refuses exactly that — so the engine does not offer it.
func TestAMutantTheRunCouldNotSettleCarriesNoExecutions(t *testing.T) {
	t.Parallel()

	result := execute.MutantResult{
		ID: "a1b2c3",
		Attempts: []execute.Attempt{{
			Worker:   1,
			Outcome:  mutation.OutcomeTimedOut,
			Duration: 10 * time.Second,
			Binaries: []string{"example.com/m/internal/alpha"},
		}},
		Final: mutation.OutcomeNotRun,
	}
	if got := executionsOf(result); got != nil {
		t.Errorf("executionsOf(a not-run mutant) = %+v, want none", got)
	}

	// And an attempt this document has no word for takes the whole list with
	// it, rather than leaving one short of the attempt count — which is a
	// contradiction [report.Build] refuses, and would turn an impossible
	// outcome into a failed run at the very last step.
	unrenderable := execute.MutantResult{
		ID: "a1b2c3",
		Attempts: []execute.Attempt{
			{Worker: 0, Outcome: mutation.OutcomeSurvived, Duration: time.Second},
			{Worker: 0, Outcome: mutation.Outcome(200), Err: errors.New("nothing produces this")},
		},
		Final: mutation.OutcomeSurvived,
	}
	if got := executionsOf(unrenderable); got != nil {
		t.Errorf("executionsOf(an unrenderable attempt) = %+v, want none at all", got)
	}
}
