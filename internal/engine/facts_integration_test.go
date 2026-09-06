// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The run facts of a real run, judged against the run they claim to describe.
//
// What the report says about a run's cost — where its minutes went, which
// toolchain ran it, how many files it copied, which worker executed which
// mutant — is checkable only against a run that really happened. A unit test
// can prove the builder copies what it is handed; only a toolchain can prove
// the engine hands it the run.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/engine/...
//
// The comment above is deliberately not a package doc — integration_test.go
// carries this package's — which is what the blank line below is for.

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

// TestTheReportsTimingMatchesTheTracesPhaseAndStageDurations is the claim that
// makes the report's timing worth reading: it is the recording's own timeline,
// not a second measurement of the same run.
//
// A trace is opt-in, kept for ten runs and thrown away; the report is the
// permanent record, and nearly every reader has only the report. So the two
// have to agree exactly — same names, same durations, same words for what
// became of a stage — because a reader who does have both must not be left
// reconciling two accounts of one run.
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

	// The phases. The report's own `report` phase is still open when the
	// document is built — the run is inside it — so the recording has one
	// phase-end the report cannot have, and every phase the report does carry
	// has to be the recording's.
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

	// The stages, the same way. The report's own `build`, `history` and
	// `artifacts` stages close after the document is built, so the report is a
	// prefix of the recording rather than the whole of it — which is a fact
	// about when a report is written and not a licence to differ.
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
	// The one stage a run of this fixture always has, so that a report whose
	// timing were quietly empty could not pass the comparisons above.
	if !slices.ContainsFunc(timing.Stages, func(s report.StageTiming) bool {
		return s.Phase == trace.PhaseMutate && s.Name == "execute"
	}) {
		t.Errorf("the report's timing has no mutate/execute stage: %+v", timing.Stages)
	}

	// And the validation facts, which are the other half of "why was this
	// slow": the builds a bisection cost. Every catalogued mutant of this
	// fixture compiles, so it is the one build that proves it.
	if outcome.Report.Validation == nil || outcome.Report.Validation.Builds < 1 {
		t.Errorf("validation = %+v, want the builds this run spent", outcome.Report.Validation)
	}
}

// TestEveryExecutedMutantReportsItsExecutions is the per-mutant half of the
// same claim: what the report says about how a mutant was run is what the run
// did to it.
//
// The three shapes are the whole contract. A mutant this run executed carries
// one row per attempt, and their outcomes are what settled its own. A mutant
// coverage settled carries none, because no process was started for it. And a
// mutant a second run adopted from the cache carries none either, while keeping
// the attempt count of the run that did measure it — which is the one place the
// two numbers legitimately differ, and the reason `attempts` is not simply the
// length of the list.
func TestEveryExecutedMutantReportsItsExecutions(t *testing.T) {
	t.Parallel()

	root := testkit.Copy(t, "killable")
	cacheRoot := t.TempDir()
	// Recorded, because the worker each attempt ran in is published twice — as
	// a `mutant-exec` event and in the report — and the two have to be the same
	// number: see the join below.
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
		// Every mutant of this fixture settles first time, so the mutant's own
		// verdict is the last attempt's observation and its killer is that
		// attempt's killer.
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

	// The worker each attempt ran in, joined to the recording by mutant and
	// attempt number. The two are published from one field of one attempt
	// today; this is what would fail if somebody ever passed the number to the
	// recorder from one place and to the report from another, which is how the
	// two would come to disagree without anybody noticing.
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

	// The second run over the same tree, which measures nothing.
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
		// The attempt count is second-hand and reported as it stands, exactly
		// as the duration and the killer beside it are: it is the count the run
		// that measured this mutant recorded, not a count of what this run did.
		// Asserting it against that run's own row is the whole of the claim —
		// "at least one" would pass for a number this run invented.
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

// An attemptKey names one pass over the test binaries in both documents: the
// mutant it was made for, and which pass it was.
type attemptKey struct {
	id      string
	attempt int
}

// cancellingSink is a recorder sink that cancels the run when a named stage
// finishes.
//
// It is the only hook fine enough for what the test below needs. The engine's
// own event stream announces phases and mutants, and the window this is about —
// after the catalogue exists and before the first validation build — is inside
// one phase and before the first mutant. Stages are recorded rather than
// published, so the recording is where a test can stand.
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

// TestAnInterruptedRunReportsOnlyWhatItMeasured is the "nil means not measured"
// rule on a real interrupted run.
//
// A run cancelled once the catalogue exists still publishes a report, because
// the catalogue is worth having: which mutants there are, and that the signal
// cut them short. What it must not do is fill in a fact it never established.
// `validation: {builds: 0}` would be that report claiming it proved which
// mutants compile without compiling anything, and zero is exactly the number a
// reader cannot tell from a measurement.
//
// Which side of that line this run lands on is a race — the signal arrives
// while validation is starting, and whether the first `go build` was counted
// depends on the scheduler — so the assertion is the biconditional rather than
// one of its two sides. Both are real states, and the report has to agree with
// the run about which one it is in. [TestTheFactsARunDidNotMeasureAreAbsent]
// pins the rendering itself, where nothing is racing.
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
	// The document still says everything it does know, which is what makes the
	// absence above a statement rather than a hole.
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

// TestReportToolchainAndSnapshotFactsMatchTheRun checks the facts that say
// *what* ran, against the run that ran.
//
// Both are questions a report could not answer before: which `go` compiled and
// ran the tests — `workspace.go_version` is the module's directive, which under
// a toolchain manager is a different statement — and what the copy of the tree
// cost, which is the difference between two runs of one workspace when one of
// them was slow.
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

	// The resolved command is the recorded command with that executable in
	// place of the bare `go`, and nothing else changed.
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

	// A run that did not fall back says nothing about a fallback, rather than
	// carrying a reason nobody has.
	if outcome.Report.Coverage.BuildFallback || outcome.Report.Coverage.UnavailableReason != nil {
		t.Errorf("coverage = %+v, want no fallback on a run whose instrumented binaries compiled",
			outcome.Report.Coverage)
	}
	if outcome.Report.Coverage.Mode != report.CoveragePackage {
		t.Errorf("coverage.mode = %q, want %q", outcome.Report.Coverage.Mode, report.CoveragePackage)
	}

	// The engine is the only caller of report.Build outside its own tests, so
	// this is also the assertion that it fills in every fact the document has
	// room for.
	if outcome.Report.Validation == nil || outcome.Report.Timing == nil {
		t.Errorf("the report carries validation %v and timing %v, want both",
			outcome.Report.Validation, outcome.Report.Timing)
	}
}
