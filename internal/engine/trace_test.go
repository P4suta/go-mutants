// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

const traceTick = 5 * time.Millisecond

func tickingClock() func() time.Time {
	clock := testkit.NewClock(time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC))
	clock.Tick(traceTick)
	return clock.Now
}

func recording(t *testing.T, opts Options) (RunOutcome, []Event, error) {
	t.Helper()
	events := make(chan Event, 64)
	done := make(chan []Event, 1)
	go func() {
		var seen []Event
		for e := range events {
			seen = append(seen, e)
		}
		done <- seen
	}()
	opts.Events = events
	outcome, err := Run(t.Context(), opts)
	return outcome, <-done, err
}

func eventNames(events []Event) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, strings.TrimPrefix(fmt.Sprintf("%T", e), "engine."))
	}
	return names
}

func typesOf(events []trace.Event) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, e.Type)
	}
	return names
}

func TestRunWithoutATraceSinkRecordsNothingAndIsUnchanged(t *testing.T) {
	t.Parallel()

	base := func() Options {
		return Options{
			Config:        config.Defaults(),
			WorkspaceRoot: "   ",
			RunID:         "20260907T120000Z-abcd",
			now:           tickingClock(),
		}
	}

	silent := base()
	silent.PublishTrace = true
	untracedOut, untraced, untracedErr := recording(t, silent)

	sink := trace.NewMemorySink(0)
	traced := base()
	traced.TraceSink = sink
	tracedOut, events, err := recording(t, traced)

	if CodeOf(untracedErr) != CodeWorkspaceRoot || CodeOf(err) != CodeWorkspaceRoot {
		t.Fatalf("the two runs failed differently: %v and %v", untracedErr, err)
	}
	if untracedOut.Status != tracedOut.Status || untracedOut.RunID != tracedOut.RunID {
		t.Errorf("outcome = %s/%s traced, %s/%s untraced",
			tracedOut.Status, tracedOut.RunID, untracedOut.Status, untracedOut.RunID)
	}
	if got, want := eventNames(events), eventNames(untraced); !slices.Equal(got, want) {
		t.Errorf("the traced run published %v, want the untraced run's %v", got, want)
	}
	for _, e := range untraced {
		if _, published := e.(Traced); published {
			t.Error("a run with no sink published a Traced event: PublishTrace has invented a recorder")
		}
	}
	if got := typesOf(sink.Events()); len(got) < 2 || got[0] != trace.TypeRunStart || got[len(got)-1] != trace.TypeRunEnd {
		t.Errorf("the recording is %v, want it to open with %s and close with %s",
			got, trace.TypeRunStart, trace.TypeRunEnd)
	}
}

func TestPhaseCompletedFollowsEveryPhaseChangedWithADuration(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 32)
	sink := trace.NewMemorySink(0)
	s := &session{events: events, clock: tickingClock()}
	s.trace = trace.New(sink, s.now, trace.StartRecord{Kind: trace.StartKindRun})

	for _, phase := range Phases() {
		s.enterPhase(phase, "doing the "+phase.String())
	}
	s.closePhase()
	s.closePhase()
	close(events)

	var seen []Event
	for e := range events {
		seen = append(seen, e)
	}

	want := make([]string, 0, 2*len(Phases()))
	for range Phases() {
		want = append(want, "PhaseChanged", "PhaseCompleted")
	}
	if got := eventNames(seen); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i, phase := range Phases() {
		changed, _ := seen[2*i].(PhaseChanged)
		completed, _ := seen[2*i+1].(PhaseCompleted)
		if changed.Phase != phase || completed.Phase != phase {
			t.Errorf("pair %d = %s/%s, want %s twice", i, changed.Phase, completed.Phase, phase)
		}
		if completed.Duration <= 0 {
			t.Errorf("phase %s completed in %s, want a measured span", phase, completed.Duration)
		}
	}

	if len(s.timing.Phases) != len(Phases()) {
		t.Fatalf("timing recorded %d phases, want %d", len(s.timing.Phases), len(Phases()))
	}
	for i, phase := range Phases() {
		if s.timing.Phases[i].Phase != phase || s.timing.Phases[i].Duration <= 0 {
			t.Errorf("timing.Phases[%d] = %+v, want %s with a measured span", i, s.timing.Phases[i], phase)
		}
	}

	got := typesOf(sink.Events())
	wantTrace := []string{trace.TypeRunStart}
	for range Phases() {
		wantTrace = append(wantTrace, trace.TypePhaseStart, trace.TypePhaseEnd)
	}
	if !slices.Equal(got, wantTrace) {
		t.Errorf("recording = %v, want %v", got, wantTrace)
	}
}

func TestRunIDOptionIsUsedWhenGiven(t *testing.T) {
	t.Parallel()

	const given = "20260907T120000Z-beef"
	sink := trace.NewMemorySink(0)
	out, _, err := recording(t, Options{
		Config:        config.Defaults(),
		WorkspaceRoot: "   ",
		RunID:         given,
		TraceSink:     sink,
		now:           tickingClock(),
	})
	if CodeOf(err) != CodeWorkspaceRoot {
		t.Fatalf("Run = %v, want %s", err, CodeWorkspaceRoot)
	}
	if out.RunID != given {
		t.Errorf("RunID = %q, want the one the caller named (%q)", out.RunID, given)
	}
	events := sink.Events()
	if len(events) == 0 || events[0].Start == nil {
		t.Fatalf("the recording does not open with a run-start: %v", typesOf(events))
	}
	if events[0].Start.RunID != given {
		t.Errorf("run-start.run_id = %q, want %q", events[0].Start.RunID, given)
	}
	if events[0].Start.Kind != trace.StartKindRun {
		t.Errorf("run-start.kind = %q, want %q", events[0].Start.Kind, trace.StartKindRun)
	}

	minted, _, err := recording(t, Options{
		Config:        config.Defaults(),
		WorkspaceRoot: "   ",
		now:           tickingClock(),
	})
	if CodeOf(err) != CodeWorkspaceRoot {
		t.Fatalf("Run = %v, want %s", err, CodeWorkspaceRoot)
	}
	if !strings.HasPrefix(minted.RunID, "20260907T120000Z-") {
		t.Errorf("RunID = %q, want one minted from the run's own clock", minted.RunID)
	}
}

func TestTracedAndPhaseCompletedAreSealedEvents(t *testing.T) {
	t.Parallel()

	var traced Event = Traced{Event: trace.Event{Seq: 1, Type: trace.TypeNote}}
	var completed Event = PhaseCompleted{Phase: PhaseDiscover, Duration: time.Second}

	if got := eventNames([]Event{traced, completed}); !slices.Equal(got, []string{"Traced", "PhaseCompleted"}) {
		t.Errorf("events = %v, want the two new ones", got)
	}
}

func TestMutantResultCarriesKilledByAttemptsAndCoveringPackages(t *testing.T) {
	t.Parallel()

	const id = "aa"
	covering := []string{"example.com/m/a", "example.com/m/b"}

	events := make(chan Event, 8)
	s := &session{events: events}
	st := newState()
	st.display[id] = MutantResult{ID: id, DisplayID: "aa"}
	st.coverage.covering = map[string][]string{id: covering}
	s.hooks(st, 0).Finished(execute.MutantResult{
		ID:       id,
		Final:    mutation.OutcomeKilled,
		KilledBy: "example.com/m/a",
		Attempts: make([]execute.Attempt, 2),
		Duration: 30 * time.Millisecond,
	})
	close(events)
	measured := (<-events).(MutantFinished).Result
	if measured.KilledBy != "example.com/m/a" || measured.Attempts != 2 {
		t.Errorf("measured = killed by %q in %d attempts, want example.com/m/a in 2",
			measured.KilledBy, measured.Attempts)
	}
	if !slices.Equal(measured.CoveringTestPackages, covering) {
		t.Errorf("measured covering packages = %v, want %v", measured.CoveringTestPackages, covering)
	}

	adopted := make(chan Event, 8)
	c := &session{events: adopted}
	cs := newState()
	cs.display[id] = MutantResult{ID: id, DisplayID: "aa"}
	cs.coverage.covering = map[string][]string{id: covering}
	c.adopt(id, cache.Entry{
		Outcome:    mutation.OutcomeKilled,
		DurationMS: 12,
		KilledBy:   "example.com/m/b",
		Attempts:   1,
	}, cs)
	close(adopted)
	var reused MutantResult
	for e := range adopted {
		if finished, ok := e.(MutantFinished); ok {
			reused = finished.Result
		}
	}
	if reused.KilledBy != "example.com/m/b" || reused.Attempts != 1 {
		t.Errorf("adopted = killed by %q in %d attempts, want example.com/m/b in 1",
			reused.KilledBy, reused.Attempts)
	}
	if !slices.Equal(reused.CoveringTestPackages, covering) {
		t.Errorf("adopted covering packages = %v, want %v", reused.CoveringTestPackages, covering)
	}

	killedBy := "example.com/m/a"
	rep := &report.Report{Mutants: []report.Mutant{{
		ID:                   id,
		Outcome:              report.OutcomeSurvived,
		KilledBy:             &killedBy,
		Attempts:             3,
		CoveringTestPackages: covering,
	}}}
	block := notable(st, rep.Mutants)
	if len(block) != 1 {
		t.Fatalf("notable listed %d mutants, want the one survivor", len(block))
	}
	if block[0].KilledBy != killedBy || block[0].Attempts != 3 {
		t.Errorf("notable = killed by %q in %d attempts, want %q in 3",
			block[0].KilledBy, block[0].Attempts, killedBy)
	}
	if !slices.Equal(block[0].CoveringTestPackages, covering) {
		t.Errorf("notable covering packages = %v, want %v", block[0].CoveringTestPackages, covering)
	}
}

func TestNarrowRecordsOneCoverageMapEventPerMutant(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 16)
	sink := trace.NewMemorySink(0)
	s := &session{events: events, clock: tickingClock()}
	s.trace = trace.New(sink, s.now, trace.StartRecord{Kind: trace.StartKindRun})

	st := newState()
	st.display["covered"] = MutantResult{ID: "covered", Path: "a.go", Line: 1, Original: "x"}
	st.display["orphan"] = MutantResult{ID: "orphan", Path: "b.go", Line: 1, Original: "x"}
	runs := []execute.MutantRun{{ID: "covered"}, {ID: "orphan"}}
	bins := []execute.TestBinary{{ImportPath: "example.com/m/a"}}
	profiles := map[string]coverage.Profile{
		"example.com/m/a": {Mode: "set", Blocks: []coverage.Block{{
			File: "example.com/m/a.go", StartLine: 1, StartCol: 1, EndLine: 2, EndCol: 1, NumStmt: 1, Count: 1,
		}}},
	}
	decided := coverageMutants(runs, st)
	mapped := coverage.Map(coverage.Options{ModulePath: "example.com/m", Mutants: decided, Profiles: profiles})
	if mapped.Matched == 0 {
		t.Fatal("the fixture profile does not line up, so this checks the wrong path")
	}

	s.narrow(mapped, decided, bins, runs, st)
	close(events)

	var recorded []trace.CoverageRecord
	for _, e := range sink.Events() {
		if e.Type == trace.TypeCoverageMap {
			recorded = append(recorded, *e.Coverage)
		}
	}
	if len(recorded) != 2 {
		t.Fatalf("recorded %d coverage-map events, want one per mapped mutant: %+v", len(recorded), recorded)
	}
	byID := map[string]trace.CoverageRecord{}
	for _, record := range recorded {
		byID[record.MutantID] = record
	}
	covered := byID["covered"]
	if covered.Path != "a.go" || covered.StartLine != 1 || !slices.Equal(covered.Covering, []string{"example.com/m/a"}) {
		t.Errorf("covered = %+v, want a.go:1 reached by example.com/m/a", covered)
	}
	if covered.Uncovered {
		t.Error("a mutant a binary reaches was recorded as uncovered")
	}
	orphan := byID["orphan"]
	if !orphan.Uncovered || len(orphan.Covering) != 0 {
		t.Errorf("orphan = %+v, want it recorded as uncovered with no covering binary", orphan)
	}
}

func TestCachePhaseRecordsOpenLookupAndStoreDecisions(t *testing.T) {
	t.Parallel()

	f := newCacheFixture(t, t.TempDir())
	f.opts.Config.Mutation.Expect = []config.Expectation{
		{ID: ids[1], Reason: "the flag is only read by the debug logger"},
	}

	cold := &session{clock: tickingClock()}
	coldSink := trace.NewMemorySink(0)
	cold.trace = trace.New(coldSink, cold.now, trace.StartRecord{Kind: trace.StartKindRun})
	coldState := newState()
	cold.cachePhase(f.opts, f.catalog, f.out, f.runs(), coldState)
	cold.storeOutcomes(f.opts, measured(), coldState)

	opens, lookups, stores := cacheRecords(coldSink.Events())
	if len(opens) != 1 || opens[0].Result != trace.CacheResultOpened {
		t.Fatalf("open = %+v, want one opened store", opens)
	}
	if opens[0].Directory == "" || opens[0].ContextKey == "" {
		t.Errorf("open = %+v, want the directory and the context key it was opened under", opens[0])
	}
	if got, want := len(lookups), len(ids); got != want {
		t.Fatalf("recorded %d lookups, want one per selected mutant (%d)", got, want)
	}
	byID := map[string]trace.CacheRecord{}
	for _, record := range lookups {
		byID[record.MutantID] = record
	}
	if got := byID[ids[0]].Result; got != trace.CacheResultMiss {
		t.Errorf("a cold lookup answered %q, want %q", got, trace.CacheResultMiss)
	}
	if got := byID[ids[1]].Result; got != trace.CacheResultExpected {
		t.Errorf("the expected mutant's lookup answered %q, want %q", got, trace.CacheResultExpected)
	}

	want := map[string]string{
		ids[0]: trace.CacheResultWritten,
		ids[1]: trace.CacheResultExpected,
		ids[2]: trace.CacheResultNotCacheable,
		ids[3]: trace.CacheResultNotCacheable,
	}
	got := map[string]string{}
	for _, record := range stores {
		got[record.MutantID] = record.Result
	}
	if len(got) != len(want) {
		t.Fatalf("recorded %d stores, want one per measured mutant: %+v", len(got), stores)
	}
	for id, result := range want {
		if got[id] != result {
			t.Errorf("the store of %s answered %q, want %q", id[:2], got[id], result)
		}
	}

	warm := &session{clock: tickingClock()}
	warmSink := trace.NewMemorySink(0)
	warm.trace = trace.New(warmSink, warm.now, trace.StartRecord{Kind: trace.StartKindRun})
	warm.cachePhase(f.opts, f.catalog, f.out, f.runs(), newState())
	_, warmLookups, _ := cacheRecords(warmSink.Events())
	hits := 0
	for _, record := range warmLookups {
		if record.Result != trace.CacheResultHit {
			continue
		}
		hits++
		if record.Outcome == "" {
			t.Errorf("the hit on %s recorded no outcome", record.MutantID[:2])
		}
	}
	if hits != 1 {
		t.Errorf("the warm run recorded %d hits, want the one reusable outcome it stored", hits)
	}
}

func cacheRecords(events []trace.Event) (opens, lookups, stores []trace.CacheRecord) {
	for _, e := range events {
		if e.Type != trace.TypeCache {
			continue
		}
		switch e.Cache.Op {
		case trace.CacheOpOpen:
			opens = append(opens, *e.Cache)
		case trace.CacheOpLookup:
			lookups = append(lookups, *e.Cache)
		case trace.CacheOpStore:
			stores = append(stores, *e.Cache)
		}
	}
	return opens, lookups, stores
}

func TestSweepTemporaryRecordsWhatItCollected(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	dead := abandonedDirectory(t, parent, snapshot.DirPrefix+"dead")

	sink := trace.NewMemorySink(0)
	s := &session{clock: tickingClock()}
	s.trace = trace.New(sink, s.now, trace.StartRecord{Kind: trace.StartKindRun})

	result := s.sweepTemporary(parent)
	if !slices.Equal(result.Removed, []string{dead}) {
		t.Fatalf("the sweep reported %v removed, want %v", result.Removed, []string{dead})
	}

	var records []trace.SweepRecord
	for _, e := range sink.Events() {
		if e.Type == trace.TypeSweep {
			records = append(records, *e.Sweep)
		}
	}
	if len(records) != 1 {
		t.Fatalf("recorded %d sweeps, want one", len(records))
	}
	record := records[0]
	if record.Parent != parent {
		t.Errorf("sweep.parent = %q, want %q", record.Parent, parent)
	}
	if record.Live != 0 || record.Kept != 0 {
		t.Errorf("sweep = %+v, want nothing live and nothing kept", record)
	}
	if !slices.Equal(record.Removed, []string{dead}) {
		t.Errorf("sweep.removed = %v, want %v", record.Removed, []string{dead})
	}
	if record.Error != "" {
		t.Errorf("a clean sweep recorded the error %q", record.Error)
	}
}

func TestPublishingATraceDoesNotStallTheRecordingBehindASlowConsumer(t *testing.T) {
	t.Parallel()

	const (
		producers   = 4
		perProducer = 50
		crawl       = 50 * time.Millisecond
		budget      = 2 * time.Second
	)

	events := make(chan Event)
	var slow atomic.Bool
	slow.Store(true)
	consumed := make(chan int, 1)
	go func() {
		seen := 0
		for range events {
			seen++
			if slow.Load() {
				time.Sleep(crawl)
			}
		}
		consumed <- seen
	}()

	s := &session{events: events}
	published := newEventSink(s)
	s.published = published
	kept := trace.NewMemorySink(0)
	s.trace = trace.New(trace.NewTeeSink(kept, published), nil, trace.StartRecord{Kind: trace.StartKindRun})

	started := time.Now()
	var wg sync.WaitGroup
	for range producers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perProducer {
				s.trace.Exec(trace.ExecRecord{Kind: trace.ExecKindMutantRun})
			}
		}()
	}
	wg.Wait()
	recording := time.Since(started)

	slow.Store(false)
	s.drainPublished()
	close(events)
	<-consumed

	if recording > budget {
		t.Errorf("recording %d events took %s, want under %s: the recorder is waiting for the consumer",
			producers*perProducer, recording, budget)
	}
	if got, want := len(kept.Events()), producers*perProducer+1; got != want {
		t.Errorf("the sink kept %d events, want %d — the run-start and every record", got, want)
	}
}

func TestARunIDThatIsNotOneIsRefused(t *testing.T) {
	t.Parallel()

	refused := map[string]string{
		"not an id at all":     "nonsense",
		"a path":               "../../etc/passwd",
		"the wrong length":     "20260907T120000Z-abc",
		"upper case hex":       "20260907T120000Z-ABCD",
		"no separator":         "20260907T120000Zabcd",
		"a missing zone":       "20260907T120000-abcd",
		"trailing whitespace":  "20260907T120000Z-abcd ",
		"an id with a suffix":  "20260907T120000Z-abcd.json",
		"letters in the clock": "2026090xT120000Z-abcd",
	}
	for name, id := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			private := t.TempDir()
			sink := trace.NewMemorySink(0)
			out, _, err := recording(t, Options{
				Config:        config.Defaults(),
				WorkspaceRoot: t.TempDir(),
				RunID:         id,
				TempDirectory: private,
				TraceSink:     sink,
				now:           tickingClock(),
			})
			if CodeOf(err) != CodeRunID {
				t.Fatalf("Run with the id %q = %v, want %s", id, err, CodeRunID)
			}
			if !strings.Contains(err.Error(), strconv.Quote(id)) {
				t.Errorf("the failure does not quote the id it refused: %v", err)
			}
			if left := testkit.Entries(t, private); len(left) != 0 {
				t.Errorf("the refused run left %v in its temporary directory", left)
			}
			if !runIDPattern.MatchString(out.RunID) {
				t.Errorf("the refused run reports the id %q, want one of the minted form", out.RunID)
			}
			if events := sink.Events(); len(events) == 0 || events[0].Start.RunID == id {
				t.Errorf("the recording opened under the refused id")
			}
		})
	}

	const minted = "20260907T120000Z-abcd"
	out, _, err := recording(t, Options{
		Config:        config.Defaults(),
		WorkspaceRoot: "   ",
		RunID:         minted,
		now:           tickingClock(),
	})
	if CodeOf(err) != CodeWorkspaceRoot {
		t.Fatalf("Run with a well-formed id = %v, want it to get as far as %s", err, CodeWorkspaceRoot)
	}
	if out.RunID != minted {
		t.Errorf("RunID = %q, want the caller's %q", out.RunID, minted)
	}
	if got := NewRunID(time.Now()); !runIDPattern.MatchString(got) {
		t.Errorf("NewRunID minted %q, which this check would refuse", got)
	}
}

func TestSweepReadsTheRunsOwnClock(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	legacy := filepath.Join(parent, snapshot.DirPrefix+"from-an-older-binary")
	if err := os.Mkdir(legacy, 0o755); err != nil {
		t.Fatal(err)
	}

	spared := &session{clock: func() time.Time { return time.Now() }}
	if got := spared.sweepTemporary(parent); len(got.Removed) != 0 {
		t.Fatalf("the sweep removed %v, want a young unowned directory left alone", got.Removed)
	}

	later := &session{clock: func() time.Time { return time.Now().Add(365 * 24 * time.Hour) }}
	got := later.sweepTemporary(parent)
	if !slices.Equal(got.Removed, []string{legacy}) {
		t.Errorf("the sweep removed %v, want %v: the age rule is reading the wall clock rather than the run's",
			got.Removed, []string{legacy})
	}
}

func TestNotesAreRecordedRightAfterRunStart(t *testing.T) {
	t.Parallel()

	notes := []trace.NoteRecord{
		{Kind: trace.NoteTraceUnavailable, Detail: "the trace directory was refused"},
		{Kind: trace.NoteTraceGC, Code: "GOM1013", Detail: "removed 2 recordings"},
	}
	base := func() Options {
		return Options{
			Config:        config.Defaults(),
			WorkspaceRoot: "   ",
			RunID:         "20260907T120000Z-abcd",
			now:           tickingClock(),
		}
	}

	opts := base()
	sink := trace.NewMemorySink(0)
	opts.TraceSink = sink
	opts.Notes = notes
	if _, _, err := recording(t, opts); CodeOf(err) != CodeWorkspaceRoot {
		t.Fatalf("Run = %v, want the workspace root refusal", err)
	}

	events := sink.Events()
	if len(events) < 1+len(notes) {
		t.Fatalf("the run recorded %d events, want at least the run-start and %d notes", len(events), len(notes))
	}
	if events[0].Type != trace.TypeRunStart {
		t.Fatalf("the recording opens with a %s, want a run-start", events[0].Type)
	}
	for i, want := range notes {
		event := events[1+i]
		if event.Type != trace.TypeNote || event.Note == nil {
			t.Fatalf("event %d is a %s, want the note %q right after the run-start", event.Seq, event.Type, want.Kind)
		}
		if *event.Note != want {
			t.Errorf("event %d recorded %+v, want %+v", event.Seq, *event.Note, want)
		}
		if event.Seq != int64(2+i) || event.Timestamp == "" {
			t.Errorf("event %d is stamped %d/%q, want the recorder's own numbering and clock",
				event.Seq, event.Seq, event.Timestamp)
		}
	}

	plain := base()
	plainSink := trace.NewMemorySink(0)
	plain.TraceSink = plainSink
	if _, _, err := recording(t, plain); CodeOf(err) != CodeWorkspaceRoot {
		t.Fatalf("Run = %v, want the workspace root refusal", err)
	}
	for _, event := range plainSink.Events() {
		if event.Type == trace.TypeNote {
			t.Errorf("a run with no notes recorded %+v", *event.Note)
		}
	}
}

func TestAPublishedRecordingCarriesTheDigestAndNotTheBytes(t *testing.T) {
	t.Parallel()

	captured := []byte(strings.Repeat("--- FAIL: TestClamp (0.00s)\n", 64))
	events := make(chan Event, 16)
	s := &session{events: events}
	kept := trace.NewMemorySink(0)
	s.trace = trace.New(s.sink(Options{TraceSink: kept, PublishTrace: true}), tickingClock(),
		trace.StartRecord{Kind: trace.StartKindRun, RunID: "20260907T120000Z-abcd"})
	s.trace.Exec(trace.ExecRecord{
		Kind:   trace.ExecKindMutantRun,
		Argv:   []string{"/tmp/go-mutants-tmp-1/clamp.test", "-test.timeout=10s"},
		Output: captured,
	})
	s.trace.MutantExec(trace.MutantRecord{ID: strings.Repeat("a", 64), Attempt: 1, OutputTail: "--- FAIL"})
	s.drainPublished()
	close(events)

	var published []trace.Event
	for e := range events {
		if traced, ok := e.(Traced); ok {
			published = append(published, traced.Event)
		}
	}

	var execs int
	for _, e := range published {
		if e.Type == trace.TypeExec {
			execs++
			if len(e.Exec.Output) != 0 {
				t.Errorf("a published exec carries %d bytes of captured output", len(e.Exec.Output))
			}
			if e.Exec.OutputBytes != len(captured) || e.Exec.OutputSHA256 == "" {
				t.Errorf("the published exec lost the digest of what it captured: %d bytes, sha %q",
					e.Exec.OutputBytes, e.Exec.OutputSHA256)
			}
		}
		if e.Type == trace.TypeMutantExec && e.Mutant.OutputTail != "" {
			t.Errorf("a published attempt carries the killing binary's output: %q", e.Mutant.OutputTail)
		}
	}
	if execs != 1 {
		t.Fatalf("published %d exec events, want 1: %v", execs, typesOf(published))
	}

	for _, e := range kept.Events() {
		if e.Type == trace.TypeExec && len(e.Exec.Output) != len(captured) {
			t.Errorf("the run's own recording kept %d of %d bytes", len(e.Exec.Output), len(captured))
		}
	}
}
