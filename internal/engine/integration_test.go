// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of the engine's tests. It runs a real `go build`,
// a real `go test`, a real instrumentation pass and real mutant processes
// against the fixture corpus, which is the only way to prove the pipeline
// works: everything interesting about it — process supervision, the snapshot,
// the environment the children get, the timeout derived from what was actually
// measured, whether activating a mutant turns a passing suite red — is exactly
// what a mock would have to invent.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/engine/...
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

// testToolVersion is the version string the engine records in a test report.
// The document requires a non-empty one, and a fixed value keeps the golden
// parts of a report independent of what internal/cli currently says.
const testToolVersion = "0.0.0-test"

// fixture returns the absolute path of a corpus module.
func fixture(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "fixtures", name))
	if err != nil {
		t.Fatalf("resolving the fixture path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "go.mod")); err != nil {
		t.Fatalf("fixture %s is not a module: %v", name, err)
	}
	return path
}

// options returns the engine options for one fixture run: one baseline
// observation, one worker, and a history store of the test's own.
//
// Both numbers are about the tests rather than about the engine. One baseline
// run is all a fixture needs to prove its suite is green, and the derived
// timeout is generous whatever it measures; one worker makes the order of the
// result events deterministic, which is what lets a test assert the sequence
// rather than a set.
//
// The history root is the load-bearing one. Every run files a report, and the
// default store is the developer's own operating system cache directory: a test
// suite that wrote there would leave a directory per fixture behind on every
// machine that ever ran it.
func options(t *testing.T, name string) Options {
	t.Helper()
	cfg := config.Defaults()
	cfg.Test.BaselineRuns = 1
	cfg.Execution.Jobs = 1
	return Options{
		Config:        cfg,
		WorkspaceRoot: fixture(t, name),
		ToolVersion:   testToolVersion,
		HistoryRoot:   t.TempDir(),
	}
}

// collect runs the engine with a drained event channel and returns everything
// that was published.
//
// The collector starts before the engine does and is joined after it returns.
// Both halves matter: the engine's sends block, so a consumer that is not
// already running deadlocks the run, and a consumer that is not waited for can
// miss the terminal event.
func collect(t *testing.T, ctx context.Context, opts Options) (RunOutcome, []Event, error) {
	t.Helper()
	return watch(t, ctx, opts, nil)
}

// watch is [collect] with a hook that sees each event as it arrives, so that a
// test can act on the run — cancel it, for instance — at a point the run itself
// defines rather than after a sleep.
//
// The hook runs on the collector's goroutine and the engine's sends block, so a
// hook that cancels has cancelled before the engine publishes anything else.
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

// kinds names each event by its type, so a sequence can be compared as data.
func kinds(events []Event) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, fmt.Sprintf("%T", e))
	}
	return names
}

// results renders every settled mutant as "outcome path:line rule", in the
// order the run reported them.
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

// privateTempDir points this process's temporary directory at a fresh one for
// the length of the test, and returns it.
//
// The engine creates its snapshot and its scratch directory under
// os.TempDir(), so redirecting that is what makes "nothing was left behind" an
// assertion rather than a guess: the shared temporary directory of a machine
// running `go test ./...` has several packages writing into it at once, and a
// leak of one is indistinguishable from a leak of another.
func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// os.TempDir reads TMPDIR on POSIX and TMP then TEMP on Windows, so all
	// three are set rather than guessing which platform is reading.
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	return dir
}

// entries lists the names directly under a directory, sorted.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	found, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, 0, len(found))
	for _, e := range found {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

func TestRunMeasuresTheBaselineAndDerivesTheTimeout(t *testing.T) {
	tempRoot := privateTempDir(t)
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

	// The expectation is derived from what the run itself measured, never from
	// a second measurement: runner.Result.Duration is the outer, supervised
	// time, and anything this test timed independently would be a different
	// number.
	slowest := slices.Max(outcome.BaselineRuns)
	if outcome.SlowestBaseline != slowest {
		t.Errorf("SlowestBaseline = %s, want %s", outcome.SlowestBaseline, slowest)
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

	// The fixture's whole catalogue, as a number, so that the sequence below
	// stays a claim about the pipeline rather than a restatement of whatever
	// the run happened to do.
	const simpleMutants = 13
	if got := len(outcome.Report.Mutants); got != simpleMutants {
		t.Fatalf("the simple fixture produced %d mutants, want %d", got, simpleMutants)
	}

	// The whole sequence, pinned. With one worker the mutants settle in
	// catalogue order, so this is a fact about the pipeline rather than about
	// the machine — and it is the only assertion that would notice a phase
	// quietly dropping out of the run.
	wantKinds := []string{
		"engine.RunPlanned",
		"engine.PhaseChanged", // discover
		"engine.PhaseChanged", // baseline
		"engine.BaselineProgress",
		"engine.BaselineProgress",
		"engine.BaselineCompleted",
		"engine.PhaseChanged", // mutate
		"engine.Discovered",
		"engine.Validated",
		"engine.BaselineProgress", // the instrumented baseline
		"engine.CoverageMapped",   // every mutant in this fixture is covered
	}
	// One started/finished pair per mutant, back to back, because there is one
	// worker: a second worker would interleave them and this would be a claim
	// about scheduling rather than about the phases.
	for range simpleMutants {
		wantKinds = append(wantKinds, "engine.MutantStarted", "engine.MutantFinished")
	}
	wantKinds = append(wantKinds,
		"engine.PhaseChanged", // report
		"engine.ReportPublished",
		"engine.RunCompleted",
	)
	if got := kinds(events); !slices.Equal(got, wantKinds) {
		t.Fatalf("event sequence =\n\t%s\nwant\n\t%s",
			strings.Join(got, "\n\t"), strings.Join(wantKinds, "\n\t"))
	}

	if planned := events[0].(RunPlanned); planned.RunID != outcome.RunID || planned.Workers != 1 {
		t.Errorf("RunPlanned = %+v, want run %s and 1 worker", planned, outcome.RunID)
	}
	for i, phase := range []Phase{PhaseDiscover, PhaseBaseline} {
		if got := events[1+i].(PhaseChanged); got.Phase != phase || got.Detail == "" {
			t.Errorf("phase %d = %+v, want a described %s", i, got, phase)
		}
	}
	for i, index := range []int{3, 4} {
		progress := events[index].(BaselineProgress)
		if progress.Run != i+1 || progress.Of != 2 {
			t.Errorf("progress %d = %+v, want run %d of 2", index, progress, i+1)
		}
		if progress.Duration != outcome.BaselineRuns[i] {
			t.Errorf("progress %d reported %s, outcome recorded %s", index, progress.Duration, outcome.BaselineRuns[i])
		}
	}
	completed := events[5].(BaselineCompleted)
	if completed.Timeout != outcome.Timeout || completed.TimeoutSource != outcome.TimeoutSource {
		t.Errorf("BaselineCompleted = %+v, want the outcome's timeout %s (%s)", completed, outcome.Timeout, outcome.TimeoutSource)
	}
	if !slices.Equal(completed.Runs, outcome.BaselineRuns) {
		t.Errorf("BaselineCompleted.Runs = %v, want %v", completed.Runs, outcome.BaselineRuns)
	}
	// The instrumented baseline is the sole `1 of 1`, and it is what proves the
	// rewrite preserved meaning: the suite passed with every guard in the tree
	// and nothing activated.
	if instrumented := events[9].(BaselineProgress); instrumented.Run != 1 || instrumented.Of != 1 {
		t.Errorf("the instrumented baseline reported %+v, want run 1 of 1", instrumented)
	}
	// Coverage is on by default — the test command is the built-in one — and
	// this fixture's every function is exercised, so nothing is skipped.
	if mapped := events[10].(CoverageMapped); mapped.Binaries != 1 || mapped.Covered != simpleMutants || mapped.Uncovered != 0 {
		t.Errorf("CoverageMapped = %+v, want 1 binary covering all %d mutants", mapped, simpleMutants)
	}
	if mode := outcome.Report.Coverage.Mode; mode != report.CoveragePackage {
		t.Errorf("coverage mode = %q, want %q with the built-in test command", mode, report.CoveragePackage)
	}
	if final := events[len(events)-1].(RunCompleted); final.Status != StatusOK || final.Run == nil {
		t.Errorf("RunCompleted = %+v, want an ok run carrying its summary", final)
	}
	if len(outcome.Warnings) != 0 {
		t.Errorf("a clean run published %v", outcome.Warnings)
	}

	// The snapshot is gone, and so is the scratch directory beside it — the
	// compiled test binaries and the per-worker temporary directories included.
	if filepath.Dir(outcome.SnapshotRoot) != tempRoot {
		t.Errorf("the snapshot was created in %s, want it under the redirected temporary directory %s",
			filepath.Dir(outcome.SnapshotRoot), tempRoot)
	}
	if _, err := os.Stat(outcome.SnapshotRoot); !os.IsNotExist(err) {
		t.Errorf("the snapshot at %s survived the run (stat error %v)", outcome.SnapshotRoot, err)
	}
	if left := entries(t, tempRoot); len(left) != 0 {
		t.Errorf("the run left %v behind in the temporary directory", left)
	}
}

// TestKillableRunReachesTheFixturesPredeterminedFates is the whole pipeline
// judged against a fixture built to have exactly one answer.
//
// The killable corpus is laid out so that a mutant can be named by file, line
// and rule alone, and its documentation states which functions are covered and
// which one is not. This asserts that claim as a tally: every mutant in the two
// functions the tests exercise has to die, and every mutant in the function
// nothing calls has to live. A run where everything died would be a tree that
// stopped compiling, and one where everything lived would be activation that
// never happened.
func TestKillableRunReachesTheFixturesPredeterminedFates(t *testing.T) {
	privateTempDir(t)
	outcome, events, err := collect(t, t.Context(), options(t, "killable"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	// The catalogue in full, one line per mutant. Writing it out rather than
	// counting is what makes the tally a statement about the fixture instead of
	// about which operator families have landed: every mutant in Clamp and
	// IsReady dies, every mutant in Untested lives, and a family that starts or
	// stops firing here has to be looked at rather than absorbed.
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

	// The survivors are the three in untested.go, and with coverage on the run
	// establishes *why* they survived without executing any of them: nothing in
	// the module calls Untested, so no test binary reaches the line. The other
	// ten are covered by the fixture's single test binary.
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

	// Not strict, so a survivor is a finding rather than a failure. That is the
	// default on purpose: go-mutants does not fail a build unless it was asked
	// to.
	if outcome.Verdict.Code != mutation.ExitOK {
		t.Errorf("verdict = %+v, want exit 0 without --strict", outcome.Verdict)
	}

	// The report is on disk, under the history root this test owns, and it is
	// the same document the outcome carries.
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

// TestParallelWorkersReachTheSameTally is the one test that runs the mutants
// concurrently.
//
// Every other test here pins Jobs to 1 so that the event order is a fact about
// the pipeline; this one gives up that order deliberately, because the workers
// are the only place in go-mutants where several goroutines publish on the
// event channel and the only place a result could be written twice. What has to
// survive parallelism is the answer, not the sequence — so the results are
// sorted before they are compared, and the summary is checked against the same
// tally the serial run produces.
//
// Run it under `-race` when a C toolchain is available: the invariants it
// leans on are that the display index is written before Schedule and only read
// after, that each worker writes only its own result slot, and that no hook
// touches the warning list.
func TestParallelWorkersReachTheSameTally(t *testing.T) {
	privateTempDir(t)
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
	// Exactly one settled result per mutant, whatever order they finished in: a
	// mutant reported twice would leave another one silently unaccounted for.
	if len(got) != 13 {
		t.Errorf("the run published %d results for 13 mutants", len(got))
	}
}

// TestStrictFailsOnTheSurvivorItWasNotToldAbout is the same run with the one
// gate this fixture can trip.
func TestStrictFailsOnTheSurvivorItWasNotToldAbout(t *testing.T) {
	privateTempDir(t)
	opts := options(t, "killable")
	opts.Config.Policy.Strict = true

	outcome, _, err := collect(t, t.Context(), opts)
	// A policy failure is not an error. The run did everything right; what it
	// found is what the user asked to be told about, and conflating the two
	// would make "go-mutants could not do its job" and "your tests missed
	// something" the same event.
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

// TestMutantSelectsExactlyOne pins the difference between `run --mutant` and
// `list --mutant`: a run must narrow to one mutant, and everything else is
// still catalogued and reported as not-run.
func TestMutantSelectsExactlyOne(t *testing.T) {
	privateTempDir(t)

	// The id comes from a run rather than from a constant: a mutant's identity
	// is a digest over the fixture's bytes, so a hard-coded one would turn every
	// edit to a comment in the fixture into a failure here.
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
	// The catalogue is still whole, which is what keeps policy.require_mutants
	// honest about the difference between "nothing to find" and "not looked at
	// this time".
	if candidates := outcome.Report.Selection.Candidates; candidates != 13 {
		t.Errorf("candidates = %d, want the whole catalogue of 13", candidates)
	}
}

func TestMutantThatSelectsNothingIsRefused(t *testing.T) {
	privateTempDir(t)
	opts := options(t, "killable")
	// Well formed and not a prefix of any digest of this fixture.
	opts.MutantPrefix = strings.Repeat("0", 32)

	_, _, err := collect(t, t.Context(), opts)
	var selection *SelectionError
	if !errors.As(err, &selection) {
		t.Fatalf("Run = %v, want a SelectionError", err)
	}
	if !errors.Is(err, mutation.ErrMutantNotFound) {
		t.Errorf("error = %v, want the catalogue's own sentinel in the chain", err)
	}
	// No GOM code, deliberately: the mistake is in how the run was invoked, and
	// internal/cli codes it in its own block rather than there being two
	// identifiers for one condition.
	if code := CodeOf(err); code != "" {
		t.Errorf("the selection error carries code %s, want none", code)
	}
}

// TestExpectedSurvivorLeavesAStrictRunGreen is the expectations ledger doing
// the job it exists for: survivors somebody has looked at, explained, and
// signed off stop being a reason to fail.
func TestExpectedSurvivorLeavesAStrictRunGreen(t *testing.T) {
	privateTempDir(t)
	first, _, err := collect(t, t.Context(), options(t, "killable"))
	if err != nil {
		t.Fatalf("the run that sources the id: %v", err)
	}
	// Every one of them, because strict fails on any survivor the ledger does
	// not account for: a ledger covering some of a function's mutants and not
	// the rest is exactly the half-done state this test would otherwise pass
	// over.
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
	// A fulfilled expectation leaves the score alone in both directions: it is
	// neither a detection to be proud of nor a survivor to be nagged about.
	if score := outcome.Report.Summary.ScorePercent; score == nil || *score != 100 {
		t.Errorf("score = %v, want 100: the expected survivor is out of the denominator", score)
	}
}

// TestExpectingAKilledMutantIsAContractFailure is the other half of the ledger.
// A row that says "known survivor" about something the tests now catch is lying
// to whoever reads it, and a stale ledger is worse than none — so it escalates
// past the opt-in gates to exit 2.
func TestExpectingAKilledMutantIsAContractFailure(t *testing.T) {
	privateTempDir(t)
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
	// The gate has to be named somewhere a person can read it. Nothing else in
	// the closing block says why this run exited 2 — the counts and the score
	// are the same as a green run's — and a policy failure is deliberately not
	// printed to standard error.
	final := events[len(events)-1].(RunCompleted)
	if final.Run == nil || final.Run.Failure.Reason != mutation.ReasonExpectationFailure {
		t.Fatalf("the closing summary = %+v, want it to name the expectation failure", final.Run)
	}
	if final.Run.Failure.Detail == "" {
		t.Error("the named gate carries no explanation")
	}
}

// TestCancellationMidRunStillPublishesAPartialReport is the interruption
// contract, taken at the one point where there is something to lose: after the
// catalogue exists and some mutants have been measured.
//
// The cancellation is triggered off the event stream rather than after a sleep.
// The engine's sends block, so cancelling inside the collector means the run is
// already cancelled by the time it publishes anything else — which makes this a
// test of the drain-and-publish path rather than a race with a timer.
func TestCancellationMidRunStillPublishesAPartialReport(t *testing.T) {
	privateTempDir(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	opts := options(t, "killable")
	// On the first *executed* mutant, and deliberately not on the first
	// MutantFinished. Coverage-guided selection settles this fixture's uncovered
	// survivor from the run's own goroutine before the execution phase starts,
	// and cancelling there would cancel before a single mutant process had run —
	// which is a different path, already covered by the test that cancels before
	// discovery. What this one is for is the drain-and-publish path *during*
	// execution.
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
	// Something really was measured before the signal, which is what makes this
	// the interruption-during-execution case rather than one more cancellation
	// before any process started.
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
	// The snapshot is still removed: an interrupted run has to leave the
	// machine as it found it.
	if _, err := os.Stat(outcome.SnapshotRoot); !os.IsNotExist(err) {
		t.Errorf("the snapshot at %s survived an interrupted run (stat error %v)", outcome.SnapshotRoot, err)
	}
}

// TestRejectableRunReportsWhatWillNotCompile is compile validation end to end.
//
// The rejectable fixture holds three candidates whose mutated copy is not a
// program — two constant divisions by zero and a constant that overflows the
// type it is returned as — next to sixteen healthy ones sharing the same files.
// A phase that rejected a file rather than a candidate would take the healthy
// ones with it, which is what the counts here would catch.
//
// Four of the sixteen are the fixture's control, in named.go: a comparison and a
// boolean literal returned as a named boolean type. Those four were the module's
// original traps, refused because Form C's selector is a plain `bool` and
// `type Flag bool` will not take one. They are ordinary mutants now — the
// statement guard carries them, they execute, and the fixture's tests kill them
// — and their presence in the killed set rather than the rejected one is the
// improvement asserted where it can fail.
func TestRejectableRunReportsWhatWillNotCompile(t *testing.T) {
	privateTempDir(t)
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
		// A rejection with no explanation is the silence the whole phase exists
		// to avoid, and the document requires one on every row.
		if strings.TrimSpace(rejection.Diagnostic) == "" {
			t.Errorf("rejected mutant %s carries no diagnostic", rejection.DisplayID)
		}
		if rejection.Line <= 0 || rejection.Path == "" {
			t.Errorf("rejected mutant %s has no coordinates: %+v", rejection.DisplayID, rejection)
		}
	}

	summary := outcome.Report.Summary
	if summary.Total != 16 {
		t.Errorf("summary total = %d, want the 16 candidates that compile", summary.Total)
	}
	// The rejected three are out of the score entirely: a mutant that cannot
	// exist must never sit in a denominator. The sixteen that remain are all
	// killed, which is the fixture's other claim about itself — a healthy
	// mutant nothing killed would sit in the report as a survivor and read, at
	// a glance, like a trap that slipped through.
	if summary.ScorePercent == nil || *summary.ScorePercent != 100 {
		t.Errorf("score = %v, want 100 over the sixteen that compile", summary.ScorePercent)
	}
	if len(outcome.Report.Mutants) != 16 {
		t.Errorf("the report holds %d executed mutants, want 16", len(outcome.Report.Mutants))
	}

	validated := validatedOf(t, events)
	if validated.Accepted != 16 || validated.Rejected != 3 {
		t.Errorf("Validated = %+v, want 16 accepted and 3 rejected", validated)
	}

	// The control, by name. Every candidate in named.go has to be executed and
	// killed, and none of them may appear among the rejections: a run that
	// refused them again would still report three traps and a score of 100 over
	// whatever was left, so the count assertions above would not notice.
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
		// Accepted proves the guard compiled; executed proves the statement form
		// really carried the edit into a running test process.
		if m.Attempts == 0 {
			t.Errorf("the named boolean mutant %s (%s) was never executed", m.DisplayID, m.Rule)
		}
	}
	if named != 4 {
		t.Errorf("the report holds %d mutants in named.go, want the fixture's 4", named)
	}
}

// TestMutantThatWasRejectedSaysSo is the other half of `run --mutant`.
//
// `list` does not validate, so every id it prints can be handed to `run
// --mutant` — including one compile validation will refuse. That selects
// nothing, and every gate that might have noticed is working as designed: the
// catalogue is whole so `require_mutants` is satisfied, the denominator is empty
// so `minimum_score` cannot be missed, and there are no survivors for `strict`.
// The run exits 0 having measured nothing, which is the shape
// policy.require_mutants' own documentation calls the most dangerous kind of
// green.
//
// What stops it from being silent is one warning naming the mutant and quoting
// what the compiler said. The exit code is deliberately still 0 and is asserted
// as such: a rejection is data rather than a failure — the same fixture's
// whole-catalogue run reports three of them and stays green — so the fix is to
// say the thing out loud, not to invent a gate for it.
func TestMutantThatWasRejectedSaysSo(t *testing.T) {
	privateTempDir(t)

	// The id comes from a run for the reason survivorOf gives: a mutant's
	// identity is a digest over the fixture's bytes.
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
	// The mutant by name, where it is, and the compiler's own words. Without the
	// last of these the message says only what the user can already infer from
	// nothing having run.
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
	// One line, because that is what a warning is: the plain renderer writes it
	// after a "warning GOM4043: " prefix, and the report stores it as one string.
	if strings.ContainsAny(warning.Message, "\n\r") {
		t.Errorf("the warning is not one line: %q", warning.Message)
	}

	// A renderer that was not listening must not lose it either.
	filed := false
	for _, w := range outcome.Report.Warnings {
		if w.Code == string(CodeSelectedMutantRejected) && w.Message == warning.Message {
			filed = true
		}
	}
	if !filed {
		t.Errorf("the warning is not in the filed report: %+v", outcome.Report.Warnings)
	}
	// A new code is a new value in a published document, so the document is held
	// against the shipped schema here rather than only where a run has no
	// warnings to carry.
	document, err := os.ReadFile(published(t, events).RunPath)
	if err != nil {
		t.Fatalf("reading the filed report: %v", err)
	}
	validateDocument(t, document)

	// The rest of the run is unchanged, and pinned so that a later reader can
	// tell the deliberate parts from the accident.
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
	// The catalogue is still whole, which is exactly why require_mutants stayed
	// quiet and why the warning had to be the thing that spoke.
	if summary := outcome.Report.Summary; summary.Total != 16 || summary.NotRun != 16 {
		t.Errorf("summary = %+v, want the 16 that compile, all not-run", summary)
	}
	if score := outcome.Report.Summary.ScorePercent; score != nil {
		t.Errorf("score = %v, want none: nothing was measured", *score)
	}
}

func TestRunStopsOnAFailingBaseline(t *testing.T) {
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

	// The failure has to quote the test output, or the user is left with an
	// exit status and nothing to act on.
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

	// The stream still terminates, and the snapshot is still removed: a failed
	// run has to leave the machine as it found it.
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

// TestMutationExcludeChangesNeitherTheSnapshotNorItsDigest pins the boundary
// between selecting what to mutate and copying what to build.
//
// `mutation.exclude` is candidate selection. If it reaches the snapshot walk,
// two things break: the excluded files are not built or tested — so
// `exclude = ["**/*_test.go"]` deletes the suite and the baseline passes having
// run nothing — and the workspace digest moves when a setting that touches no
// byte of source changes, which is what the outcome cache and the shard
// congruence check are built on.
//
// Both halves are asserted, but they are not two independent probes: the digest
// is taken over the manifest, so equal file sets already imply equal digests.
// The digest line is the stronger spelling — it also catches a walk that copied
// the same number of different files — and it is here to name the invariant
// phases 9 and 10 will depend on, where the file count alone would not say why.
func TestMutationExcludeChangesNeitherTheSnapshotNorItsDigest(t *testing.T) {
	privateTempDir(t)
	plain, _, err := collect(t, t.Context(), options(t, "simple"))
	if err != nil {
		t.Fatalf("Run with no excludes: %v", err)
	}

	// Every file in the fixture is named by one of these patterns, so a walk
	// that honoured them would copy nothing but go.mod.
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
	// It does decide the catalogue, which is the job it actually has: with
	// simple.go excluded there is nothing left to mutate.
	if got := len(selective.Report.Mutants); got != 0 {
		t.Errorf("mutation.exclude left %d mutants, want none: the only mutable file was excluded", got)
	}
}

// TestMutationExcludeCannotHideAFailingBaseline is the same contract stated as
// the consequence a user meets: a red suite stays red however the mutation
// candidates are selected.
func TestMutationExcludeCannotHideAFailingBaseline(t *testing.T) {
	opts := options(t, "failing-baseline")
	opts.Config.Mutation.Exclude = []string{"**/*_test.go"}

	outcome, _, err := collect(t, t.Context(), opts)
	if err == nil {
		t.Fatal("Run succeeded against a red suite that mutation.exclude named: the baseline gate ran no tests")
	}
	if got := CodeOf(err); got != CodeBaselineTestFailed {
		t.Fatalf("error code = %s, want %s (error: %v)", got, CodeBaselineTestFailed, err)
	}
	// The code alone could arrive for another reason; the needle proves the
	// deliberately-red test was copied, compiled, and executed.
	if output := OutputOf(err); !strings.Contains(output, "this fixture fails on purpose") {
		t.Errorf("the output tail does not show the excluded test running:\n%s", output)
	}
	if outcome.Status != StatusFailed {
		t.Errorf("status = %s, want %s", outcome.Status, StatusFailed)
	}
}

func TestExplicitTimeoutBelowTheBaselineIsRefused(t *testing.T) {
	opts := options(t, "simple")
	// One nanosecond is below any real measurement, so the rejection cannot
	// depend on how fast this machine is.
	opts.Config.Test.Timeout = time.Nanosecond
	// The `--` passthrough travels here, and this is the run that proves it
	// reaches the child rather than being quietly ignored.
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
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	outcome, events, err := collect(t, ctx, options(t, "simple"))
	if err == nil {
		t.Fatal("Run succeeded with an already cancelled context")
	}
	// A cancellation is reported as one wherever it lands. Depending on how far
	// the run got it carries either this package's interrupt code or the code
	// of whatever was cancelled, and in both cases context.Canceled is in the
	// chain — which is what the command line maps to exit 130.
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

// TestCommandLineEndToEnd compiles cmd/go-mutants and runs it, which is the
// only test that covers the wiring between the command tree, the renderer, and
// the engine as a user meets it.
//
// It runs against the killable fixture with --strict, so it is also the one
// place the exit status a CI job branches on is read off a real process rather
// than off a Verdict: a survivor exists, --strict was asked for, and the answer
// has to be 1.
func TestCommandLineEndToEnd(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go executable on PATH: %v", err)
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "go-mutants")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(t.Context(), goBin, "build", "-o", binary, "./cmd/go-mutants")
	build.Dir = repo
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("building cmd/go-mutants: %v\n%s", buildErr, out)
	}

	// The binary is the authority on its own version, which keeps this test out
	// of internal/cli — an import that would make the dependency run backwards,
	// from the engine to the command line that drives it.
	versionOut, err := exec.CommandContext(t.Context(), binary, "--version").Output()
	if err != nil {
		t.Fatalf("go-mutants --version: %v", err)
	}
	version := strings.TrimSpace(string(versionOut))
	if !strings.HasPrefix(version, "go-mutants ") {
		t.Fatalf("--version printed %q, want a `go-mutants <version>` line", version)
	}

	run := exec.CommandContext(t.Context(), binary, "run", "--strict")
	run.Dir = fixture(t, "killable")
	// Hermetic here means "nothing of go-mutants' own leaks in", not "an empty
	// environment": the child has to find the same `go`, the same module cache,
	// and on Windows the same SystemRoot this process did, or it fails for
	// reasons that have nothing to do with what is being tested. The cache
	// variables are redirected because the run files a report in the operating
	// system's cache directory, and a test must not write to the developer's.
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
		// Fourteen candidates and thirteen mutants: `return true` in ready.go is
		// proposed by two rules with the same edit, and the catalogue keeps one.
		"discovered 14 candidates",
		"validated 13 mutants, 0 rejections",
		"phase report:",
		"report run: ",
		"report latest: ",
		// The survivor, its coordinates, and the diff a reader acts on.
		// Coverage is on by default and this fixture's survivor is uncovered:
		// nothing calls Untested, so the label carries the reason.
		"SURVIVED (uncovered)  ",
		"untested.go:14:11  neq-to-eq  != -> ==",
		"    - !=",
		"    + ==",
		"mutants 13  killed 10  survived 3",
		"  uncovered 3",
		"coverage: 1 test binary, 10 of 13 mutants covered, 3 uncovered",
		"score 76.92%",
		// The gate is named on the console and nowhere else: a policy failure
		// is deliberately not printed to standard error.
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
	// A failed policy gate says everything it has to say in the summary block.
	// Repeating a shortened version of it on standard error would dress a
	// measurement the run made correctly up as something having gone wrong.
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want nothing: a policy failure is not an error", stderr.String())
	}
}

// TestJSONWritesTheDocumentAloneOnStandardOutput is the machine-readable half
// of the same wiring, checked against the shipped schema.
func TestJSONWritesTheDocumentAloneOnStandardOutput(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go executable on PATH: %v", err)
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}
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
	run.Dir = fixture(t, "killable")
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
	// Nothing but the document: a stray progress line would make this fail to
	// decode, which is the whole point of routing the renderer at standard
	// error under --json.
	var decoded map[string]any
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatalf("standard output is not one JSON document: %v\n%s", err, stdout.String())
	}
	if decoded["document_type"] != report.DocumentType {
		t.Fatalf("document_type = %v, want %q", decoded["document_type"], report.DocumentType)
	}
	// The progress the console would have printed went to standard error
	// instead of being dropped, so a user watching a --json run still sees one.
	if !strings.Contains(stderr.String(), "phase mutate:") {
		t.Errorf("stderr carries no progress:\n%s", stderr.String())
	}
	validateDocument(t, document)

	// The summary is the tally, not a second opinion about it: recounting the
	// mutants[] array has to reproduce it.
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

// validateDocument holds the published report against the schema go-mutants
// ships.
//
// The validator is a test-only dependency on purpose: it is what proves the
// document internal/report writes is the document the schema describes, and
// linking a JSON Schema engine into the shipped binary to assert that at run
// time would be paying for the check on every run of every user.
func validateDocument(t *testing.T, document []byte) {
	t.Helper()
	if err := schemas.Validate(schemas.RunReportV1, document); err != nil {
		t.Fatalf("the published report does not satisfy run-report-v1: %v", err)
	}
}

// published returns the one ReportPublished event of a run.
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

// validatedOf returns the one Validated event of a run.
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

// warningsOf returns every warning a run published, in order.
func warningsOf(events []Event) []Warning {
	var out []Warning
	for _, e := range events {
		if got, ok := e.(Warning); ok {
			out = append(out, got)
		}
	}
	return out
}

// warningWith returns the first warning carrying a code, and whether there was
// one.
func warningWith(events []Event, code Code) (Warning, bool) {
	for _, w := range warningsOf(events) {
		if w.Code == string(code) {
			return w, true
		}
	}
	return Warning{}, false
}

// survivorOf returns the report's one survivor of a rule, survivorsOf every
// survivor it holds, and killedOf one of its kills.
//
// They exist because a mutant's identity is a digest over the fixture's bytes:
// a test that needs an id has to read it from a run rather than hard-code one,
// or every edit to a comment in the fixture becomes a failure here.
//
// survivorOf takes a rule name because the killable fixture's uncovered
// function produces several survivors, one per rule that fires on it. A test
// that means "the survivor" has to name which one, or it silently becomes a
// test about whichever mutant the catalogue happens to order first.
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

// survivorsOf returns every survivor, in report order, and fails when there are
// none: a test written about survivors must not pass over a run that produced
// no survivor at all.
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

// TestCoverageGuidedRunExecutesOnlyWhatTheProfilesReach is coverage-guided
// selection end to end, against a fixture built to have three different
// answers.
//
// The corpus module has two test binaries and three functions whose mutants
// have three different fates, and its documentation states which binary reaches
// which: `AboveZero` is reached only by its own package's tests, `Differs` only
// by the caller package's, and `Orphan` by nothing at all. This asserts every
// mutant of all three, which is what makes the run's narrowing a fact about
// coverage rather than a coincidence of the catalogue.
func TestCoverageGuidedRunExecutesOnlyWhatTheProfilesReach(t *testing.T) {
	privateTempDir(t)
	outcome, events, err := collect(t, t.Context(), options(t, "coverage"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	block := outcome.Report.Coverage
	if block.Mode != report.CoveragePackage {
		t.Fatalf("coverage mode = %q, want %q", block.Mode, report.CoveragePackage)
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
	// Each mutant, the binaries the profiles say reach it, and what became of
	// it. The `core.Differs` rows are the ones the whole feature is for: they
	// live in `core` and are reachable only from `caller`.
	//
	// The key is the rule and the bytes it rewrites, because a rule alone no
	// longer names one mutant: the return family fires on every one of these
	// functions, so `return-true` is four different mutants in four different
	// coverage situations.
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
		// The kill has to come from the binary the profile named, which is the
		// observable proof that the narrowing did not merely skip work but
		// skipped the right work: a mutant measured against the wrong binary
		// would have survived.
		killedBy := ""
		if m.KilledBy != nil {
			killedBy = *m.KilledBy
		}
		if killedBy != expected.killedBy {
			t.Errorf("%s was killed by %q, want %q", name, killedBy, expected.killedBy)
		}
	}

	uncovered := ruleOf(t, outcome.Report, "lt-to-le")
	// Never executed, asserted through the hooks rather than inferred from the
	// duration: internal/execute publishes MutantStarted at the beginning of
	// every attempt, so the absence of one is the absence of a process.
	for _, e := range events {
		if started, ok := e.(MutantStarted); ok && started.ID == uncovered.ID {
			t.Errorf("the uncovered mutant %s was started on worker %d", started.DisplayID, started.Worker)
		}
	}
	if uncovered.Attempts != 0 || uncovered.DurationMS != 0 {
		t.Errorf("the uncovered mutant reports %d attempts in %dms, want none of either",
			uncovered.Attempts, uncovered.DurationMS)
	}
	// It is still announced, so a renderer's counts and the report's agree.
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
	// And it comes first. The summary of what is about to be skipped has to
	// arrive before the first thing that was skipped, or a reader watching the
	// run sees it backwards. Nothing else in the sequence pins this: a fixture
	// with no uncovered mutants would pass either way, which is why the
	// assertion lives here and not in the `simple` sequence test.
	if got := kinds(events); slices.Index(got, "engine.CoverageMapped") > slices.Index(got, "engine.MutantFinished") {
		t.Errorf("the coverage summary arrives after the first settled mutant:\n\t%s",
			strings.Join(got, "\n\t"))
	}

	// A run whose only survivors are uncovered ones still scores them against
	// the suite, because they are survivors: no test runs the line, so no test
	// caught the edit.
	if score := outcome.Report.Summary.ScorePercent; score == nil || *score < 72 || *score > 73 {
		t.Errorf("score = %v, want 8 of 11", score)
	}

	document, err := os.ReadFile(published(t, events).RunPath)
	if err != nil {
		t.Fatalf("reading the filed report: %v", err)
	}
	validateDocument(t, document)
}

// TestCustomTestCommandTurnsCoverageOffAndSaysSo is the other rule.
//
// A custom command cannot be attributed to go-mutants' own per-package test
// binaries, so the run gives the optimisation up rather than guessing — and it
// has to say so, because the alternative is a user wondering why their run got
// slower when they changed `test.command`. The same fixture then executes every
// mutant, including the one nothing covers, and reaches the same verdicts.
func TestCustomTestCommandTurnsCoverageOffAndSaysSo(t *testing.T) {
	privateTempDir(t)
	opts := options(t, "coverage")
	// Verbatim `go test ./...` with one flag added, which is exactly the shape
	// of a real project's reason for setting the command at all.
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
	// A renderer that was not listening must not lose it either.
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

	// Every mutant executed, and the verdicts unchanged: giving up the
	// optimisation costs time and nothing else.
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

// coverageMappedOf returns the one CoverageMapped event of a run, and whether
// there was one.
func coverageMappedOf(events []Event) (CoverageMapped, bool) {
	for _, e := range events {
		if got, ok := e.(CoverageMapped); ok {
			return got, true
		}
	}
	return CoverageMapped{}, false
}

// ruleOf returns the report's only mutant produced by a rule, and fails when
// there is not exactly one: a test that means "the uncovered one" must not
// silently start meaning "whichever came first".
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
