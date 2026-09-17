// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

const testToolVersion = "0.0.0-test"

func options(t *testing.T, name string) Options {
	t.Helper()
	return optionsAt(t, testkit.Copy(t, name))
}

func optionsAt(t *testing.T, root string) Options {
	t.Helper()
	cfg := config.Defaults()
	cfg.Test.BaselineRuns = 1
	cfg.Execution.Jobs = 1

	private := testkit.Scratch(t)
	temp := filepath.Join(private, "temp")
	if err := os.MkdirAll(temp, 0o755); err != nil {
		t.Fatalf("creating the run's temporary directory: %v", err)
	}
	return Options{
		Config:        cfg,
		WorkspaceRoot: root,
		ToolVersion:   testToolVersion,
		TempDirectory: temp,
		HistoryRoot:   filepath.Join(private, "history"),
		CacheRoot:     filepath.Join(private, "cache"),
		TraceSink:     mutantkit.TraceSink(t),
	}
}

func untraced(opts Options) Options {
	opts.TraceSink = nil
	return opts
}

func collect(t *testing.T, ctx context.Context, opts Options) (RunOutcome, []Event, error) {
	t.Helper()
	return watch(t, ctx, opts, nil)
}

func watch(t *testing.T, ctx context.Context, opts Options, saw func(Event)) (RunOutcome, []Event, error) {
	t.Helper()
	events := make(chan Event, 64)
	done := make(chan []Event, 1)
	go func() {
		var seen []Event
		for e := range events {
			if saw != nil {
				saw(e)
			}
			seen = append(seen, e)
		}
		done <- seen
	}()
	opts.Events = events
	outcome, err := Run(ctx, opts)
	return outcome, <-done, err
}

func kinds(events []Event) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, fmt.Sprintf("%T", e))
	}
	return names
}

func results(events []Event) []string {
	var out []string
	for _, e := range events {
		if finished, ok := e.(MutantFinished); ok {
			m := finished.Result
			out = append(out, fmt.Sprintf("%s %s:%d %s", m.Outcome, m.Path, m.Line, m.Rule))
		}
	}
	return out
}

func runDirNames(dir string) []string {
	found, _ := os.ReadDir(dir)
	var mine []string
	for _, e := range found {
		if e.IsDir() && strings.HasPrefix(e.Name(), runDirPrefix) {
			mine = append(mine, e.Name())
		}
	}
	slices.Sort(mine)
	return mine
}

const runDirPrefix = "go-mutants-"

func TestTempDirectoryIsWhereTheRunSnapshotsAndSweeps(t *testing.T) {
	opts := options(t, "simple")
	parent := opts.TempDirectory
	system := t.TempDir()
	t.Setenv("TMPDIR", system)
	t.Setenv("TMP", system)
	t.Setenv("TEMP", system)

	orphan := abandonedDirectory(t, parent, snapshot.DirPrefix+"orphan")
	if before := runDirNames(system); len(before) != 0 {
		t.Fatalf("the redirected temporary directory already held %v", before)
	}

	var duringMine, duringSystem []string
	saw := func(e Event) {
		if _, ok := e.(BaselineCompleted); !ok {
			return
		}
		duringMine = runDirNames(parent)
		duringSystem = runDirNames(system)
	}
	outcome, _, err := watch(t, t.Context(), opts, saw)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	if len(duringMine) != 2 {
		t.Errorf("the temporary directory the run was given held %v mid-run, "+
			"want the snapshot and the scratch directory beside it", duringMine)
	}
	if len(duringSystem) != 0 {
		t.Errorf("the run put %v in the operating system's temporary directory, "+
			"which it was told nothing about", duringSystem)
	}

	owned := filepath.Dir(outcome.SnapshotRoot)
	if filepath.Base(outcome.SnapshotRoot) != snapshot.TreeName || !testkit.SamePath(filepath.Dir(owned), parent) {
		t.Errorf("the snapshot was created at %s, want %s below a directory the run owns in %s",
			outcome.SnapshotRoot, snapshot.TreeName, parent)
	}
	if _, statErr := os.Stat(orphan); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("the orphan %s in the run's own temporary directory was not swept (stat error %v)",
			orphan, statErr)
	}
	if left := testkit.Entries(t, parent); len(left) != 0 {
		t.Errorf("the run left %v behind in the temporary directory it was given", left)
	}
	if after := runDirNames(system); len(after) != 0 {
		t.Errorf("the run left %v in the operating system's temporary directory", after)
	}
	if len(outcome.Warnings) != 0 {
		t.Errorf("a clean run published %v", outcome.Warnings)
	}
}

func TestABaselineThatRanTwiceSizesTheBudgetOnTheSecondRun(t *testing.T) {
	t.Parallel()
	opts := options(t, "simple")
	opts.Config.Test.BaselineRuns = 2
	opts.Config.Test.Command = []string{"go", "test", "-count=1", "./..."}

	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(outcome.BaselineRuns) != 2 {
		t.Fatalf("measured %d baseline runs, want 2", len(outcome.BaselineRuns))
	}
	if outcome.SlowestBaseline != outcome.BaselineRuns[1] {
		t.Errorf("SlowestBaseline = %s, want %s, the run after the one that compiled (runs %v)",
			outcome.SlowestBaseline, outcome.BaselineRuns[1], outcome.BaselineRuns)
	}
	if want := max(MinDerivedTimeout, TimeoutFactor*outcome.BaselineRuns[1]); outcome.Timeout != want {
		t.Errorf("timeout = %s, want max(%s, 5 x %s) = %s",
			outcome.Timeout, MinDerivedTimeout, outcome.BaselineRuns[1], want)
	}
	if _, found := warningOf(outcome, CodeBaselineFromTestCache); found {
		t.Errorf("a baseline neither run of which was cached published %s", CodeBaselineFromTestCache)
	}
}

func TestRunMeasuresTheBaselineAndDerivesTheTimeout(t *testing.T) {
	t.Parallel()
	opts := options(t, "simple")
	opts.Config.Test.BaselineRuns = 2

	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if outcome.Status != StatusOK {
		t.Errorf("status = %s, want %s", outcome.Status, StatusOK)
	}
	if len(outcome.BaselineRuns) != 2 {
		t.Fatalf("measured %d baseline runs, want 2", len(outcome.BaselineRuns))
	}
	for i, d := range outcome.BaselineRuns {
		if d <= 0 {
			t.Errorf("baseline run %d took %s, want a positive duration", i+1, d)
		}
	}

	slowest := outcome.BaselineRuns[1]
	if outcome.SlowestBaseline != slowest {
		t.Errorf("SlowestBaseline = %s, want %s, the run after the one that compiled (runs %v); "+
			"the first is what a cache lookup in the second would have left",
			outcome.SlowestBaseline, slowest, outcome.BaselineRuns)
	}
	if _, found := warningOf(outcome, CodeBaselineFromTestCache); found {
		t.Errorf("a baseline both of whose runs ran published %s", CodeBaselineFromTestCache)
	}
	want := max(MinDerivedTimeout, TimeoutFactor*slowest)
	if outcome.Timeout != want {
		t.Errorf("timeout = %s, want max(%s, 5 x %s) = %s", outcome.Timeout, MinDerivedTimeout, slowest, want)
	}
	if outcome.TimeoutSource != TimeoutDerived {
		t.Errorf("timeout source = %s, want %s", outcome.TimeoutSource, TimeoutDerived)
	}

	if !slices.Equal(outcome.TestCommand, config.DefaultTestCommand()) {
		t.Errorf("test command = %q, want the configured default", outcome.TestCommand)
	}
	if len(outcome.WorkspaceDigest) != 64 {
		t.Errorf("workspace digest = %q, want 64 hex characters", outcome.WorkspaceDigest)
	}
	if outcome.SnapshotFiles != 3 {
		t.Errorf("snapshotted %d files, want 3 (go.mod, simple.go, simple_test.go)", outcome.SnapshotFiles)
	}
	if outcome.Toolchain.GoBin == "" || outcome.Toolchain.Version.Raw == "" {
		t.Errorf("toolchain = %+v, want a located one", outcome.Toolchain)
	}

	const simpleMutants = 13
	if got := len(outcome.Report.Mutants); got != simpleMutants {
		t.Fatalf("the simple fixture produced %d mutants, want %d", got, simpleMutants)
	}

	wantKinds := []string{
		"engine.RunPlanned",
		"engine.PhaseChanged",
		"engine.PhaseCompleted",
		"engine.PhaseChanged",
		"engine.BaselineProgress",
		"engine.BaselineProgress",
		"engine.BaselineCompleted",
		"engine.MemoryDerived",
		"engine.PhaseCompleted",
		"engine.PhaseChanged",
		"engine.Discovered",
		"engine.Validated",
		"engine.BaselineProgress",
		"engine.CoverageMapped",
	}
	for range simpleMutants {
		wantKinds = append(wantKinds, "engine.MutantStarted", "engine.MutantFinished")
	}
	wantKinds = append(wantKinds,
		"engine.PhaseCompleted",
		"engine.PhaseChanged",
		"engine.ReportPublished",
		"engine.PhaseCompleted",
		"engine.RunCompleted",
	)
	if got := kinds(events); !slices.Equal(got, wantKinds) {
		t.Fatalf("event sequence =\n\t%s\nwant\n\t%s",
			strings.Join(got, "\n\t"), strings.Join(wantKinds, "\n\t"))
	}

	if planned := events[0].(RunPlanned); planned.RunID != outcome.RunID || planned.Workers != 1 {
		t.Errorf("RunPlanned = %+v, want run %s and 1 worker", planned, outcome.RunID)
	}
	for _, span := range []struct {
		phase         Phase
		entered, left int
	}{
		{PhaseDiscover, 1, 2},
		{PhaseBaseline, 3, 8},
	} {
		if got := events[span.entered].(PhaseChanged); got.Phase != span.phase || got.Detail == "" {
			t.Errorf("event %d = %+v, want a described %s", span.entered, got, span.phase)
		}
		got := events[span.left].(PhaseCompleted)
		if got.Phase != span.phase || got.Duration < 0 {
			t.Errorf("event %d = %+v, want %s with a measured span", span.left, got, span.phase)
		}
	}
	for i, index := range []int{4, 5} {
		progress := events[index].(BaselineProgress)
		if progress.Run != i+1 || progress.Of != 2 {
			t.Errorf("progress %d = %+v, want run %d of 2", index, progress, i+1)
		}
		if progress.Duration != outcome.BaselineRuns[i] {
			t.Errorf("progress %d reported %s, outcome recorded %s", index, progress.Duration, outcome.BaselineRuns[i])
		}
	}
	completed := events[6].(BaselineCompleted)
	if completed.Timeout != outcome.Timeout || completed.TimeoutSource != outcome.TimeoutSource {
		t.Errorf("BaselineCompleted = %+v, want the outcome's timeout %s (%s)", completed, outcome.Timeout, outcome.TimeoutSource)
	}
	if !slices.Equal(completed.Runs, outcome.BaselineRuns) {
		t.Errorf("BaselineCompleted.Runs = %v, want %v", completed.Runs, outcome.BaselineRuns)
	}
	if derived := events[7].(MemoryDerived); derived.Limit != outcome.Memory || derived.Source != outcome.MemorySource {
		t.Errorf("MemoryDerived = %+v, want the outcome's bound %d (%s)",
			derived, outcome.Memory, outcome.MemorySource)
	}
	if instrumented := events[12].(BaselineProgress); instrumented.Run != 1 || instrumented.Of != 1 {
		t.Errorf("the instrumented baseline reported %+v, want run 1 of 1", instrumented)
	}
	if mapped := events[13].(CoverageMapped); mapped.Binaries != 1 || mapped.Covered != simpleMutants || mapped.Uncovered != 0 {
		t.Errorf("CoverageMapped = %+v, want 1 binary covering all %d mutants", mapped, simpleMutants)
	}
	if mode := outcome.Report.Coverage.Mode; mode != report.CoverageTest {
		t.Errorf("coverage mode = %q, want %q with the built-in test command", mode, report.CoverageTest)
	}
	if final := events[len(events)-1].(RunCompleted); final.Status != StatusOK || final.Run == nil {
		t.Errorf("RunCompleted = %+v, want an ok run carrying its summary", final)
	}
	if len(outcome.Warnings) != 0 {
		t.Errorf("a clean run published %v", outcome.Warnings)
	}

}

func TestKillableRunReachesTheFixturesPredeterminedFates(t *testing.T) {
	t.Parallel()
	outcome, events, err := collect(t, t.Context(), options(t, "killable"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	want := []string{
		"killed clamp.go:41 lt-to-le",
		"killed clamp.go:41 negate-condition",
		"killed clamp.go:42 gt-to-ge",
		"killed clamp.go:42 negate-condition",
		"killed clamp.go:43 return-zero-numeric",
		"killed clamp.go:45 add-to-sub",
		"killed clamp.go:45 return-zero-numeric",
		"killed clamp.go:47 return-zero-numeric",
		"killed clamp.go:47 sub-to-add",
		"killed ready.go:14 true-to-false",
		"survived untested.go:14 neq-to-eq",
		"survived untested.go:14 return-false",
		"survived untested.go:14 return-true",
	}
	got := results(events)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("results =\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}

	summary := outcome.Report.Summary
	if summary.Total != 13 || summary.Killed != 10 || summary.Survived != 3 {
		t.Errorf("summary = %+v, want 13 mutants, 10 killed, 3 survived", summary)
	}
	if summary.NotRun != 0 || summary.Errored != 0 || summary.Inconclusive != 0 || summary.TimedOut != 0 {
		t.Errorf("summary = %+v, want every mutant settled as killed or survived", summary)
	}
	if want := float64(10) / float64(13) * 100; summary.ScorePercent == nil || *summary.ScorePercent != want {
		t.Errorf("score = %v, want %v (10 of 13)", summary.ScorePercent, want)
	}
	if len(outcome.Report.Rejected) != 0 {
		t.Errorf("rejected = %+v, want none: every guard in this fixture compiles", outcome.Report.Rejected)
	}

	if got := outcome.Report.Coverage.MutantsUncovered; got == nil || *got != 3 {
		t.Errorf("coverage.mutants_uncovered = %v, want 3", got)
	}
	for _, survivor := range survivorsOf(t, outcome.Report) {
		if !survivor.Uncovered {
			t.Errorf("the survivor %s in %s is not marked uncovered", survivor.DisplayID, survivor.Path)
		}
		if survivor.Attempts != 0 {
			t.Errorf("the uncovered survivor %s was executed %d times", survivor.DisplayID, survivor.Attempts)
		}
	}
	for _, m := range outcome.Report.Mutants {
		if m.Uncovered == (len(m.CoveringTestPackages) > 0) {
			t.Errorf("mutant %s: uncovered = %t with covering packages %v",
				m.DisplayID, m.Uncovered, m.CoveringTestPackages)
		}
	}

	if outcome.Verdict.Code != mutation.ExitOK {
		t.Errorf("verdict = %+v, want exit 0 without --strict", outcome.Verdict)
	}

	if _, err := os.Stat(outcome.RunPath); err != nil {
		t.Errorf("the run report at %s cannot be opened: %v", outcome.RunPath, err)
	}
	if _, err := os.Stat(outcome.LatestPath); err != nil {
		t.Errorf("the latest pointer at %s cannot be opened: %v", outcome.LatestPath, err)
	}
	published := published(t, events)
	if published.RunPath != outcome.RunPath || published.LatestPath != outcome.LatestPath {
		t.Errorf("ReportPublished = %+v, want the outcome's paths %s and %s",
			published, outcome.RunPath, outcome.LatestPath)
	}
}

func TestVetSuspectGuardShapesStillReachExecution(t *testing.T) {
	t.Parallel()
	outcome, events, err := collect(t, t.Context(), options(t, "vetsuspect"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	want := []string{
		"killed vetsuspect.go:61 eq-to-neq",
		"killed vetsuspect.go:61 eq-to-neq",
		"killed vetsuspect.go:61 or-to-and",
		"killed vetsuspect.go:61 return-false",
		"killed vetsuspect.go:61 return-true",
		"killed vetsuspect.go:72 and-to-or",
		"killed vetsuspect.go:72 neq-to-eq",
		"killed vetsuspect.go:72 neq-to-eq",
		"killed vetsuspect.go:72 return-false",
		"killed vetsuspect.go:72 return-true",
	}
	got := results(events)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("results =\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}

	summary := outcome.Report.Summary
	if summary.Total != 10 || summary.Killed != 10 || summary.Survived != 0 {
		t.Errorf("summary = %+v, want 10 mutants, all killed", summary)
	}
	if summary.NotRun != 0 || summary.Errored != 0 || summary.Inconclusive != 0 || summary.TimedOut != 0 {
		t.Errorf("summary = %+v, want every mutant settled by executing it", summary)
	}
	if len(outcome.Report.Rejected) != 0 {
		t.Errorf("rejected = %+v, want none: the suspect shapes are legal Go", outcome.Report.Rejected)
	}

	if uncovered := outcome.Report.Coverage.MutantsUncovered; uncovered == nil || *uncovered != 0 {
		t.Errorf("coverage.mutants_uncovered = %v, want 0: both functions are called by the tests", uncovered)
	}
	connectives := 0
	for _, m := range outcome.Report.Mutants {
		if m.Attempts == 0 || m.Uncovered {
			t.Errorf("mutant %s (%s at %s:%d) was never executed: attempts %d, uncovered %t",
				m.DisplayID, m.Rule, m.Path, m.Line, m.Attempts, m.Uncovered)
		}
		if m.Family == string(mutation.FamilyBooleanConnective) {
			connectives++
		}
	}
	if connectives != 2 {
		t.Errorf("the run catalogued %d boolean-connective mutants, want the fixture's 2 vet-suspect ones", connectives)
	}
}

func TestParallelWorkersReachTheSameTally(t *testing.T) {
	t.Parallel()
	opts := options(t, "killable")
	opts.Config.Execution.Jobs = 4

	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{
		"killed clamp.go:41 lt-to-le",
		"killed clamp.go:41 negate-condition",
		"killed clamp.go:42 gt-to-ge",
		"killed clamp.go:42 negate-condition",
		"killed clamp.go:43 return-zero-numeric",
		"killed clamp.go:45 add-to-sub",
		"killed clamp.go:45 return-zero-numeric",
		"killed clamp.go:47 return-zero-numeric",
		"killed clamp.go:47 sub-to-add",
		"killed ready.go:14 true-to-false",
		"survived untested.go:14 neq-to-eq",
		"survived untested.go:14 return-false",
		"survived untested.go:14 return-true",
	}
	got := results(events)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("four workers reached\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
	summary := outcome.Report.Summary
	if summary.Total != 13 || summary.Killed != 10 || summary.Survived != 3 {
		t.Errorf("summary = %+v, want the same 13/10/3 the serial run produces", summary)
	}
	if len(got) != 13 {
		t.Errorf("the run published %d results for 13 mutants", len(got))
	}
}

func TestStrictFailsOnTheSurvivorItWasNotToldAbout(t *testing.T) {
	t.Parallel()
	opts := options(t, "killable")
	opts.Config.Policy.Strict = true

	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Verdict.Code != mutation.ExitPolicyFailure {
		t.Fatalf("verdict = %+v, want exit 1 under --strict", outcome.Verdict)
	}
	if !outcome.Verdict.Has(mutation.ReasonUnexpectedSurvivors) {
		t.Errorf("verdict reasons = %v, want the unexpected survivor", outcome.Verdict.Reasons())
	}
	if failure := outcome.Report.Summary.Policy.Failure; failure == nil ||
		*failure != string(mutation.ReasonUnexpectedSurvivors) {
		t.Errorf("the report names %v as the policy failure, want %s", failure, mutation.ReasonUnexpectedSurvivors)
	}
}

func TestMutantSelectsExactlyOne(t *testing.T) {
	t.Parallel()

	first, _, err := collect(t, t.Context(), options(t, "killable"))
	if err != nil {
		t.Fatalf("the run that sources the id: %v", err)
	}
	survivor := survivorOf(t, first.Report, "neq-to-eq")

	opts := options(t, "killable")
	opts.MutantPrefix = survivor.DisplayID
	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run --mutant %s: %v", survivor.DisplayID, err)
	}

	if got := results(events); len(got) != 1 || !strings.HasPrefix(got[0], "survived untested.go") {
		t.Fatalf("executed %v, want only the survivor in untested.go", got)
	}
	summary := outcome.Report.Summary
	if summary.Total != 13 || summary.Survived != 1 || summary.NotRun != 12 {
		t.Errorf("summary = %+v, want 13 catalogued, 1 measured, 12 not run", summary)
	}
	if mode := outcome.Report.Selection.Mode; mode != report.ModeMutant {
		t.Errorf("selection mode = %s, want %s", mode, report.ModeMutant)
	}
	if selected := outcome.Report.Selection.Selected; selected != 1 {
		t.Errorf("selected = %d, want 1", selected)
	}
	if candidates := outcome.Report.Selection.Candidates; candidates != 13 {
		t.Errorf("candidates = %d, want the whole catalogue of 13", candidates)
	}
}

func TestMutantThatSelectsNothingIsRefused(t *testing.T) {
	t.Parallel()
	opts := options(t, "killable")
	opts.MutantPrefix = strings.Repeat("0", 32)

	_, _, err := collect(t, t.Context(), opts)
	var selection *SelectionError
	if !errors.As(err, &selection) {
		t.Fatalf("Run = %v, want a SelectionError", err)
	}
	if !errors.Is(err, mutation.ErrMutantNotFound) {
		t.Errorf("error = %v, want the catalogue's own sentinel in the chain", err)
	}
	if code := CodeOf(err); code != "" {
		t.Errorf("the selection error carries code %s, want none", code)
	}
}

func TestExpectedSurvivorLeavesAStrictRunGreen(t *testing.T) {
	t.Parallel()
	first, _, err := collect(t, t.Context(), options(t, "killable"))
	if err != nil {
		t.Fatalf("the run that sources the id: %v", err)
	}
	survivors := survivorsOf(t, first.Report)
	expect := make([]config.Expectation, 0, len(survivors))
	for _, survivor := range survivors {
		expect = append(expect, config.Expectation{
			ID:     survivor.ID,
			Reason: "Untested is deliberately uncovered; these are the fixture's survivors",
		})
	}

	opts := options(t, "killable")
	opts.Config.Policy.Strict = true
	opts.Config.Mutation.Expect = expect

	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Verdict.Code != mutation.ExitOK {
		t.Fatalf("verdict = %+v, want exit 0: every survivor is accounted for", outcome.Verdict)
	}
	if len(outcome.Report.Expectations) != len(expect) {
		t.Fatalf("expectations = %+v, want %d rows", outcome.Report.Expectations, len(expect))
	}
	for _, row := range outcome.Report.Expectations {
		if row.State != report.StateFulfilled {
			t.Fatalf("expectation %+v is not fulfilled", row)
		}
	}
	if score := outcome.Report.Summary.ScorePercent; score == nil || *score != 100 {
		t.Errorf("score = %v, want 100: the expected survivor is out of the denominator", score)
	}
}

func TestExpectingAKilledMutantIsAContractFailure(t *testing.T) {
	t.Parallel()
	first, _, err := collect(t, t.Context(), options(t, "killable"))
	if err != nil {
		t.Fatalf("the run that sources the id: %v", err)
	}
	killed := killedOf(t, first.Report)

	opts := options(t, "killable")
	opts.Config.Mutation.Expect = []config.Expectation{{
		ID:     killed.ID,
		Reason: "out of date: this mutant is caught now",
	}}

	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Verdict.Code != mutation.ExitInfrastructure {
		t.Fatalf("verdict = %+v, want exit 2 for a ledger that stopped describing reality", outcome.Verdict)
	}
	if !outcome.Verdict.Has(mutation.ReasonExpectationFailure) {
		t.Errorf("verdict reasons = %v, want the expectation failure", outcome.Verdict.Reasons())
	}
	if len(outcome.Report.Expectations) != 1 || outcome.Report.Expectations[0].State != report.StateUnfulfilled {
		t.Errorf("expectations = %+v, want one unfulfilled row", outcome.Report.Expectations)
	}
	final := events[len(events)-1].(RunCompleted)
	if final.Run == nil || final.Run.Failure.Reason != mutation.ReasonExpectationFailure {
		t.Fatalf("the closing summary = %+v, want it to name the expectation failure", final.Run)
	}
	if final.Run.Failure.Detail == "" {
		t.Error("the named gate carries no explanation")
	}
}

func TestCancellationMidRunStillPublishesAPartialReport(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	opts := options(t, "killable")
	outcome, events, err := watch(t, ctx, opts, func(e Event) {
		if finished, ok := e.(MutantFinished); ok && !finished.Result.Uncovered {
			cancel()
		}
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled in the chain", err)
	}
	if outcome.Status != StatusInterrupted {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusInterrupted)
	}
	if outcome.Report == nil {
		t.Fatal("an interrupted run published no report, so everything it measured was thrown away")
	}
	if outcome.Report.Status != report.StatusInterrupted {
		t.Errorf("report status = %s, want %s", outcome.Report.Status, report.StatusInterrupted)
	}
	summary := outcome.Report.Summary
	if summary.Total != 13 {
		t.Errorf("summary total = %d, want the whole catalogue of 13", summary.Total)
	}
	if summary.NotRun == 0 {
		t.Errorf("summary = %+v, want the mutants the signal cut short recorded as not-run", summary)
	}
	if summary.Killed+summary.TimedOut+summary.Inconclusive+summary.Errored == 0 {
		t.Errorf("summary = %+v, want at least the one executed mutant that triggered the cancellation", summary)
	}
	if _, err := os.Stat(outcome.RunPath); err != nil {
		t.Errorf("the partial report at %s cannot be opened: %v", outcome.RunPath, err)
	}

	names := kinds(events)
	if len(names) == 0 || names[len(names)-1] != "engine.RunCompleted" {
		t.Fatalf("event sequence = %v, want it to end with RunCompleted", names)
	}
	if !slices.Contains(names, "engine.ReportPublished") {
		t.Error("the interrupted run never announced its report")
	}
	if final := events[len(events)-1].(RunCompleted); final.Run == nil {
		t.Error("the terminal event carries no summary, so a renderer has nothing to close with")
	}
	if _, err := os.Stat(outcome.SnapshotRoot); !os.IsNotExist(err) {
		t.Errorf("the snapshot at %s survived an interrupted run (stat error %v)", outcome.SnapshotRoot, err)
	}
}

func TestRejectableRunReportsWhatWillNotCompile(t *testing.T) {
	t.Parallel()
	outcome, events, err := collect(t, t.Context(), options(t, "rejectable"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s: a rejection is data, not a failure", outcome.Status, StatusOK)
	}

	if got := len(outcome.Report.Rejected); got != 3 {
		t.Fatalf("rejected %d mutants, want the fixture's 3 traps: %+v", got, outcome.Report.Rejected)
	}
	for _, rejection := range outcome.Report.Rejected {
		if strings.TrimSpace(rejection.Diagnostic) == "" {
			t.Errorf("rejected mutant %s carries no diagnostic", rejection.DisplayID)
		}
		if rejection.Line <= 0 || rejection.Path == "" {
			t.Errorf("rejected mutant %s has no coordinates: %+v", rejection.DisplayID, rejection)
		}
	}

	summary := outcome.Report.Summary
	if summary.Total != 18 {
		t.Errorf("summary total = %d, want the 18 candidates that compile", summary.Total)
	}
	if summary.ScorePercent == nil || *summary.ScorePercent != 100 {
		t.Errorf("score = %v, want 100 over the eighteen that compile", summary.ScorePercent)
	}
	if len(outcome.Report.Mutants) != 18 {
		t.Errorf("the report holds %d executed mutants, want 18", len(outcome.Report.Mutants))
	}

	validated := validatedOf(t, events)
	if validated.Accepted != 18 || validated.Rejected != 3 {
		t.Errorf("Validated = %+v, want 18 accepted and 3 rejected", validated)
	}

	for _, rejection := range outcome.Report.Rejected {
		if rejection.Path == "named.go" {
			t.Errorf("the named boolean candidate %s (%s) was rejected again: %s",
				rejection.DisplayID, rejection.Rule, firstLine(rejection.Diagnostic))
		}
	}
	named := 0
	for _, m := range outcome.Report.Mutants {
		if m.Path != "named.go" {
			continue
		}
		named++
		if m.Outcome != report.OutcomeKilled {
			t.Errorf("the named boolean mutant %s (%s) settled as %s, want killed",
				m.DisplayID, m.Rule, m.Outcome)
		}
		if m.Attempts == 0 {
			t.Errorf("the named boolean mutant %s (%s) was never executed", m.DisplayID, m.Rule)
		}
	}
	if named != 6 {
		t.Errorf("the report holds %d mutants in named.go, want the fixture's 6", named)
	}
}

func TestMutantThatWasRejectedSaysSo(t *testing.T) {
	t.Parallel()

	first, _, err := collect(t, t.Context(), options(t, "rejectable"))
	if err != nil {
		t.Fatalf("the run that sources the id: %v", err)
	}
	if len(first.Report.Rejected) == 0 {
		t.Fatal("the rejectable fixture rejected nothing, so this cannot be exercised")
	}
	target := first.Report.Rejected[0]

	opts := options(t, "rejectable")
	opts.MutantPrefix = target.DisplayID
	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run --mutant %s: %v", target.DisplayID, err)
	}

	warning, found := warningWith(events, CodeSelectedMutantRejected)
	if !found {
		t.Fatalf("no %s warning; the run selected a rejected mutant in silence. warnings: %+v",
			CodeSelectedMutantRejected, warningsOf(events))
	}
	for _, needle := range []string{
		strconv.Quote(target.DisplayID),
		target.Path + ":" + strconv.Itoa(target.Line) + ":" + strconv.Itoa(target.Column),
		target.Rule,
		firstLine(strings.TrimSpace(target.Diagnostic)),
	} {
		if needle == "" {
			t.Fatalf("the fixture's rejection is missing a field this asserts on: %+v", target)
		}
		if !strings.Contains(warning.Message, needle) {
			t.Errorf("the warning does not mention %q:\n%s", needle, warning.Message)
		}
	}
	if strings.ContainsAny(warning.Message, "\n\r") {
		t.Errorf("the warning is not one line: %q", warning.Message)
	}

	filed := false
	for _, w := range outcome.Report.Warnings {
		if w.Code == string(CodeSelectedMutantRejected) && w.Message == warning.Message {
			filed = true
		}
	}
	if !filed {
		t.Errorf("the warning is not in the filed report: %+v", outcome.Report.Warnings)
	}
	document, err := os.ReadFile(published(t, events).RunPath)
	if err != nil {
		t.Fatalf("reading the filed report: %v", err)
	}
	validateDocument(t, document)

	if outcome.Status != StatusOK || outcome.Verdict.Code != mutation.ExitOK {
		t.Errorf("status %s verdict %+v, want an ok run: a rejection is data, not a failure",
			outcome.Status, outcome.Verdict)
	}
	if got := results(events); len(got) != 0 {
		t.Errorf("executed %v, want nothing: the one mutant asked about cannot be built", got)
	}
	selection := outcome.Report.Selection
	if selection.Mode != report.ModeMutant || selection.Selected != 0 {
		t.Errorf("selection = %+v, want mode %s and 0 selected", selection, report.ModeMutant)
	}
	if summary := outcome.Report.Summary; summary.Total != 18 || summary.NotRun != 18 {
		t.Errorf("summary = %+v, want the 18 that compile, all not-run", summary)
	}
	if score := outcome.Report.Summary.ScorePercent; score != nil {
		t.Errorf("score = %v, want none: nothing was measured", *score)
	}
}

func TestRunStopsOnAFailingBaseline(t *testing.T) {
	t.Parallel()
	outcome, events, err := collect(t, t.Context(), options(t, "failing-baseline"))
	if err == nil {
		t.Fatal("Run succeeded against a workspace whose tests fail")
	}
	if got := CodeOf(err); got != CodeBaselineTestFailed {
		t.Fatalf("error code = %s, want %s (error: %v)", got, CodeBaselineTestFailed, err)
	}
	if outcome.Status != StatusFailed {
		t.Errorf("status = %s, want %s", outcome.Status, StatusFailed)
	}
	if outcome.Report != nil {
		t.Error("a run that never catalogued anything published a report claiming the workspace holds no mutants")
	}

	output := OutputOf(err)
	if output == "" {
		t.Fatal("the baseline error carries no output tail")
	}
	for _, needle := range []string{"FAIL", "this fixture fails on purpose"} {
		if !strings.Contains(output, needle) {
			t.Errorf("the output tail does not contain %q:\n%s", needle, output)
		}
	}
	if strings.Contains(output, "\r") {
		t.Error("the output tail still carries carriage returns")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("the error message is not one line: %q", err.Error())
	}

	names := kinds(events)
	if len(names) == 0 || names[len(names)-1] != "engine.RunCompleted" {
		t.Fatalf("event sequence = %v, want it to end with RunCompleted", names)
	}
	if final := events[len(events)-1].(RunCompleted); final.Status != StatusFailed {
		t.Errorf("RunCompleted status = %s, want %s", final.Status, StatusFailed)
	}
	if _, err := os.Stat(outcome.SnapshotRoot); !os.IsNotExist(err) {
		t.Errorf("the snapshot at %s survived a failed run (stat error %v)", outcome.SnapshotRoot, err)
	}
}

func TestMutationExcludeChangesNeitherTheSnapshotNorItsDigest(t *testing.T) {
	t.Parallel()
	plain, _, err := collect(t, t.Context(), options(t, "simple"))
	if err != nil {
		t.Fatalf("Run with no excludes: %v", err)
	}

	excluded := options(t, "simple")
	excluded.Config.Mutation.Exclude = []string{"**/*_test.go", "**/simple.go", "**/testdata/**"}

	selective, _, err := collect(t, t.Context(), excluded)
	if err != nil {
		t.Fatalf("Run with mutation.exclude set: %v", err)
	}

	if selective.SnapshotFiles != plain.SnapshotFiles {
		t.Errorf("mutation.exclude changed the snapshot from %d files to %d: a selection setting must not shrink the tree that gets built",
			plain.SnapshotFiles, selective.SnapshotFiles)
	}
	if selective.WorkspaceDigest != plain.WorkspaceDigest {
		t.Errorf("mutation.exclude changed the workspace digest from %s to %s: the digest describes the code, not the selection",
			plain.WorkspaceDigest, selective.WorkspaceDigest)
	}
	if got := len(selective.Report.Mutants); got != 0 {
		t.Errorf("mutation.exclude left %d mutants, want none: the only mutable file was excluded", got)
	}
}

func TestMutationExcludeCannotHideAFailingBaseline(t *testing.T) {
	t.Parallel()
	opts := options(t, "failing-baseline")
	opts.Config.Mutation.Exclude = []string{"**/*_test.go"}

	outcome, _, err := collect(t, t.Context(), opts)
	if err == nil {
		t.Fatal("Run succeeded against a red suite that mutation.exclude named: the baseline gate ran no tests")
	}
	if got := CodeOf(err); got != CodeBaselineTestFailed {
		t.Fatalf("error code = %s, want %s (error: %v)", got, CodeBaselineTestFailed, err)
	}
	if output := OutputOf(err); !strings.Contains(output, "this fixture fails on purpose") {
		t.Errorf("the output tail does not show the excluded test running:\n%s", output)
	}
	if outcome.Status != StatusFailed {
		t.Errorf("status = %s, want %s", outcome.Status, StatusFailed)
	}
}

func TestExplicitTimeoutBelowTheBaselineIsRefused(t *testing.T) {
	t.Parallel()
	opts := options(t, "simple")
	opts.Config.Test.Timeout = time.Nanosecond
	opts.TestArgv = []string{"go", "test", "-count=1", "./..."}

	outcome, _, err := collect(t, t.Context(), opts)
	if got := CodeOf(err); got != CodeTimeoutTooSmall {
		t.Fatalf("error code = %s, want %s (error: %v)", got, CodeTimeoutTooSmall, err)
	}
	if !slices.Equal(outcome.TestCommand, opts.TestArgv) {
		t.Errorf("test command = %q, want the passthrough %q", outcome.TestCommand, opts.TestArgv)
	}
	if len(outcome.BaselineRuns) != 1 {
		t.Errorf("measured %d baseline runs, want 1 before the rejection", len(outcome.BaselineRuns))
	}
}

func TestCancellationBeforeAnythingIsCataloguedPublishesNothing(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	outcome, events, err := collect(t, ctx, options(t, "simple"))
	if err == nil {
		t.Fatal("Run succeeded with an already cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled in the chain", err)
	}
	if outcome.Status != StatusInterrupted {
		t.Errorf("status = %s, want %s", outcome.Status, StatusInterrupted)
	}
	if outcome.Report != nil {
		t.Error("a run cancelled before discovery filed a report claiming the workspace holds no mutants")
	}
	names := kinds(events)
	if len(names) == 0 || names[len(names)-1] != "engine.RunCompleted" {
		t.Fatalf("event sequence = %v, want it to end with RunCompleted", names)
	}
}

func TestCommandLineEndToEnd(t *testing.T) {
	t.Parallel()
	goBin := testkit.GoBinary(t)
	repo := testkit.Root(t)

	binary := filepath.Join(t.TempDir(), "go-mutants")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(t.Context(), goBin, "build", "-o", binary, "./cmd/go-mutants")
	build.Dir = repo
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("building cmd/go-mutants: %v\n%s", buildErr, out)
	}

	versionOut, err := exec.CommandContext(t.Context(), binary, "--version").Output()
	if err != nil {
		t.Fatalf("go-mutants --version: %v", err)
	}
	version := strings.TrimSpace(string(versionOut))
	if !strings.HasPrefix(version, "go-mutants ") {
		t.Fatalf("--version printed %q, want a `go-mutants <version>` line", version)
	}

	run := exec.CommandContext(t.Context(), binary, "run", "--strict")
	run.Dir = testkit.Copy(t, "killable")
	cache := t.TempDir()
	run.Env = append(childEnv(t.TempDir()),
		"NO_COLOR=1", "LOCALAPPDATA="+cache, "XDG_CACHE_HOME="+cache, "HOME="+cache)

	var stdout, stderr strings.Builder
	run.Stdout = &stdout
	run.Stderr = &stderr
	runErr := run.Run()
	code := run.ProcessState.ExitCode()
	if code != int(mutation.ExitPolicyFailure) {
		t.Fatalf("exit = %d (%v), want %d for a survivor under --strict\nstdout:\n%s\nstderr:\n%s",
			code, runErr, mutation.ExitPolicyFailure, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, needle := range []string{
		version + " (run ",
		"phase discover:",
		"phase baseline:",
		"baseline ok: avg ",
		"(derived)",
		"phase mutate:",
		"discovered 14 candidates",
		"validated 13 mutants, 0 rejections",
		"phase report:",
		"report run: ",
		"report latest: ",
		"SURVIVED (uncovered)  ",
		"untested.go:14:11  neq-to-eq  != -> ==",
		"    - !=",
		"    + ==",
		"mutants 13  killed 10  survived 3",
		"  uncovered 3",
		"tests of 1 test binary, 10 of 13 mutants covered, 3 uncovered",
		"score 76.92%",
		"failed unexpected-survivors: policy.strict is set and 3 mutants survived unexpectedly",
		"  exit 1",
	} {
		if !strings.Contains(out, needle) {
			t.Errorf("stdout does not contain %q:\n%s", needle, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Error("stdout carries escape sequences with NO_COLOR set")
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want nothing: a policy failure is not an error", stderr.String())
	}
}

func TestJSONWritesTheDocumentAloneOnStandardOutput(t *testing.T) {
	t.Parallel()
	goBin := testkit.GoBinary(t)
	repo := testkit.Root(t)
	binary := filepath.Join(t.TempDir(), "go-mutants")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(t.Context(), goBin, "build", "-o", binary, "./cmd/go-mutants")
	build.Dir = repo
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("building cmd/go-mutants: %v\n%s", buildErr, out)
	}

	run := exec.CommandContext(t.Context(), binary, "run", "--json")
	run.Dir = testkit.Copy(t, "killable")
	cache := t.TempDir()
	run.Env = append(childEnv(t.TempDir()),
		"NO_COLOR=1", "LOCALAPPDATA="+cache, "XDG_CACHE_HOME="+cache, "HOME="+cache)

	var stdout, stderr strings.Builder
	run.Stdout = &stdout
	run.Stderr = &stderr
	if err := run.Run(); err != nil {
		t.Fatalf("go-mutants run --json: %v\nstderr:\n%s", err, stderr.String())
	}

	document := []byte(stdout.String())
	var decoded map[string]any
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatalf("standard output is not one JSON document: %v\n%s", err, stdout.String())
	}
	if decoded["document_type"] != report.DocumentType {
		t.Fatalf("document_type = %v, want %q", decoded["document_type"], report.DocumentType)
	}
	if !strings.Contains(stderr.String(), "phase mutate:") {
		t.Errorf("stderr carries no progress:\n%s", stderr.String())
	}
	validateDocument(t, document)

	var parsed report.Report
	if err := json.Unmarshal(document, &parsed); err != nil {
		t.Fatalf("decoding the document into a report: %v", err)
	}
	tally, err := parsed.Tally()
	if err != nil {
		t.Fatalf("recounting the report: %v", err)
	}
	summary := parsed.Summary
	if tally.Total() != summary.Total || tally.Killed != summary.Killed || tally.Survived() != summary.Survived {
		t.Errorf("the recounted tally %+v disagrees with the summary %+v", tally, summary)
	}
	if summary.Total != 13 || summary.Killed != 10 || summary.Survived != 3 {
		t.Errorf("summary = %+v, want 13 mutants, 10 killed, 3 survived", summary)
	}
}

func validateDocument(t *testing.T, document []byte) {
	t.Helper()
	if err := schemas.Validate(schemas.RunReportV1, document); err != nil {
		t.Fatalf("the published report does not satisfy run-report-v1: %v", err)
	}
}

func published(t *testing.T, events []Event) ReportPublished {
	t.Helper()
	for _, e := range events {
		if got, ok := e.(ReportPublished); ok {
			return got
		}
	}
	t.Fatal("the run published no report")
	return ReportPublished{}
}

func validatedOf(t *testing.T, events []Event) Validated {
	t.Helper()
	for _, e := range events {
		if got, ok := e.(Validated); ok {
			return got
		}
	}
	t.Fatal("the run published no Validated event")
	return Validated{}
}

func warningsOf(events []Event) []Warning {
	var out []Warning
	for _, e := range events {
		if got, ok := e.(Warning); ok {
			out = append(out, got)
		}
	}
	return out
}

func warningWith(events []Event, code Code) (Warning, bool) {
	for _, w := range warningsOf(events) {
		if w.Code == string(code) {
			return w, true
		}
	}
	return Warning{}, false
}

func survivorOf(t *testing.T, r *report.Report, rule string) report.Mutant {
	t.Helper()
	var found []report.Mutant
	for _, m := range r.Mutants {
		if m.Outcome == report.OutcomeSurvived && m.Rule == rule {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the report holds %d survived %s mutants, want exactly 1", len(found), rule)
	}
	return found[0]
}

func survivorsOf(t *testing.T, r *report.Report) []report.Mutant {
	t.Helper()
	var found []report.Mutant
	for _, m := range r.Mutants {
		if m.Outcome == report.OutcomeSurvived {
			found = append(found, m)
		}
	}
	if len(found) == 0 {
		t.Fatal("the report holds no survivor")
	}
	return found
}

func killedOf(t *testing.T, r *report.Report) report.Mutant {
	t.Helper()
	for _, m := range r.Mutants {
		if m.Outcome == report.OutcomeKilled {
			return m
		}
	}
	t.Fatal("the report holds no killed mutant")
	return report.Mutant{}
}

func TestCoverageGuidedRunExecutesOnlyWhatTheProfilesReach(t *testing.T) {
	t.Parallel()
	outcome, events, err := collect(t, t.Context(), options(t, "coverage"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	block := outcome.Report.Coverage
	if block.Mode != report.CoverageTest {
		t.Fatalf("coverage mode = %q, want %q", block.Mode, report.CoverageTest)
	}
	if block.Binaries == nil || *block.Binaries != 2 {
		t.Errorf("coverage.binaries = %v, want the fixture's 2", block.Binaries)
	}
	if block.MutantsUncovered == nil || *block.MutantsUncovered != 3 {
		t.Errorf("coverage.mutants_uncovered = %v, want 3", block.MutantsUncovered)
	}

	const (
		corePackage   = "fixture.example/coverage/core"
		callerPackage = "fixture.example/coverage/caller"
	)
	want := map[string]struct {
		covering []string
		outcome  report.Outcome
		killedBy string
	}{
		"return-true core.Differs(a, b)":  {covering: []string{callerPackage}, outcome: report.OutcomeKilled, killedBy: callerPackage},
		"return-false core.Differs(a, b)": {covering: []string{callerPackage}, outcome: report.OutcomeKilled, killedBy: callerPackage},
		"return-true v > 0":               {covering: []string{corePackage}, outcome: report.OutcomeKilled, killedBy: corePackage},
		"return-false v > 0":              {covering: []string{corePackage}, outcome: report.OutcomeKilled, killedBy: corePackage},
		"gt-to-ge >":                      {covering: []string{corePackage}, outcome: report.OutcomeKilled, killedBy: corePackage},
		"return-true a != b":              {covering: []string{callerPackage}, outcome: report.OutcomeKilled, killedBy: callerPackage},
		"return-false a != b":             {covering: []string{callerPackage}, outcome: report.OutcomeKilled, killedBy: callerPackage},
		"neq-to-eq !=":                    {covering: []string{callerPackage}, outcome: report.OutcomeKilled, killedBy: callerPackage},
		"return-true a < b":               {covering: []string{}, outcome: report.OutcomeSurvived},
		"return-false a < b":              {covering: []string{}, outcome: report.OutcomeSurvived},
		"lt-to-le <":                      {covering: []string{}, outcome: report.OutcomeSurvived},
	}
	if len(outcome.Report.Mutants) != len(want) {
		t.Fatalf("the catalogue holds %d mutants, want %d: %+v",
			len(outcome.Report.Mutants), len(want), outcome.Report.Mutants)
	}
	for _, m := range outcome.Report.Mutants {
		name := m.Rule + " " + m.Original
		expected, known := want[name]
		if !known {
			t.Errorf("unexpected mutant %s (%s)", m.DisplayID, name)
			continue
		}
		if !slices.Equal(m.CoveringTestPackages, expected.covering) {
			t.Errorf("%s is covered by %v, want %v", name, m.CoveringTestPackages, expected.covering)
		}
		if m.Outcome != expected.outcome {
			t.Errorf("%s is %s, want %s", name, m.Outcome, expected.outcome)
		}
		if m.Uncovered != (len(expected.covering) == 0) {
			t.Errorf("%s: uncovered = %t with covering %v", name, m.Uncovered, m.CoveringTestPackages)
		}
		killedBy := ""
		if m.KilledBy != nil {
			killedBy = *m.KilledBy
		}
		if killedBy != expected.killedBy {
			t.Errorf("%s was killed by %q, want %q", name, killedBy, expected.killedBy)
		}
	}

	uncovered := ruleOf(t, outcome.Report, "lt-to-le")
	for _, e := range events {
		if started, ok := e.(MutantStarted); ok && started.ID == uncovered.ID {
			t.Errorf("the uncovered mutant %s was started on worker %d", started.DisplayID, started.Worker)
		}
	}
	if uncovered.Attempts != 0 || uncovered.DurationMS != 0 {
		t.Errorf("the uncovered mutant reports %d attempts in %dms, want none of either",
			uncovered.Attempts, uncovered.DurationMS)
	}
	finished := false
	for _, e := range events {
		if done, ok := e.(MutantFinished); ok && done.Result.ID == uncovered.ID {
			finished = true
			if !done.Result.Uncovered || done.Result.Outcome != mutation.OutcomeSurvived {
				t.Errorf("the uncovered mutant was published as %+v", done.Result)
			}
		}
	}
	if !finished {
		t.Error("the uncovered mutant was never published, so a renderer would be a mutant short of the report")
	}

	mapped, found := coverageMappedOf(events)
	if !found {
		t.Fatal("the run published no CoverageMapped event")
	}
	if mapped.Binaries != 2 || mapped.Covered != 8 || mapped.Uncovered != 3 {
		t.Errorf("CoverageMapped = %+v, want 2 binaries, 8 covered, 3 uncovered", mapped)
	}
	if got := kinds(events); slices.Index(got, "engine.CoverageMapped") > slices.Index(got, "engine.MutantFinished") {
		t.Errorf("the coverage summary arrives after the first settled mutant:\n\t%s",
			strings.Join(got, "\n\t"))
	}

	if score := outcome.Report.Summary.ScorePercent; score == nil || *score < 72 || *score > 73 {
		t.Errorf("score = %v, want 8 of 11", score)
	}

	document, err := os.ReadFile(published(t, events).RunPath)
	if err != nil {
		t.Fatalf("reading the filed report: %v", err)
	}
	validateDocument(t, document)
}

func TestCustomTestCommandTurnsCoverageOffAndSaysSo(t *testing.T) {
	t.Parallel()
	opts := options(t, "coverage")
	opts.TestArgv = []string{"go", "test", "-count=1", "./..."}

	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	warning, found := warningWith(events, Code(coverage.CodeCustomTestCommand))
	if !found {
		t.Fatalf("no %s warning; the run gave up coverage in silence. warnings: %+v",
			coverage.CodeCustomTestCommand, warningsOf(events))
	}
	for _, needle := range []string{"go test -count=1 ./...", "go test ./..."} {
		if !strings.Contains(warning.Message, needle) {
			t.Errorf("the warning does not mention %q:\n%s", needle, warning.Message)
		}
	}
	if strings.ContainsAny(warning.Message, "\n\r") {
		t.Errorf("the warning is not one line: %q", warning.Message)
	}
	filed := false
	for _, w := range outcome.Report.Warnings {
		if w.Code == string(coverage.CodeCustomTestCommand) && w.Message == warning.Message {
			filed = true
		}
	}
	if !filed {
		t.Errorf("the warning is not in the filed report: %+v", outcome.Report.Warnings)
	}

	if mode := outcome.Report.Coverage.Mode; mode != report.CoverageOff {
		t.Fatalf("coverage mode = %q, want %q", mode, report.CoverageOff)
	}
	if outcome.Report.Coverage.Binaries != nil || outcome.Report.Coverage.MutantsUncovered != nil {
		t.Errorf("a run that narrowed nothing reports %+v", outcome.Report.Coverage)
	}
	if _, mapped := coverageMappedOf(events); mapped {
		t.Error("a run with coverage off published a CoverageMapped event")
	}

	started := 0
	for _, e := range events {
		if _, ok := e.(MutantStarted); ok {
			started++
		}
	}
	if started != 11 {
		t.Errorf("started %d mutants, want all 11", started)
	}
	for _, m := range outcome.Report.Mutants {
		if m.Uncovered {
			t.Errorf("mutant %s is marked uncovered in a run with coverage off", m.DisplayID)
		}
		if len(m.CoveringTestPackages) != 0 {
			t.Errorf("mutant %s names %v as covering it in a run that never looked", m.DisplayID, m.CoveringTestPackages)
		}
		if m.Attempts == 0 {
			t.Errorf("mutant %s was not executed", m.DisplayID)
		}
	}
	if summary := outcome.Report.Summary; summary.Total != 11 || summary.Killed != 8 || summary.Survived != 3 {
		t.Errorf("summary = %+v, want the same 11/8/3 the coverage-guided run reaches", summary)
	}

	document, err := os.ReadFile(published(t, events).RunPath)
	if err != nil {
		t.Fatalf("reading the filed report: %v", err)
	}
	validateDocument(t, document)
}

func coverageMappedOf(events []Event) (CoverageMapped, bool) {
	for _, e := range events {
		if got, ok := e.(CoverageMapped); ok {
			return got, true
		}
	}
	return CoverageMapped{}, false
}

func ruleOf(t *testing.T, r *report.Report, rule string) report.Mutant {
	t.Helper()
	var found []report.Mutant
	for _, m := range r.Mutants {
		if m.Rule == rule {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the report holds %d %s mutants, want exactly 1", len(found), rule)
	}
	return found[0]
}
