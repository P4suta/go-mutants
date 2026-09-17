// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package console

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/engine"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

const (
	verbosePackage      = "github.com/example/clamp"
	verboseOtherPackage = "github.com/example/untested"
	verboseSlowPackage  = "github.com/example/slow"
	verboseTraceDir     = "/w/reports/mutation/trace/20260819T101112Z-a1b2"

	verboseCoverageDetail = "the coverage profile was empty\n" +
		"go: no test binaries were built for ./internal/..."
)

func verboseKilled() engine.MutantResult {
	m := killed
	m.KilledBy = verbosePackage
	m.Attempts = 1
	m.CoveringTestPackages = []string{verbosePackage}
	return m
}

func verboseTimedOut() engine.MutantResult {
	m := killed
	m.ID = strings.Repeat("5e4d3c2b", 8)
	m.DisplayID = "5e4d3c2b1a0918273645"
	m.Path = "slow.go"
	m.Line = 31
	m.Column = 4
	m.Rule = "cond-to-true"
	m.Original = "ok"
	m.Replacement = "true"
	m.Outcome = mutation.OutcomeTimedOut
	m.Duration = 20 * time.Second
	m.KilledBy = verboseSlowPackage
	m.Attempts = 2
	m.CoveringTestPackages = []string{verboseSlowPackage}
	return m
}

func verboseSurvivor() engine.MutantResult {
	m := survivor
	m.Attempts = 1
	m.CoveringTestPackages = []string{verbosePackage, verboseOtherPackage}
	return m
}

func verboseUncovered() engine.MutantResult {
	m := uncoveredSurvivor()
	m.Attempts = 0
	return m
}

func recordedEvents(t *testing.T) []trace.Event {
	t.Helper()

	const tick = 250 * time.Millisecond
	moment := time.Date(2026, 8, 19, 10, 11, 12, 0, time.UTC).Add(-tick)
	clock := func() time.Time {
		moment = moment.Add(tick)
		return moment
	}

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, clock, trace.StartRecord{
		Kind:        trace.StartKindRun,
		RunID:       "20260819T101112Z-a1b2",
		ToolVersion: "0.1.0-dev",
		PID:         31337,
		Root:        "/w",
		Args:        []string{"run", "-vv"},
	})
	if recorder == nil {
		t.Fatal("New returned no recorder for a usable sink")
	}

	endPhase := recorder.PhaseStart(trace.PhaseMutate)
	endStage := recorder.Stage("catalog", "4 candidates")
	endStage(trace.ResultSucceeded)
	recorder.Prepare(trace.PreparePhaseDiscovery, trace.StateFinished, trace.ResultSucceeded, 875*time.Millisecond)
	execSeq := recorder.Exec(trace.ExecRecord{
		Kind:       trace.ExecKindMutantRun,
		Subject:    killed.ID,
		Argv:       []string{"/tmp/go-mutants-tmp-1/clamp.test", "-test.timeout=10s", "-test.run", "Test Clamp"},
		Dir:        "/tmp/go-mutants-snap-1",
		EnvNames:   []string{"GO_MUTANTS_ACTIVE=" + killed.ID, "PATH=/usr/bin"},
		TimeoutMS:  10000,
		ExitCode:   1,
		DurationMS: 181,
		Output:     []byte("--- FAIL: TestClamp (0.00s)\n"),
	})
	recorder.MutantExec(trace.MutantRecord{
		ID:         killed.ID,
		DisplayID:  killed.DisplayID,
		Attempt:    1,
		Worker:     3,
		Package:    verbosePackage,
		Binaries:   []string{verbosePackage, verboseOtherPackage},
		TimeoutMS:  10000,
		Outcome:    trace.OutcomeKilled,
		KilledBy:   verbosePackage,
		DurationMS: 181,
		ExecSeqs:   []int64{execSeq},
	})
	probeSeq := recorder.Exec(trace.ExecRecord{
		Kind:       trace.ExecKindProbeRun,
		Argv:       []string{"/tmp/go-mutants-probe-1/clamp.test"},
		ExitCode:   0,
		DurationMS: 96,
	})
	recorder.ProbeExec(trace.ProbeRecord{
		Package:    verbosePackage,
		Binaries:   []string{verbosePackage},
		Outcome:    trace.ProbeOutcomeMeasured,
		ExitCode:   0,
		DurationMS: 96,
		Infected:   []string{killed.ID},
		ExecSeqs:   []int64{probeSeq},
	})
	recorder.Validate(trace.ValidateRecord{
		Tree:    trace.ValidateTreeMutant,
		Op:      trace.ValidateOpBuild,
		Build:   2,
		Failed:  true,
		Blamed:  []string{"clamp.go"},
		Pending: 4,
	})
	recorder.Coverage(trace.CoverageRecord{
		MutantID:  killed.ID,
		Path:      "clamp.go",
		StartLine: 11,
		EndLine:   14,
		Covering:  []string{verbosePackage},
	})
	recorder.Cache(trace.CacheRecord{
		Op:         trace.CacheOpLookup,
		MutantID:   killed.ID,
		Result:     trace.CacheResultHit,
		Outcome:    trace.OutcomeKilled,
		ContextKey: "0000000000000001",
	})
	recorder.Snapshot(trace.SnapshotRecord{
		Kind:       trace.SnapshotKindWorkspace,
		Source:     "/w",
		Dir:        "/tmp/go-mutants-snap-1",
		Stable:     true,
		Files:      1204,
		DurationMS: 1500,
	})
	recorder.Sweep(trace.SweepRecord{
		Parent:       "/tmp",
		Removed:      []string{"/tmp/go-mutants-snap-0", "/tmp/go-mutants-tmp-0"},
		RemovedBytes: 268435456,
		Live:         1,
		Kept:         2,
	})
	recorder.Artifact(trace.ArtifactReportJSON, "/w/reports/mutation/mutation.json")
	recorder.Note(trace.NoteWarning, "GOM4040", "the snapshot directory could not be removed")
	recorder.Note(trace.NoteCoverageUnavailable, "", verboseCoverageDetail)
	endPhase()
	recorder.RunEnd("ok", 0, nil)
	return sink.Events()
}

func verboseStream(t *testing.T) []engine.Event {
	t.Helper()

	block := summary()
	block.Notable = []engine.MutantResult{verboseTimedOut(), verboseSurvivor(), verboseUncovered()}
	block.Coverage = engine.CoveragePackage
	block.Counts = engine.Counts{Total: 4, Killed: 1, Survived: 2, TimedOut: 1, Uncovered: 1}
	block.Score = mutation.Score{Detected: 2, Denominator: 4}

	events := []engine.Event{
		engine.RunPlanned{RunID: "20260819T101112Z-a1b2", Workers: 8},
		engine.PhaseChanged{Phase: engine.PhaseDiscover, Detail: "locating the Go toolchain and copying the workspace"},
		engine.PhaseCompleted{Phase: engine.PhaseDiscover, Duration: 1250 * time.Millisecond},
		engine.PhaseChanged{Phase: engine.PhaseBaseline, Detail: "building the snapshot, then 3 timed runs of go test ./..."},
		engine.BaselineProgress{Run: 1, Of: 3, Duration: 152 * time.Millisecond},
		engine.BaselineCompleted{
			Runs:          []time.Duration{152 * time.Millisecond},
			Average:       152 * time.Millisecond,
			Slowest:       152 * time.Millisecond,
			Timeout:       10 * time.Second,
			TimeoutSource: engine.TimeoutDerived,
		},
		engine.MemoryDerived{
			Limit:  1 << 30,
			Source: engine.MemorySourceDerived,
			Peak:   200 << 20,
		},
		engine.PhaseCompleted{Phase: engine.PhaseBaseline, Duration: 2340 * time.Millisecond},
		engine.PhaseChanged{Phase: engine.PhaseMutate, Detail: "discovering candidates, validating them, then executing the mutants"},
		engine.Discovered{Candidates: 4, Skips: 12},
		engine.Validated{Accepted: 4, Rejected: 0},
		engine.Warning{
			Code: "GOM4102",
			Message: "coverage-guided selection is off because the coverage profile was empty; " +
				"every mutant will be measured against every test binary, which is slower and never wrong",
			Detail: verboseCoverageDetail,
		},
	}
	for _, recorded := range recordedEvents(t) {
		events = append(events, engine.Traced{Event: recorded})
	}
	return append(events,
		engine.MutantFinished{Result: verboseKilled()},
		engine.MutantFinished{Result: verboseTimedOut()},
		engine.MutantFinished{Result: verboseSurvivor()},
		engine.MutantFinished{Result: verboseUncovered()},
		engine.PhaseCompleted{Phase: engine.PhaseMutate, Duration: 4560 * time.Millisecond},
		engine.PhaseChanged{Phase: engine.PhaseReport, Detail: "writing the run report"},
		engine.DirectoryKept{Kind: engine.KeptSnapshot, Path: "/tmp/go-mutants-snap-1"},
		engine.ReportPublished{
			RunPath:    "/cache/go-mutants/workspaces/1a2b/runs/20260819T101112Z-a1b2.json",
			LatestPath: "/cache/go-mutants/workspaces/1a2b/latest.json",
			TracePath:  verboseTraceDir,
		},
		engine.PhaseCompleted{Phase: engine.PhaseReport, Duration: 90 * time.Millisecond},
		engine.RunCompleted{Status: engine.StatusOK, Run: &block},
	)
}

func renderAt(t *testing.T, verbosity int, events []engine.Event) string {
	t.Helper()
	r := NewPlain(nil, "0.1.0-dev", false, false)
	r.Verbosity = verbosity
	return render(t, r, events)
}

func TestVerbosityZeroIsByteIdenticalToToday(t *testing.T) {
	full := verboseStream(t)
	quiet := slices.DeleteFunc(slices.Clone(full), func(e engine.Event) bool {
		switch e.(type) {
		case engine.PhaseCompleted, engine.Traced:
			return true
		default:
			return false
		}
	})

	got := renderAt(t, 0, full)
	if want := renderAt(t, 0, quiet); got != want {
		t.Errorf("the accounting events changed the default output:\n got: %q\nwant: %q", got, want)
	}

	for _, want := range []string{
		"KILLED     1a2b3c4d  clamp.go:12:9  lt-to-le  < -> <=  (181ms)\n",
		"SURVIVED   9f8e7d6c  untested.go:9:12  neq-to-eq  != -> ==  (176ms)\n    - !=\n    + ==\n",
		"SURVIVED (uncovered)  0c1d2e3f  orphan.go:9:12  neq-to-eq  != -> ==  (0s)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the default rendering lost %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"killed by", "covered by", "no test binary", "attempts", "  exec ", "phase mutate: done"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the default rendering printed %q, which belongs to -v:\n%s", forbidden, got)
		}
	}
}

func TestVerboseOnePrintsPhaseDurationsKilledByAndCoveringPackages(t *testing.T) {
	got := renderAt(t, 1, verboseStream(t))
	testkit.Golden(t, "verbose-one.golden.txt", []byte(got))

	for _, want := range []string{
		"phase discover: done (1.25s)\n",
		"phase mutate: done (4.56s)\n",
		"KILLED     1a2b3c4d  clamp.go:12:9  lt-to-le  < -> <=  (181ms) killed by " + verbosePackage + "\n",
		"TIMEOUT    5e4d3c2b  slow.go:31:4  cond-to-true  ok -> true  (20s) hung in " + verboseSlowPackage + " (2 attempts)\n",
		"    covered by: " + verbosePackage + ", " + verboseOtherPackage + "\n",
		"    no test binary\n",
		"sweep: removed 2 directories (256.0 MiB)\n",
		"kept snapshot: /tmp/go-mutants-snap-1\n",
		"trace: " + verboseTraceDir + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("-v did not print %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "  exec ") || strings.Contains(got, "  attempt ") {
		t.Errorf("-v printed the recording, which belongs to -vv:\n%s", got)
	}
}

func TestVerboseTwoPrintsOneLineForEveryTracedEvent(t *testing.T) {
	events := verboseStream(t)
	got := renderAt(t, 2, events)
	testkit.Golden(t, "verbose-two.golden.txt", []byte(got))

	var recorded int
	for _, e := range events {
		if _, ok := e.(engine.Traced); ok {
			recorded++
		}
	}
	var printed int
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, tracePrefix) && !strings.HasPrefix(line, diffIndent) {
			printed++
		}
	}
	if printed != recorded {
		t.Errorf("-vv printed %d recorded lines for %d recorded events:\n%s", printed, recorded, got)
	}
	if deeper := renderAt(t, 7, events); deeper != got {
		t.Errorf("verbosity 7 rendered differently from -vv:\n%s", deeper)
	}
}

func TestEveryTraceEventTypeHasAVerboseRendering(t *testing.T) {
	recorded := recordedEvents(t)
	for _, eventType := range traceEventTypes(t) {
		index := slices.IndexFunc(recorded, func(e trace.Event) bool { return e.Type == eventType })
		if index < 0 {
			t.Errorf("the fixture records no %s event, so nothing here proves it renders", eventType)
			continue
		}
		line, ok := traceLine(recorded[index])
		switch {
		case !ok:
			t.Errorf("%s has no rendering: -vv would print the bare type", eventType)
		case line == "":
			t.Errorf("%s rendered nothing", eventType)
		case strings.Contains(line, "\n"):
			t.Errorf("%s rendered more than one line: %q", eventType, line)
		}
	}
}

func traceEventTypes(t *testing.T) []string {
	t.Helper()
	var document struct {
		Properties struct {
			Type struct {
				Enum []string `json:"enum"`
			} `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(trace.JSONSchema(), &document); err != nil {
		t.Fatalf("reading the trace schema: %v", err)
	}
	if len(document.Properties.Type.Enum) == 0 {
		t.Fatal("the trace schema enumerates no event types, so this test proves nothing")
	}
	return document.Properties.Type.Enum
}

func TestVerboseLinesCarryNoTimestamps(t *testing.T) {
	got := renderAt(t, 2, verboseStream(t))
	for _, pattern := range []string{
		`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`,
		`\d{2}:\d{2}:\d{2}`,
		`\d\.\d{4,}`,
	} {
		if match := regexp.MustCompile(pattern).FindString(got); match != "" {
			t.Errorf("the verbose output carries %q, matching %s:\n%s", match, pattern, got)
		}
	}
}

func TestVerboseLinesSurviveANewlineInsideARecordedField(t *testing.T) {
	events := []engine.Event{
		engine.Traced{Event: trace.Event{Seq: 1, Type: trace.TypeExec, Exec: &trace.ExecRecord{
			Kind: trace.ExecKindMutantRun,
			Argv: []string{"/tmp/a\nb/clamp.test", "-test.run", "Test\rClamp"},
		}}},
		engine.Traced{Event: trace.Event{Seq: 2, Type: trace.TypeSnapshot, Snapshot: &trace.SnapshotRecord{
			Kind: trace.SnapshotKindWorkspace,
			Dir:  "/tmp/go-mutants-snap-1/a\nb",
		}}},
		engine.Traced{Event: trace.Event{Seq: 3, Type: trace.TypeArtifact, Artifact: &trace.ArtifactRecord{
			Kind: trace.ArtifactReportJSON,
			Path: "/w/re\nports/mutation.json",
		}}},
	}
	got := renderAt(t, 2, events)

	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != len(events) {
		t.Errorf("%d events produced %d lines:\n%q", len(events), len(lines), got)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, tracePrefix) {
			t.Errorf("a recorded line carries no prefix, so it would survive `grep -v '^  '`: %q", line)
		}
	}
	if !strings.Contains(got, "/tmp/a b/clamp.test") {
		t.Errorf("the argument vector was lost rather than flattened:\n%q", got)
	}
	if !strings.Contains(got, "'Test Clamp'") {
		t.Errorf("the quoting of an argument with a space did not survive:\n%q", got)
	}
}

func TestVerboseOnePrintsTheWholeReasonUnderItsWarning(t *testing.T) {
	warning := engine.Warning{
		Code:    "GOM4102",
		Message: "coverage-guided selection is off because the coverage profile was empty; every mutant will be measured",
		Detail:  verboseCoverageDetail,
	}
	got := renderAt(t, 1, []engine.Event{warning})
	want := "warning GOM4102: " + warning.Message + "\n" +
		"    the coverage profile was empty\n" +
		"    go: no test binaries were built for ./internal/...\n"
	if got != want {
		t.Errorf("the detail is not under its warning:\n got: %q\nwant: %q", got, want)
	}

	if zero, want := renderAt(t, 0, []engine.Event{warning}), "warning GOM4102: "+warning.Message+"\n"; zero != want {
		t.Errorf("a detail changed the default rendering:\n got: %q\nwant: %q", zero, want)
	}

	plain := engine.Warning{Code: "GOM4040", Message: "the snapshot directory could not be removed"}
	for _, verbosity := range []int{0, 1, 2} {
		got := renderAt(t, verbosity, []engine.Event{plain})
		if want := "warning GOM4040: " + plain.Message + "\n"; got != want {
			t.Errorf("verbosity %d rendered %q, want %q", verbosity, got, want)
		}
	}
}

func TestAttributionNamesTheSuiteTheOutcomeCameFrom(t *testing.T) {
	base := verboseKilled()
	for _, tc := range []struct {
		name   string
		mutate func(engine.MutantResult) engine.MutantResult
		want   string
		unwant string
	}{
		{
			name:   "a kill names the suite that caught it",
			mutate: func(m engine.MutantResult) engine.MutantResult { return m },
			want:   " killed by " + verbosePackage,
		},
		{
			name: "a timeout names the suite it hung",
			mutate: func(m engine.MutantResult) engine.MutantResult {
				m.Outcome = mutation.OutcomeTimedOut
				return m
			},
			want:   " hung in " + verbosePackage,
			unwant: "killed by",
		},
		{
			name: "a second pass is stated",
			mutate: func(m engine.MutantResult) engine.MutantResult {
				m.Attempts = 2
				return m
			},
			want: " (2 attempts)",
		},
		{
			name: "an outcome nothing was attempted on counts nothing",
			mutate: func(m engine.MutantResult) engine.MutantResult {
				m.Outcome = mutation.OutcomeErrored
				m.Attempts = 2
				m.KilledBy = ""
				return m
			},
			unwant: "attempts",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderAt(t, 1, []engine.Event{engine.MutantFinished{Result: tc.mutate(base)}})
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Errorf("the result line does not carry %q:\n%s", tc.want, got)
			}
			if tc.unwant != "" && strings.Contains(got, tc.unwant) {
				t.Errorf("the result line carries %q, which it cannot honestly say:\n%s", tc.unwant, got)
			}
		})
	}
}

func TestQuietBeatsVerbosityForTheAccountingLines(t *testing.T) {
	r := NewPlain(nil, "0.1.0-dev", false, true)
	r.Verbosity = VerbosityTrace
	got := render(t, r, verboseStream(t))

	for _, forbidden := range []string{"phase discover: done", "  exec ", "sweep: removed"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("--quiet printed %q:\n%s", forbidden, got)
		}
	}
	for _, want := range []string{"warning GOM4102:", "report run: ", "run 20260819T101112Z-a1b2  exit 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("--quiet lost %q:\n%s", want, got)
		}
	}
}

func TestFormatCoarseDuration(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{-time.Second, "0s"},
		{4 * time.Millisecond, "0s"},
		{5 * time.Millisecond, "10ms"},
		{1254 * time.Millisecond, "1.25s"},
		{1255 * time.Millisecond, "1.26s"},
		{90*time.Second + 4*time.Millisecond, "1m30s"},
	} {
		if got := FormatCoarseDuration(tc.in); got != tc.want {
			t.Errorf("FormatCoarseDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if FormatCoarseDuration(152*time.Millisecond) == FormatDuration(152*time.Millisecond) {
		t.Error("152ms rounded to 10ms is 150ms; the two formatters cannot agree on it")
	}
	if got, want := FormatCoarseDuration(10*time.Second), FormatDuration(10*time.Second); got != want {
		t.Errorf("FormatCoarseDuration(10s) = %q, want %q", got, want)
	}
}

func TestFormatBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{268435456, "256.0 MiB"},
		{1 << 30, "1.0 GiB"},
		{1 << 40, "1.0 TiB"},
		{1 << 50, "1024.0 TiB"},
	} {
		if got := FormatBytes(tc.in); got != tc.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTraceLineIsTheSameLineTheConsoleDraws(t *testing.T) {
	event := trace.Event{
		Seq: 7, Type: trace.TypeExec, Timestamp: "2026-08-19T10:11:12Z", ElapsedMS: 1200,
		Exec: &trace.ExecRecord{
			Kind: trace.ExecKindMutantRun, Subject: strings.Repeat("1f", 32),
			Argv:       []string{"/tmp/w 1/clamp.test", "-test.timeout=40s"},
			Dir:        "/tmp/w 1",
			ExitCode:   1,
			DurationMS: 520,
		},
	}
	drawn, ok := traceLine(event)
	if !ok {
		t.Fatal("the renderer does not know an exec event")
	}
	if got := TraceLine(event); got != flattened(drawn) {
		t.Errorf("TraceLine = %q, but `-vv` draws %q", got, flattened(drawn))
	}
}

func TestTraceLineNamesAnEventItCannotRead(t *testing.T) {
	if got := TraceLine(trace.Event{}); got != "event" {
		t.Errorf("TraceLine of an empty event = %q, want %q", got, "event")
	}
	if got := TraceLine(trace.Event{Type: trace.TypeExec}); got != trace.TypeExec {
		t.Errorf("TraceLine of a payloadless exec = %q, want %q", got, trace.TypeExec)
	}
}

func TestQuoteArgvQuotesOnlyWhatAShellWouldBreak(t *testing.T) {
	got := QuoteArgv([]string{"/usr/lib/go/bin/go", "test", "-run", "Test A", "./..."})
	const want = `/usr/lib/go/bin/go test -run 'Test A' ./...`
	if got != want {
		t.Errorf("QuoteArgv = %q, want %q", got, want)
	}
}

func TestQuoteArgvRoundTrips(t *testing.T) {
	for _, argv := range [][]string{
		{"/usr/bin/go", "test", "./..."},
		{"/tmp/with a space/clamp.test", "-test.timeout=40s"},
		{`D:\a\_temp\bin\88723483.test`, "-test.timeout=1m0s"},
		{"it's", "a", "quote"},
		{`say "hi"`, "and $HOME", "and `tick`"},
		{`back\slash`, `trailing\`},
		{"", "after an empty argument"},
		{"two\nlines", "\ttabbed"},
		{"~", "*", "?", "[a-z]", "#comment", "a|b", "a;b", "a&b", "(a)", "{a}", "<a>"},
	} {
		line := QuoteArgv(argv)
		got, err := UnquoteArgv(line)
		if err != nil {
			t.Errorf("UnquoteArgv(QuoteArgv(%q)) = %v; the line was %q", argv, err, line)
			continue
		}
		if !slices.Equal(got, argv) {
			t.Errorf("QuoteArgv(%q) = %q, which decodes to %q", argv, line, got)
		}
	}
}

func TestUnquoteArgvRefusesALineItCannotRead(t *testing.T) {
	for _, line := range []string{`'unterminated`, `trailing\`} {
		if got, err := UnquoteArgv(line); err == nil {
			t.Errorf("UnquoteArgv(%q) = %q, want an error", line, got)
		}
	}
}

func TestAMemoryKillSaysWhatItCostAndWhatItWasAllowed(t *testing.T) {
	base := verboseKilled()
	base.MemoryExceeded = true
	base.PeakMemory = 3435973836
	base.MemoryLimit = 1 << 30

	got := renderAt(t, 1, []engine.Event{engine.MutantFinished{Result: base}})
	for _, want := range []string{" killed by " + verbosePackage, "(memory: 3.2 GiB > 1.0 GiB bound)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the result line does not carry %q:\n%s", want, got)
		}
	}

	plain := renderAt(t, 1, []engine.Event{engine.MutantFinished{Result: verboseKilled()}})
	if strings.Contains(plain, "memory") {
		t.Errorf("an ordinary kill mentions memory:\n%s", plain)
	}
}

func TestTheMemoryBoundIsPrintedOnlyForARunThatAskedForItsOwnAccount(t *testing.T) {
	derived := engine.MemoryDerived{
		Limit:  1 << 30,
		Source: engine.MemorySourceDerived,
		Peak:   200 << 20,
	}

	if got := renderAt(t, 0, []engine.Event{derived}); strings.Contains(got, "memory") {
		t.Errorf("level zero printed the memory bound:\n%s", got)
	}
	got := renderAt(t, 1, []engine.Event{derived})
	for _, want := range []string{"memory:", "baseline peak 200.0 MiB", "bound 1.0 GiB", "(derived)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the memory line does not carry %q:\n%s", want, got)
		}
	}

	unenforced := renderAt(t, 1, []engine.Event{
		engine.MemoryDerived{Source: engine.MemorySourceUnavailable, Peak: 200 << 20},
	})
	for _, want := range []string{"baseline peak 200.0 MiB", "no per-mutant bound", "not enforced on this platform"} {
		if !strings.Contains(unenforced, want) {
			t.Errorf("a run on a platform that enforces nothing does not say %q:\n%s", want, unenforced)
		}
	}

	unmeasured := renderAt(t, 1, []engine.Event{
		engine.MemoryDerived{Source: engine.MemorySourceUnavailable},
	})
	if !strings.Contains(unmeasured, "nothing measured what the baseline runs cost") {
		t.Errorf("a run that measured no peak does not say so:\n%s", unmeasured)
	}
	for _, unwanted := range []string{"0 B", "not enforced on this platform"} {
		if strings.Contains(unmeasured, unwanted) {
			t.Errorf("a run that measured nothing printed %q:\n%s", unwanted, unmeasured)
		}
	}
}
