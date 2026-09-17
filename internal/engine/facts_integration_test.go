// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

func TestTheReportsTimingMatchesTheTracesPhaseAndStageDurations(t *testing.T) {
	t.Parallel()

	opts, sink := tracedOptions(t, "killable")
	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Report == nil {
		t.Fatal("the run published no report")
	}
	timing := outcome.Report.Timing
	if timing == nil {
		t.Fatal("the report of a completed run carries no timing")
	}
	events := sink.Events()

	recorded := make([]report.PhaseTiming, 0, len(events))
	for _, e := range events {
		if e.Type == trace.TypePhaseEnd && e.Phase.DurationMS != nil {
			recorded = append(recorded, report.PhaseTiming{Name: e.Phase.Name, DurationMS: *e.Phase.DurationMS})
		}
	}
	if len(recorded) == 0 {
		t.Fatal("the recording closed no phase")
	}
	if !slices.Equal(timing.Phases, recorded[:min(len(recorded), len(timing.Phases))]) {
		t.Errorf("the report's phases are\n\t%+v\nand the recording's are\n\t%+v", timing.Phases, recorded)
	}
	if want := []string{trace.PhaseDiscover, trace.PhaseBaseline, trace.PhaseMutate}; len(timing.Phases) < len(want) {
		t.Fatalf("the report reports %d phases, want at least %v", len(timing.Phases), want)
	}
	for i, name := range []string{trace.PhaseDiscover, trace.PhaseBaseline, trace.PhaseMutate} {
		if timing.Phases[i].Name != name {
			t.Errorf("phases[%d] is %q, want %q: the phases are in run order", i, timing.Phases[i].Name, name)
		}
	}

	stages := make([]report.StageTiming, 0, len(events))
	for _, e := range events {
		if e.Type == trace.TypeStage && e.Stage.State == trace.StateFinished && e.Stage.DurationMS != nil {
			stages = append(stages, report.StageTiming{
				Phase:      e.Stage.Phase,
				Name:       e.Stage.Name,
				DurationMS: *e.Stage.DurationMS,
				Result:     report.StageResult(e.Stage.Result),
			})
		}
	}
	if len(timing.Stages) == 0 {
		t.Fatal("the report reports no stages")
	}
	if len(timing.Stages) > len(stages) {
		t.Fatalf("the report reports %d stages and the recording %d", len(timing.Stages), len(stages))
	}
	if !slices.Equal(timing.Stages, stages[:len(timing.Stages)]) {
		t.Errorf("the report's stages are\n\t%+v\nand the recording's are\n\t%+v",
			timing.Stages, stages[:len(timing.Stages)])
	}
	for _, stage := range timing.Stages {
		if !stage.Result.Valid() {
			t.Errorf("the %s/%s stage reports %q, which is not a stage result",
				stage.Phase, stage.Name, stage.Result)
		}
	}
	if !slices.ContainsFunc(timing.Stages, func(s report.StageTiming) bool {
		return s.Phase == trace.PhaseMutate && s.Name == "execute"
	}) {
		t.Errorf("the report's timing has no mutate/execute stage: %+v", timing.Stages)
	}

	if outcome.Report.Validation == nil || outcome.Report.Validation.Builds < 1 {
		t.Errorf("validation = %+v, want the builds this run spent", outcome.Report.Validation)
	}
}

func TestEveryExecutedMutantReportsItsExecutions(t *testing.T) {
	t.Parallel()

	root := testkit.Copy(t, "killable")
	cacheRoot := t.TempDir()
	sink := trace.NewMemorySink(0)
	traced := cacheOptions(t, root, cacheRoot)
	traced.TraceSink = sink
	first := runCached(t, traced)

	executed := 0
	for _, m := range first.Mutants {
		if m.Executions == nil {
			t.Errorf("mutant %s carries no executions list at all", m.DisplayID)
			continue
		}
		if m.Uncovered {
			if len(m.Executions) != 0 || m.Attempts != 0 {
				t.Errorf("the uncovered mutant %s reports %d attempts and %d executions, want none of either",
					m.DisplayID, m.Attempts, len(m.Executions))
			}
			continue
		}
		executed++
		if len(m.Executions) != m.Attempts {
			t.Errorf("mutant %s reports %d attempts and %d executions",
				m.DisplayID, m.Attempts, len(m.Executions))
			continue
		}
		last := m.Executions[len(m.Executions)-1]
		for i, execution := range m.Executions {
			if execution.Attempt != i+1 {
				t.Errorf("mutant %s: executions[%d].attempt = %d, want %d", m.DisplayID, i, execution.Attempt, i+1)
			}
			if execution.Worker < 0 {
				t.Errorf("mutant %s: executions[%d] names worker %d", m.DisplayID, i, execution.Worker)
			}
			if len(execution.Binaries) == 0 {
				t.Errorf("mutant %s: executions[%d] started no binary", m.DisplayID, i)
			}
		}
		if last.Outcome != m.Outcome {
			t.Errorf("mutant %s is %s and its last execution observed %s", m.DisplayID, m.Outcome, last.Outcome)
		}
		switch m.Outcome {
		case report.OutcomeKilled:
			if m.KilledBy == nil || last.KilledBy != *m.KilledBy {
				t.Errorf("mutant %s was killed by %v and its execution names %q", m.DisplayID, m.KilledBy, last.KilledBy)
			}
		case report.OutcomeSurvived:
			if last.KilledBy != "" {
				t.Errorf("the surviving mutant %s has an execution killed by %q", m.DisplayID, last.KilledBy)
			}
		}
	}
	if executed == 0 {
		t.Fatal("the run executed nothing, so this test is checking nothing")
	}

	recorded := map[attemptKey]int{}
	for _, e := range sink.Events() {
		if e.Type == trace.TypeMutantExec {
			recorded[attemptKey{id: e.Mutant.ID, attempt: e.Mutant.Attempt}] = e.Mutant.Worker
		}
	}
	if len(recorded) == 0 {
		t.Fatal("the recording holds no mutant execution, so the join below checks nothing")
	}
	joined := 0
	for _, m := range first.Mutants {
		for _, execution := range m.Executions {
			key := attemptKey{id: m.ID, attempt: execution.Attempt}
			worker, ok := recorded[key]
			if !ok {
				t.Errorf("the recording has no attempt %d of mutant %s", execution.Attempt, m.DisplayID)
				continue
			}
			joined++
			if worker != execution.Worker {
				t.Errorf("attempt %d of mutant %s ran in worker %d in the report and %d in the recording",
					execution.Attempt, m.DisplayID, execution.Worker, worker)
			}
		}
	}
	if joined != len(recorded) {
		t.Errorf("the report accounts for %d of the recording's %d attempts", joined, len(recorded))
	}

	second := runCached(t, cacheOptions(t, root, cacheRoot))
	if second.Cache.Hits == 0 {
		t.Fatal("the second run adopted nothing from the cache, so the cached shape is untested")
	}
	measured := make(map[string]report.Mutant, len(first.Mutants))
	for _, m := range first.Mutants {
		measured[m.ID] = m
	}
	for _, m := range second.Mutants {
		if !m.Cached {
			continue
		}
		if m.Executions == nil || len(m.Executions) != 0 {
			t.Errorf("the cached mutant %s carries %v, want an empty list: this run executed nothing",
				m.DisplayID, m.Executions)
		}
		before, known := measured[m.ID]
		if !known {
			t.Errorf("the second run reports mutant %s, which the first did not catalogue", m.DisplayID)
			continue
		}
		if m.Attempts != before.Attempts {
			t.Errorf("the cached mutant %s reports %d attempts and the run that measured it reported %d",
				m.DisplayID, m.Attempts, before.Attempts)
		}
		if m.Attempts != len(before.Executions) {
			t.Errorf("the cached mutant %s reports %d attempts and the run that measured it executed it %d times",
				m.DisplayID, m.Attempts, len(before.Executions))
		}
	}
}

type attemptKey struct {
	id      string
	attempt int
}

type cancellingSink struct {
	after  string
	cancel context.CancelFunc
}

func (s *cancellingSink) Emit(e trace.Event) error {
	if e.Type == trace.TypeStage && e.Stage.State == trace.StateFinished && e.Stage.Name == s.after {
		s.cancel()
	}
	return nil
}

func (s *cancellingSink) Close() error { return nil }

func TestAnInterruptedRunReportsOnlyWhatItMeasured(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opts := options(t, "killable")
	opts.TraceSink = &cancellingSink{after: "catalog", cancel: cancel}

	outcome, _, err := collect(t, ctx, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled in the chain", err)
	}
	if outcome.Report == nil {
		t.Fatal("the run published no report, so there is nothing to check for an absent field")
	}
	if measured := outcome.Validation.Builds > 0; measured != (outcome.Report.Validation != nil) {
		t.Errorf("the run spent %d validation builds and the report says %+v",
			outcome.Validation.Builds, outcome.Report.Validation)
	}
	if outcome.Report.Validation != nil && outcome.Report.Validation.Builds != outcome.Validation.Builds {
		t.Errorf("validation = %+v, want the %d builds the run spent",
			outcome.Report.Validation, outcome.Validation.Builds)
	}
	if outcome.Report.Timing == nil || len(outcome.Report.Timing.Phases) == 0 {
		t.Errorf("timing = %+v, want the phases the run did finish", outcome.Report.Timing)
	}
	if outcome.Report.Workspace.Snapshot == nil {
		t.Error("workspace.snapshot is absent from a run that had already copied the tree")
	}
	if len(outcome.Report.Mutants) == 0 {
		t.Error("the interrupted run published no mutants, so it was cancelled before the catalogue")
	}
	if err := schemas.Validate(schemas.RunReportV1, mustMarshalReport(t, outcome.Report)); err != nil {
		t.Fatalf("the partial report does not satisfy the schema: %v", err)
	}
}

func TestReportToolchainAndSnapshotFactsMatchTheRun(t *testing.T) {
	t.Parallel()

	outcome, _, err := collect(t, t.Context(), options(t, "killable"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Report == nil {
		t.Fatal("the run published no report")
	}

	toolchain := outcome.Report.Test.Toolchain
	if toolchain == nil {
		t.Fatal("the report names no toolchain")
	}
	if toolchain.GoBin != outcome.Toolchain.GoBin {
		t.Errorf("test.toolchain.go_bin = %q, want the located %q", toolchain.GoBin, outcome.Toolchain.GoBin)
	}
	if toolchain.Version != outcome.Toolchain.Version.String() {
		t.Errorf("test.toolchain.version = %q, want %q", toolchain.Version, outcome.Toolchain.Version.String())
	}
	if !filepath.IsAbs(toolchain.GoBin) {
		t.Errorf("test.toolchain.go_bin = %q, want an absolute path: a name would be re-resolved by a changed PATH",
			toolchain.GoBin)
	}

	resolved := outcome.Report.Test.ResolvedCommand
	command := outcome.Report.Test.Command
	if len(resolved) != len(command) {
		t.Fatalf("test.resolved_command = %v, want the same argv as %v", resolved, command)
	}
	if len(resolved) == 0 || resolved[0] != toolchain.GoBin {
		t.Errorf("test.resolved_command = %v, want it to start with the located toolchain %q", resolved, toolchain.GoBin)
	}
	if !slices.Equal(resolved[1:], command[1:]) {
		t.Errorf("test.resolved_command = %v and command = %v differ in more than the program", resolved, command)
	}

	snapshot := outcome.Report.Workspace.Snapshot
	if snapshot == nil {
		t.Fatal("the report says nothing about the snapshot")
	}
	if snapshot.Files != outcome.SnapshotFiles || snapshot.Files == 0 {
		t.Errorf("workspace.snapshot.files = %d, want the %d the run copied", snapshot.Files, outcome.SnapshotFiles)
	}
	if snapshot.StableDir != outcome.Snapshot.StableDir {
		t.Errorf("workspace.snapshot.stable_dir = %t, want %t", snapshot.StableDir, outcome.Snapshot.StableDir)
	}

	if outcome.Report.Coverage.BuildFallback || outcome.Report.Coverage.UnavailableReason != nil {
		t.Errorf("coverage = %+v, want no fallback on a run whose instrumented binaries compiled",
			outcome.Report.Coverage)
	}
	if outcome.Report.Coverage.Mode != report.CoverageTest {
		t.Errorf("coverage.mode = %q, want %q", outcome.Report.Coverage.Mode, report.CoverageTest)
	}

	if outcome.Report.Validation == nil || outcome.Report.Timing == nil {
		t.Errorf("the report carries validation %v and timing %v, want both",
			outcome.Report.Validation, outcome.Report.Timing)
	}
}
