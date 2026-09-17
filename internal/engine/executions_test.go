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
	if len(result.Attempts[0].Binaries) > 0 {
		executions[0].Binaries[0] = "rewritten"
		if result.Attempts[0].Binaries[0] == "rewritten" {
			t.Error("the report's binaries alias the attempt's own slice")
		}
	}
}

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
