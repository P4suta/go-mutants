// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package console

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/trace"
)

var (
	killed = engine.MutantResult{
		ID:          strings.Repeat("1a2b3c4d", 8),
		DisplayID:   "1a2b3c4d5e6f7a8b9c0d",
		Path:        "clamp.go",
		Line:        12,
		Column:      9,
		Rule:        "lt-to-le",
		Original:    "<",
		Replacement: "<=",
		Outcome:     mutation.OutcomeKilled,
		Duration:    181 * time.Millisecond,
	}
	survivor = engine.MutantResult{
		ID:          strings.Repeat("9f8e7d6c", 8),
		DisplayID:   "9f8e7d6c5b4a3928170f",
		Path:        "untested.go",
		Line:        9,
		Column:      12,
		Rule:        "neq-to-eq",
		Original:    "!=",
		Replacement: "==",
		Outcome:     mutation.OutcomeSurvived,
		Duration:    176 * time.Millisecond,
	}
)

func summary() engine.RunSummary {
	return engine.RunSummary{
		RunID:    "20260819T101112Z-a1b2",
		ExitCode: mutation.ExitOK,
		Notable:  []engine.MutantResult{survivor},
		Counts: engine.Counts{
			Total: 4, Killed: 3, Survived: 1,
		},
		Score:    mutation.Score{Detected: 3, Denominator: 4},
		Warnings: 1,
		Skips:    []engine.SkipCount{{Reason: "const-decl", Count: 12}},
	}
}

func stream() []engine.Event {
	block := summary()
	return []engine.Event{
		engine.RunPlanned{RunID: "20260819T101112Z-a1b2", Workers: 8},
		engine.PhaseChanged{Phase: engine.PhaseDiscover, Detail: "locating the Go toolchain and copying the workspace"},
		engine.PhaseChanged{Phase: engine.PhaseBaseline, Detail: "building the snapshot, then 3 timed runs of go test ./..."},
		engine.BaselineProgress{Run: 1, Of: 3, Duration: 152 * time.Millisecond},
		engine.BaselineProgress{Run: 2, Of: 3, Duration: 149 * time.Millisecond},
		engine.BaselineProgress{Run: 3, Of: 3, Duration: 210 * time.Millisecond},
		engine.BaselineCompleted{
			Runs:          []time.Duration{152 * time.Millisecond, 149 * time.Millisecond, 210 * time.Millisecond},
			Average:       170 * time.Millisecond,
			Slowest:       210 * time.Millisecond,
			Timeout:       10 * time.Second,
			TimeoutSource: engine.TimeoutDerived,
		},
		engine.PhaseChanged{Phase: engine.PhaseMutate, Detail: "discovering candidates, validating them, then executing the mutants"},
		engine.Discovered{Candidates: 4, Skips: 12},
		engine.Validated{Accepted: 4, Rejected: 0},
		engine.BaselineProgress{Run: 1, Of: 1, Duration: 168 * time.Millisecond},
		engine.MutantStarted{ID: killed.ID, DisplayID: killed.DisplayID, Path: killed.Path, Line: killed.Line, Rule: killed.Rule},
		engine.MutantFinished{Result: killed},
		engine.MutantFinished{Result: survivor},
		engine.Warning{Code: "GOM4040", Message: "the snapshot directory could not be removed: access denied"},
		engine.PhaseChanged{Phase: engine.PhaseReport, Detail: "writing the run report"},
		engine.ReportPublished{
			RunPath:    "/cache/go-mutants/workspaces/1a2b/runs/20260819T101112Z-a1b2.json",
			LatestPath: "/cache/go-mutants/workspaces/1a2b/latest.json",
		},
		engine.RunCompleted{Status: engine.StatusOK, Run: &block},
	}
}

const closing = "SURVIVED   9f8e7d6c  untested.go:9:12  neq-to-eq  != -> ==  (176ms)\n" +
	"    - !=\n" +
	"    + ==\n" +
	"mutants 4  killed 3  survived 1  timeout 0  inconclusive 0  errored 0  not-run 0  rejected 0\n" +
	"score 75.00%\n" +
	"warnings 1\n" +
	"skip const-decl 12\n" +
	"run 20260819T101112Z-a1b2  exit 0\n"

const published = "report run: /cache/go-mutants/workspaces/1a2b/runs/20260819T101112Z-a1b2.json\n" +
	"report latest: /cache/go-mutants/workspaces/1a2b/latest.json\n"

func render(t *testing.T, r *PlainRenderer, events []engine.Event) string {
	t.Helper()
	var out bytes.Buffer
	r.Out = &out
	ch := make(chan engine.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	if err := r.Run(context.Background(), ch); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out.String()
}

func TestPlainRendererIsByteExact(t *testing.T) {
	const want = "go-mutants 0.1.0-dev (run 20260819T101112Z-a1b2)\n" +
		"phase discover: locating the Go toolchain and copying the workspace\n" +
		"phase baseline: building the snapshot, then 3 timed runs of go test ./...\n" +
		"baseline run 1/3: 152ms\n" +
		"baseline run 2/3: 149ms\n" +
		"baseline run 3/3: 210ms\n" +
		"baseline ok: avg 170ms, slowest 210ms, timeout 10s (derived)\n" +
		"phase mutate: discovering candidates, validating them, then executing the mutants\n" +
		"discovered 4 candidates, 12 skips\n" +
		"validated 4 mutants, 0 rejections\n" +
		"baseline run 1/1: 168ms\n" +
		"KILLED     1a2b3c4d  clamp.go:12:9  lt-to-le  < -> <=  (181ms)\n" +
		"SURVIVED   9f8e7d6c  untested.go:9:12  neq-to-eq  != -> ==  (176ms)\n" +
		"    - !=\n" +
		"    + ==\n" +
		"warning GOM4040: the snapshot directory could not be removed: access denied\n" +
		"phase report: writing the run report\n" +
		published +
		closing

	got := render(t, NewPlain(nil, "0.1.0-dev", false, false), stream())
	if got != want {
		t.Errorf("plain output mismatch:\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "\x1b") {
		t.Error("the renderer emitted an escape sequence with colour off")
	}
}

func TestQuietKeepsWhatMatters(t *testing.T) {
	const want = "baseline ok: avg 170ms, slowest 210ms, timeout 10s (derived)\n" +
		"warning GOM4040: the snapshot directory could not be removed: access denied\n" +
		published +
		closing

	got := render(t, NewPlain(nil, "0.1.0-dev", false, true), stream())
	if got != want {
		t.Errorf("quiet output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestFailedRunRendersItsStatus(t *testing.T) {
	events := []engine.Event{
		engine.RunCompleted{Status: engine.StatusFailed, Summary: "GOM4011: baseline run 1 of 3 failed: exited with status 1"},
	}
	const want = "run failed: GOM4011: baseline run 1 of 3 failed: exited with status 1\n"
	if got := render(t, NewPlain(nil, "0.1.0-dev", false, false), events); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestInterruptedRunWithoutAReportRendersItsStatus(t *testing.T) {
	events := []engine.Event{engine.RunCompleted{Status: engine.StatusInterrupted}}
	if got := render(t, NewPlain(nil, "0.1.0-dev", false, false), events); got != "run interrupted:\n" {
		t.Errorf("got %q, want %q", got, "run interrupted:\n")
	}
}

func TestInterruptedRunSaysSoRatherThanGuessingAnExitCode(t *testing.T) {
	block := summary()
	block.Notable = nil
	block.Counts = engine.Counts{Total: 4, Killed: 1, NotRun: 3}
	block.Score = mutation.Score{Detected: 1, Denominator: 1}
	block.Warnings = 0
	block.Skips = nil

	got := render(t, NewPlain(nil, "0.1.0-dev", false, false),
		[]engine.Event{engine.RunCompleted{Status: engine.StatusInterrupted, Run: &block}})
	const want = "mutants 4  killed 1  survived 0  timeout 0  inconclusive 0  errored 0  not-run 3  rejected 0\n" +
		"score 100.00%\n" +
		"run 20260819T101112Z-a1b2  interrupted\n"
	if got != want {
		t.Errorf("interrupted summary mismatch:\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "exit") {
		t.Error("an interrupted run printed an exit code the engine could not have known")
	}
}

func TestUndefinedScoreSaysSoRatherThanPrintingZero(t *testing.T) {
	block := summary()
	block.Notable = nil
	block.Counts = engine.Counts{Total: 2, NotRun: 2}
	block.Score = mutation.NoScore
	block.Warnings = 0
	block.Skips = nil
	block.ExitCode = mutation.ExitPolicyFailure

	got := render(t, NewPlain(nil, "0.1.0-dev", false, false),
		[]engine.Event{engine.RunCompleted{Status: engine.StatusOK, Run: &block}})
	if !strings.Contains(got, "score N/A (0 valid mutants)\n") {
		t.Errorf("an undefined score rendered as %q, want the explicit N/A", got)
	}
	if !strings.HasSuffix(got, "run 20260819T101112Z-a1b2  exit 1\n") {
		t.Errorf("output = %q, want it to end with the run's identity and exit status", got)
	}
}

func TestFailedGateIsNamedInTheBlock(t *testing.T) {
	block := summary()
	if got := renderBlock(t, block); strings.Contains(got, "failed ") {
		t.Errorf("a passing run named a failed gate:\n%s", got)
	}

	block.Counts = engine.Counts{}
	block.Notable = nil
	block.Score = mutation.NoScore
	block.ExitCode = mutation.ExitPolicyFailure
	block.Failure = mutation.Failure{
		Reason: mutation.ReasonNoMutants,
		Detail: "policy.require_mutants is set and the run produced no mutants",
	}

	got := renderBlock(t, block)
	const want = "failed no-mutants: policy.require_mutants is set and the run produced no mutants\n"
	if !strings.Contains(got, want) {
		t.Errorf("the failed gate rendered as\n%s\nwant a line %q", got, want)
	}
	if !strings.HasSuffix(got, "run 20260819T101112Z-a1b2  exit 1\n") {
		t.Errorf("output = %q, want the gate named directly above the exit status", got)
	}
	interrupted := render(t, NewPlain(nil, "0.1.0-dev", false, false),
		[]engine.Event{engine.RunCompleted{Status: engine.StatusInterrupted, Run: &block}})
	if strings.Contains(interrupted, "failed no-mutants") {
		t.Errorf("an interrupted run named a gate it never finished judging:\n%s", interrupted)
	}
}

func TestExpectationsLineAppearsOnlyWithALedger(t *testing.T) {
	block := summary()
	if got := renderBlock(t, block); strings.Contains(got, "expectations") {
		t.Errorf("an empty ledger produced an expectations line:\n%s", got)
	}
	block.Expectations = engine.ExpectationCounts{Fulfilled: 1, Unfulfilled: 2, Stale: 3}
	if got := renderBlock(t, block); !strings.Contains(got, "expectations 1 fulfilled  2 unfulfilled  3 stale\n") {
		t.Errorf("the ledger did not render:\n%s", got)
	}
}

func renderBlock(t *testing.T, block engine.RunSummary) string {
	t.Helper()
	return render(t, NewPlain(nil, "0.1.0-dev", false, false),
		[]engine.Event{engine.RunCompleted{Status: engine.StatusOK, Run: &block}})
}

func TestNotRunMutantsGetNoResultLine(t *testing.T) {
	abandoned := survivor
	abandoned.Outcome = mutation.OutcomeNotRun

	got := render(t, NewPlain(nil, "0.1.0-dev", false, false),
		[]engine.Event{engine.MutantFinished{Result: abandoned}})
	if got != "" {
		t.Errorf("a not-run mutant rendered %q, want nothing", got)
	}
	if OutcomeLabel(mutation.OutcomeNotRun) != "" {
		t.Error("OutcomeLabel gave not-run a label, which would make a sixth outcome column")
	}
}

func TestOutcomeLabelsFitTheColumn(t *testing.T) {
	want := map[mutation.Outcome]string{
		mutation.OutcomeKilled:       "KILLED",
		mutation.OutcomeSurvived:     "SURVIVED",
		mutation.OutcomeTimedOut:     "TIMEOUT",
		mutation.OutcomeInconclusive: "INCONCL",
		mutation.OutcomeErrored:      "ERROR",
		mutation.OutcomeNotRun:       "",
	}
	for outcome, label := range want {
		got := OutcomeLabel(outcome)
		if got != label {
			t.Errorf("OutcomeLabel(%s) = %q, want %q", outcome, got, label)
		}
		if len(got) > OutcomeWidth {
			t.Errorf("OutcomeLabel(%s) = %q, which is wider than the %d column", outcome, got, OutcomeWidth)
		}
	}
}

func TestResultLineQuotesWhatWouldBreakTheLine(t *testing.T) {
	deletion := engine.MutantResult{
		DisplayID:   "abcdef0123456789abcd",
		Path:        "run.go",
		Line:        4,
		Column:      2,
		Rule:        "delete-call-statement",
		Original:    "log.Print(\"x\")",
		Replacement: "",
		Outcome:     mutation.OutcomeKilled,
		Duration:    time.Second,
	}
	got := render(t, NewPlain(nil, "0.1.0-dev", false, false),
		[]engine.Event{engine.MutantFinished{Result: deletion}})
	const want = "KILLED     abcdef01  run.go:4:2  delete-call-statement  log.Print(\"x\") -> \"\"  (1s)\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if strings.Count(got, "\n") != 1 {
		t.Errorf("a killed mutant produced more than one line: %q", got)
	}
}

func TestColorOnlyChangesBytesWhenEnabled(t *testing.T) {
	plain := render(t, NewPlain(nil, "0.1.0-dev", false, false), stream())
	colored := render(t, NewPlain(nil, "0.1.0-dev", true, false), stream())
	if plain == colored {
		t.Log("colour produced identical bytes; lipgloss found no colour profile in this environment")
	}
	for _, needle := range []string{"baseline ok:", "warning GOM4040:", "untested.go:9:12", "run 20260819T101112Z-a1b2"} {
		if !strings.Contains(colored, needle) {
			t.Errorf("coloured output lost %q", needle)
		}
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRunReportsAWriteFailureWithoutDeadlocking(t *testing.T) {
	boom := errors.New("no room on device")
	r := NewPlain(failingWriter{err: boom}, "0.1.0-dev", false, false)

	events := stream()
	ch := make(chan engine.Event)
	go func() {
		for _, e := range events {
			ch <- e
		}
		close(ch)
	}()

	err := r.Run(context.Background(), ch)
	if !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want the write error", err)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLinesAppearBeforeTheStreamCloses(t *testing.T) {
	out := &syncBuffer{}
	r := NewPlain(out, "0.1.0-dev", false, false)

	events := make(chan engine.Event)
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), events) }()

	events <- engine.RunPlanned{RunID: "20260819T101112Z-a1b2", Workers: 8}
	events <- engine.PhaseChanged{Phase: engine.PhaseDiscover, Detail: "copying the workspace"}

	if got := out.String(); !strings.Contains(got, "go-mutants 0.1.0-dev (run 20260819T101112Z-a1b2)") {
		t.Errorf("the header had not reached the writer while the stream was still open; buffer held %q", got)
	}

	close(events)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunSurvivesACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := renderWithContext(t, ctx, NewPlain(nil, "0.1.0-dev", false, false), stream())
	if !strings.Contains(got, "run 20260819T101112Z-a1b2  exit 0") {
		t.Error("a cancelled context stopped the renderer before the terminal event")
	}
}

func renderWithContext(t *testing.T, ctx context.Context, r *PlainRenderer, events []engine.Event) string {
	t.Helper()
	var out bytes.Buffer
	r.Out = &out
	ch := make(chan engine.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	if err := r.Run(ctx, ch); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out.String()
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{-time.Second, "0s"},
		{151987300 * time.Nanosecond, "152ms"},
		{10 * time.Second, "10s"},
		{90 * time.Second, "1m30s"},
		{1500 * time.Millisecond, "1.5s"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.in); got != c.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestColorEnabledRefusesNonFiles(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CI", "")
	if ColorEnabled(&bytes.Buffer{}, false) {
		t.Error("colour was enabled for a writer that is not a terminal")
	}
	t.Setenv("NO_COLOR", "1")
	if ColorEnabled(&bytes.Buffer{}, false) {
		t.Error("NO_COLOR did not turn colour off")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("CI", "true")
	if ColorEnabled(&bytes.Buffer{}, false) {
		t.Error("CI did not turn colour off")
	}
	if ColorEnabled(&bytes.Buffer{}, true) {
		t.Error("--no-color did not turn colour off")
	}
}

func TestEveryEventIsAccountedFor(t *testing.T) {
	block := summary()
	rows := []struct {
		event engine.Event
		lines bool
	}{
		{engine.RunPlanned{}, true},
		{engine.PhaseChanged{}, true},
		{engine.BaselineProgress{}, true},
		{engine.BaselineCompleted{}, true},
		{engine.Discovered{}, true},
		{engine.Validated{}, true},
		{engine.CoverageMapped{}, true},
		{engine.MutantStarted{}, false},
		{engine.MutantFinished{Result: killed}, true},
		{engine.MutantFinished{}, false},
		{engine.CacheHit{}, false},
		{engine.PhaseCompleted{}, false},
		{engine.Traced{}, false},
		{engine.DirectoryKept{Kind: engine.KeptSnapshot, Path: "/tmp/go-mutants-snap-0000"}, true},
		{engine.Warning{}, true},
		{engine.ReportPublished{}, true},
		{engine.RunCompleted{}, true},
		{engine.RunCompleted{Run: &block}, true},
	}
	r := NewPlain(nil, "0.1.0-dev", false, false)
	r.Out = &bytes.Buffer{}
	for _, row := range rows {
		if _, ok := r.line(row.event); ok != row.lines {
			t.Errorf("%T rendered = %t, want %t", row.event, ok, row.lines)
		}
	}
}

func TestDirectoryKeptRendersAsKeptKindPath(t *testing.T) {
	const snapshotDir = "/tmp/go-mutants-snap-1a2b/tree"
	const scratchDir = "/tmp/go-mutants-tmp-3c4d"

	rows := []struct {
		event engine.Event
		want  string
	}{
		{engine.DirectoryKept{Kind: engine.KeptSnapshot, Path: snapshotDir}, "kept snapshot: " + snapshotDir},
		{engine.DirectoryKept{Kind: engine.KeptScratch, Path: scratchDir}, "kept scratch: " + scratchDir},
	}
	for _, quiet := range []bool{false, true} {
		r := NewPlain(nil, "0.1.0-dev", false, quiet)
		r.Out = &bytes.Buffer{}
		for _, row := range rows {
			line, ok := r.line(row.event)
			if !ok {
				t.Fatalf("quiet=%t: %+v printed nothing", quiet, row.event)
			}
			if line != row.want {
				t.Errorf("quiet=%t: %+v rendered %q, want %q", quiet, row.event, line, row.want)
			}
		}
	}
}

func TestPlainRendererPrintsNothingForTracedEventsByDefault(t *testing.T) {
	r := NewPlain(nil, "0.1.0-dev", false, false)
	var out bytes.Buffer
	r.Out = &out

	noisy := []engine.Event{
		engine.PhaseCompleted{Phase: engine.PhaseDiscover, Duration: 250 * time.Millisecond},
		engine.Traced{Event: trace.Event{Seq: 7, Type: trace.TypeExec, Exec: &trace.ExecRecord{
			Kind: trace.ExecKindBaselineTest,
			Argv: []string{"/usr/bin/go", "test", "./..."},
		}}},
		engine.Traced{},
	}
	for _, event := range noisy {
		if line, ok := r.line(event); ok || line != "" {
			t.Errorf("%T rendered %q, want nothing at the default verbosity", event, line)
		}
	}
	if out.Len() != 0 {
		t.Errorf("the renderer wrote %q for events it says it prints nothing for", out.String())
	}
}

func uncoveredSurvivor() engine.MutantResult {
	m := survivor
	m.ID = strings.Repeat("0c1d2e3f", 8)
	m.DisplayID = "0c1d2e3f4a5b6c7d8e9f"
	m.Path = "orphan.go"
	m.Uncovered = true
	m.Duration = 0
	return m
}

func TestACachedResultSaysSoBesideItsDuration(t *testing.T) {
	reused := killed
	reused.Cached = true
	got := render(t, NewPlain(nil, "0.1.0-dev", false, false),
		[]engine.Event{engine.MutantFinished{Result: reused}})

	const want = "KILLED     1a2b3c4d  clamp.go:12:9  lt-to-le  < -> <=  (181ms cached)\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	plain := render(t, NewPlain(nil, "0.1.0-dev", false, false),
		[]engine.Event{engine.MutantFinished{Result: killed}})
	if strings.Contains(plain, "cached") {
		t.Errorf("a measured result claims to be cached: %q", plain)
	}
}

func TestTheClosingBlockCountsReusedOutcomesOnlyWhenTheCacheWasOn(t *testing.T) {
	block := summary()
	block.Cache = engine.CacheOn
	block.Counts.Cached = 2
	got := render(t, NewPlain(nil, "0.1.0-dev", false, false),
		[]engine.Event{engine.RunCompleted{Status: engine.StatusOK, Run: &block}})
	if !strings.Contains(got, "cached 2") {
		t.Errorf("the closing block does not count the reused outcomes:\n%s", got)
	}

	off := summary()
	got = render(t, NewPlain(nil, "0.1.0-dev", false, false),
		[]engine.Event{engine.RunCompleted{Status: engine.StatusOK, Run: &off}})
	if strings.Contains(got, "cached") {
		t.Errorf("a run with the cache off reported a cache number:\n%s", got)
	}
}

func TestUncoveredSurvivorSaysWhyItSurvived(t *testing.T) {
	got := render(t, NewPlain(nil, "0.1.0-dev", false, false),
		[]engine.Event{engine.MutantFinished{Result: uncoveredSurvivor()}})

	const want = "SURVIVED (uncovered)  0c1d2e3f  orphan.go:9:12  neq-to-eq  != -> ==  (0s)\n" +
		"    - !=\n" +
		"    + ==\n"
	if got != want {
		t.Errorf("rendered\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(got, "    - !=") {
		t.Error("the uncovered survivor lost its diff")
	}
}

func TestResultLabelOnlyQualifiesASurvivor(t *testing.T) {
	if got, want := ResultLabel(mutation.OutcomeSurvived, true), "SURVIVED (uncovered)"; got != want {
		t.Errorf("ResultLabel(survived, true) = %q, want %q", got, want)
	}
	if got, want := ResultLabel(mutation.OutcomeSurvived, false), "SURVIVED"; got != want {
		t.Errorf("ResultLabel(survived, false) = %q, want %q", got, want)
	}
	for _, outcome := range []mutation.Outcome{
		mutation.OutcomeKilled,
		mutation.OutcomeTimedOut,
		mutation.OutcomeInconclusive,
		mutation.OutcomeErrored,
		mutation.OutcomeNotRun,
	} {
		if got, want := ResultLabel(outcome, true), OutcomeLabel(outcome); got != want {
			t.Errorf("ResultLabel(%s, true) = %q, want the plain %q", outcome, got, want)
		}
	}
}

func TestCountsLineStatesUncoveredOnlyWhenItWasMeasured(t *testing.T) {
	tests := []struct {
		name      string
		coverage  engine.CoverageMode
		uncovered int
		want      string
	}{
		{
			name:     "coverage off says nothing",
			coverage: engine.CoverageOff,
			want:     "mutants 4  killed 3  survived 1  timeout 0  inconclusive 0  errored 0  not-run 0  rejected 0\n",
		},
		{
			name:      "a coverage-guided run states the count",
			coverage:  engine.CoveragePackage,
			uncovered: 1,
			want:      "mutants 4  killed 3  survived 1  timeout 0  inconclusive 0  errored 0  not-run 0  rejected 0  uncovered 1\n",
		},
		{
			name:      "a coverage-guided run with nothing to skip still states it",
			coverage:  engine.CoveragePackage,
			uncovered: 0,
			want:      "mutants 4  killed 3  survived 1  timeout 0  inconclusive 0  errored 0  not-run 0  rejected 0  uncovered 0\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			block := summary()
			block.Notable = nil
			block.Coverage = test.coverage
			block.Counts.Uncovered = test.uncovered

			got := renderBlock(t, block)
			if !strings.Contains(got, test.want) {
				t.Errorf("the block does not carry\n%q\ngot\n%s", test.want, got)
			}
		})
	}
}

func TestCoverageMappedLineNamesWhatWillBeSkipped(t *testing.T) {
	tests := []struct {
		event engine.CoverageMapped
		want  string
	}{
		{
			event: engine.CoverageMapped{Binaries: 2, Covered: 2, Uncovered: 1},
			want:  "coverage: 2 test binaries, 2 of 3 mutants covered, 1 uncovered\n",
		},
		{
			event: engine.CoverageMapped{Binaries: 1, Covered: 4, Uncovered: 0},
			want:  "coverage: 1 test binary, 4 of 4 mutants covered, 0 uncovered\n",
		},
	}
	for _, test := range tests {
		got := render(t, NewPlain(nil, "0.1.0-dev", false, false), []engine.Event{test.event})
		if got != test.want {
			t.Errorf("rendered %q, want %q", got, test.want)
		}
	}
	quiet := render(t, NewPlain(nil, "0.1.0-dev", false, true),
		[]engine.Event{engine.CoverageMapped{Binaries: 2, Covered: 2, Uncovered: 1}})
	if quiet != "" {
		t.Errorf("--quiet rendered %q for a progress line", quiet)
	}
}

func TestUncoveredSurvivorsSortAfterCoveredOnesInTheBlock(t *testing.T) {
	block := summary()
	block.Coverage = engine.CoveragePackage
	block.Counts.Uncovered = 1
	block.Notable = []engine.MutantResult{survivor, uncoveredSurvivor()}

	got := renderBlock(t, block)
	covered := strings.Index(got, "SURVIVED   9f8e7d6c")
	uncovered := strings.Index(got, "SURVIVED (uncovered)  0c1d2e3f")
	switch {
	case covered < 0 || uncovered < 0:
		t.Fatalf("the block is missing one of the survivors:\n%s", got)
	case uncovered < covered:
		t.Errorf("the uncovered survivor comes first:\n%s", got)
	}
}

func TestReportPublishedNamesEveryArtefactThatExists(t *testing.T) {
	for name, tc := range map[string]struct {
		event engine.ReportPublished
		want  string
	}{
		"history only": {
			event: engine.ReportPublished{RunPath: "/c/runs/a.json", LatestPath: "/c/latest.json"},
			want:  "report run: /c/runs/a.json\nreport latest: /c/latest.json\n",
		},
		"both artefacts": {
			event: engine.ReportPublished{
				RunPath: "/c/runs/a.json", LatestPath: "/c/latest.json",
				ProjectionPath: "/w/reports/mutation/mutation.json",
				HTMLPath:       "/w/reports/mutation/mutation.html",
			},
			want: "report run: /c/runs/a.json\nreport latest: /c/latest.json\n" +
				"report json: /w/reports/mutation/mutation.json\n" +
				"report html: /w/reports/mutation/mutation.html\n",
		},
		"json only": {
			event: engine.ReportPublished{
				RunPath: "/c/runs/a.json", LatestPath: "/c/latest.json",
				ProjectionPath: "/w/reports/mutation/mutation.json",
			},
			want: "report run: /c/runs/a.json\nreport latest: /c/latest.json\n" +
				"report json: /w/reports/mutation/mutation.json\n",
		},
		"html only": {
			event: engine.ReportPublished{
				RunPath: "/c/runs/a.json", LatestPath: "/c/latest.json",
				HTMLPath: "/w/reports/mutation/mutation.html",
			},
			want: "report run: /c/runs/a.json\nreport latest: /c/latest.json\n" +
				"report html: /w/reports/mutation/mutation.html\n",
		},
		"with a recording": {
			event: engine.ReportPublished{
				RunPath: "/c/runs/a.json", LatestPath: "/c/latest.json",
				ProjectionPath: "/w/reports/mutation/mutation.json",
				TracePath:      "/w/reports/mutation/trace/20260819T101112Z-a1b2",
			},
			want: "report run: /c/runs/a.json\nreport latest: /c/latest.json\n" +
				"report json: /w/reports/mutation/mutation.json\n" +
				"trace: /w/reports/mutation/trace/20260819T101112Z-a1b2\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := render(t, NewPlain(nil, "0.1.0-dev", false, false), []engine.Event{tc.event})
			if got != tc.want {
				t.Errorf("the report block =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestReportPublishedSurvivesQuiet(t *testing.T) {
	got := render(t, NewPlain(nil, "0.1.0-dev", false, true), []engine.Event{
		engine.ReportPublished{
			RunPath: "/c/runs/a.json", LatestPath: "/c/latest.json",
			ProjectionPath: "/w/reports/mutation/mutation.json",
			TracePath:      "/w/reports/mutation/trace/20260819T101112Z-a1b2",
		},
	})
	if !strings.Contains(got, "report json: /w/reports/mutation/mutation.json") {
		t.Errorf("--quiet dropped the artefact path:\n%q", got)
	}
	if !strings.Contains(got, "trace: /w/reports/mutation/trace/20260819T101112Z-a1b2") {
		t.Errorf("--quiet dropped where the run recorded:\n%q", got)
	}
}
