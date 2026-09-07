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

// The verbose fixture's own constants. They are the recording's, because the
// two goldens below are one run said twice: the same events at two verbosities.
const (
	verbosePackage      = "github.com/example/clamp"
	verboseOtherPackage = "github.com/example/untested"
	verboseSlowPackage  = "github.com/example/slow"
	verboseTraceDir     = "/w/reports/mutation/trace/20260819T101112Z-a1b2"

	// The whole reason coverage was given up, as the recording holds it: more
	// than one line, which is the difference between what `-v` prints and the
	// folded single line the warning carries.
	verboseCoverageDetail = "the coverage profile was empty\n" +
		"go: no test binaries were built for ./internal/..."
)

// verboseKilled, verboseTimedOut, verboseSurvivor and verboseUncovered are the
// package's four mutants with the fields `-v` renders filled in.
//
// They are copies of the fixtures the level-zero tests use rather than edits to
// them: the claim `-v` rests on is that these fields change nothing at level
// zero, and a fixture shared with the tests that pin level zero would make that
// claim untestable.
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

// recordedEvents returns one recorded event of every type in the trace
// contract, plus the `coverage-unavailable` note `-v` expands.
//
// It is recorded through the real recorder rather than assembled by hand, on a
// clock that advances by a fixed step, so that the envelope every line carries
// — the sequence number, the elapsed milliseconds — is the one a run produces
// and the goldens below pin a rendering of real events.
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

// verboseStream is the golden run every test below renders: the level-zero
// fixture, with the two accounting events a verbose run draws — a phase's
// duration and the run's own recording — added to it.
//
// The recording arrives in one block rather than interleaved with the phases it
// describes. What is under test is the rendering of each event, and a fixture
// that also modelled the interleaving would be pinning the engine's ordering in
// the wrong package.
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

// renderAt renders the events at one verbosity, with colour off.
func renderAt(t *testing.T, verbosity int, events []engine.Event) string {
	t.Helper()
	r := NewPlain(nil, "0.1.0-dev", false, false)
	r.Verbosity = verbosity
	return render(t, r, events)
}

// TestVerbosityZeroIsByteIdenticalToToday is the promise the whole feature
// rests on: a run that did not ask for more prints exactly what it printed
// before the flag existed.
//
// It is checked from both ends. The accounting events must contribute nothing —
// so a stream with them renders the same bytes as one without — and the fields
// `-v` adds to a result must not move the result line, which is asserted
// against the literal bytes the level-zero tests in this package pin.
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

	// The result lines are today's bytes, character for character, for mutants
	// carrying every field -v prints.
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

// TestVerboseOnePrintsPhaseDurationsKilledByAndCoveringPackages is what `-v` is
// for: where the time went, what caught each mutant, and which suites ran the
// line a survivor sits on.
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
	// The recording itself stays off a `-v` console: one line per subprocess is
	// what -vv is for, and a run of any size has thousands of them.
	if strings.Contains(got, "  exec ") || strings.Contains(got, "  attempt ") {
		t.Errorf("-v printed the recording, which belongs to -vv:\n%s", got)
	}
}

// TestVerboseTwoPrintsOneLineForEveryTracedEvent is the property that makes two
// runs diffable: the console is the recording, one event to one line.
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
		// Two spaces and then something: a survivor's own continuation lines are
		// indented four, which is what keeps the two blocks apart on the screen
		// as well as here.
		if strings.HasPrefix(line, tracePrefix) && !strings.HasPrefix(line, diffIndent) {
			printed++
		}
	}
	if printed != recorded {
		t.Errorf("-vv printed %d recorded lines for %d recorded events:\n%s", printed, recorded, got)
	}
	// More than two is the same as two, so that `-vvv` is a typo with an
	// obvious meaning rather than a level nobody implemented.
	if deeper := renderAt(t, 7, events); deeper != got {
		t.Errorf("verbosity 7 rendered differently from -vv:\n%s", deeper)
	}
}

// TestEveryTraceEventTypeHasAVerboseRendering is the ledger that keeps `-vv`
// total.
//
// The list comes from the published schema rather than from a slice in this
// file, so a type added to the contract without a rendering fails here instead
// of printing nothing in front of somebody diagnosing a run.
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

// traceEventTypes reads the event types out of the published schema.
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

// TestVerboseLinesCarryNoTimestamps keeps the verbose output diffable.
//
// A recording is stamped with the wall clock, and a console that printed the
// stamps would make every line of two runs differ. What a reader needs from a
// line is how long the thing took, which is what is printed instead.
func TestVerboseLinesCarryNoTimestamps(t *testing.T) {
	got := renderAt(t, 2, verboseStream(t))
	for _, pattern := range []string{
		`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`, // RFC 3339
		`\d{2}:\d{2}:\d{2}`,                   // a bare wall-clock time
		`\d\.\d{4,}`,                          // an unrounded duration
	} {
		if match := regexp.MustCompile(pattern).FindString(got); match != "" {
			t.Errorf("the verbose output carries %q, matching %s:\n%s", match, pattern, got)
		}
	}
}

// TestVerboseLinesSurviveANewlineInsideARecordedField is the one-line contract
// held against the fields that are not prose.
//
// A recorded argument vector, directory or path is whatever the operating
// system allowed, and a newline is legal in all three. Two physical lines out
// of one event would break both halves of what the indentation promises: the
// count of recorded lines would exceed the count of recorded events, and the
// continuation would carry no prefix, so `grep -v '^  '` would leave a fragment
// of the recording in the run's own output.
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
	// The bytes are still there, with the newline spent as a space rather than
	// as a line break.
	if !strings.Contains(got, "/tmp/a b/clamp.test") {
		t.Errorf("the argument vector was lost rather than flattened:\n%q", got)
	}
	// And a space a shell-quoted argument put there on purpose is untouched, so
	// the vector still reads as the command it was.
	if !strings.Contains(got, "'Test Clamp'") {
		t.Errorf("the quoting of an argument with a space did not survive:\n%q", got)
	}
}

// TestVerboseOnePrintsTheWholeReasonUnderItsWarning pins where a warning's
// detail comes from, which is the point of it being on the event.
//
// The one-line message is what a successful run prints at any verbosity; the
// whole reason is what `-v` adds, and it is carried by the warning itself so
// that it lands under the line it explains on every run — including one whose
// recording could not be opened, which publishes no [engine.Traced] at all.
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

	// At the default verbosity the warning is the line it has always been.
	if zero, want := renderAt(t, 0, []engine.Event{warning}), "warning GOM4102: "+warning.Message+"\n"; zero != want {
		t.Errorf("a detail changed the default rendering:\n got: %q\nwant: %q", zero, want)
	}

	// And a warning with nothing more to say prints no dangling continuation.
	plain := engine.Warning{Code: "GOM4040", Message: "the snapshot directory could not be removed"}
	for _, verbosity := range []int{0, 1, 2} {
		got := renderAt(t, verbosity, []engine.Event{plain})
		if want := "warning GOM4040: " + plain.Message + "\n"; got != want {
			t.Errorf("verbosity %d rendered %q, want %q", verbosity, got, want)
		}
	}
}

// TestAttributionNamesTheSuiteTheOutcomeCameFrom keeps the two suffixes on the
// outcomes that can honestly carry them.
//
// A timed-out mutant was not detected by an assertion: the binary named is the
// one it hung, which is a different sentence and a different piece of work. And
// an attempt count is only meaningful for a mutant something was attempted on:
// an uncovered survivor has none, and a run that never reached one has none.
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

// TestQuietBeatsVerbosityForTheAccountingLines keeps this type's contract total.
//
// internal/cli refuses `-v` with `--quiet`, so no run reaches this; a renderer
// whose two fields contradicted each other would still be a renderer that
// printed a phase's duration under a banner --quiet had dropped, and the type
// should not depend on a rule enforced two packages away.
func TestQuietBeatsVerbosityForTheAccountingLines(t *testing.T) {
	r := NewPlain(nil, "0.1.0-dev", false, true)
	r.Verbosity = VerbosityTrace
	got := render(t, r, verboseStream(t))

	for _, forbidden := range []string{"phase discover: done", "  exec ", "sweep: removed"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("--quiet printed %q:\n%s", forbidden, got)
		}
	}
	// What --quiet keeps, it keeps: the warning, the paths, and the block.
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
	// The coarse rounding is coarser than the millisecond one, and both agree
	// on a duration that needs neither.
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

// TestTraceLineIsTheSameLineTheConsoleDraws is the whole of what exporting it
// promises: `go-mutants explain` quotes a recorded command afterwards, and what
// it quotes is what `-vv` printed while the run was happening.
//
// The comparison is against the renderer's own output rather than against a
// literal, because a literal would pass while the two drifted apart — which is
// the one failure exporting the function was meant to make impossible.
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

// TestTraceLineNamesAnEventItCannotRead keeps the exported form total: every
// event costs exactly one line, including one whose envelope was lost, so a
// caller never has to invent a spelling for a line with no type on it.
func TestTraceLineNamesAnEventItCannotRead(t *testing.T) {
	if got := TraceLine(trace.Event{}); got != "event" {
		t.Errorf("TraceLine of an empty event = %q, want %q", got, "event")
	}
	if got := TraceLine(trace.Event{Type: trace.TypeExec}); got != trace.TypeExec {
		t.Errorf("TraceLine of a payloadless exec = %q, want %q", got, trace.TypeExec)
	}
}

// TestQuoteArgvQuotesOnlyWhatAShellWouldBreak is the property a pasted
// reproduction rests on: the line is legible where it can be, and correct
// where it cannot.
func TestQuoteArgvQuotesOnlyWhatAShellWouldBreak(t *testing.T) {
	got := QuoteArgv([]string{"/usr/lib/go/bin/go", "test", "-run", "Test A", "./..."})
	const want = `/usr/lib/go/bin/go test -run 'Test A' ./...`
	if got != want {
		t.Errorf("QuoteArgv = %q, want %q", got, want)
	}
}

// TestQuoteArgvRoundTrips is the property that makes a printed command line
// something a program may read back.
//
// `go-mutants explain` prints an argument vector as one POSIX-quoted line, and
// on a platform whose shell cannot run that line the only way to check the line
// is to decode it. A decoder that disagreed with the quoter would fail exactly
// where the quoting matters — a path with a space in it, a Windows separator —
// so the two are held to each other here rather than to a literal.
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

// TestUnquoteArgvRefusesALineItCannotRead keeps the decoder from inventing an
// argument vector out of a line nothing quoted.
func TestUnquoteArgvRefusesALineItCannotRead(t *testing.T) {
	for _, line := range []string{`'unterminated`, `trailing\`} {
		if got, err := UnquoteArgv(line); err == nil {
			t.Errorf("UnquoteArgv(%q) = %q, want an error", line, got)
		}
	}
}
