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

// traceTick is how far the tests' clock moves on every reading. It is a whole
// number of milliseconds because the contract records durations in whole
// milliseconds, so anything smaller would record every span as zero and the
// assertions about durations would pass for a recorder that measured nothing.
const traceTick = 5 * time.Millisecond

// tickingClock is the clock these tests hand the engine: every reading is one
// [traceTick] later than the last, so every span is non-zero and every event
// carries a distinct timestamp without a test having to script one.
func tickingClock() func() time.Time {
	clock := testkit.NewClock(time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC))
	clock.Tick(traceTick)
	return clock.Now
}

// recording runs the engine with a drained event channel and returns what both
// streams saw: the engine's own events, and the trace the sink kept.
//
// It is the unit-test counterpart of the integration suite's `collect`, and it
// is separate because the engine's sends block: a consumer that is not already
// running when Run is called deadlocks the run.
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

// eventNames names each engine event by its type, so that two streams can be
// compared as data.
func eventNames(events []Event) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, strings.TrimPrefix(fmt.Sprintf("%T", e), "engine."))
	}
	return names
}

// typesOf names the trace events a recording holds, in order.
func typesOf(events []trace.Event) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, e.Type)
	}
	return names
}

// TestRunWithoutATraceSinkRecordsNothingAndIsUnchanged is the promise the whole
// workstream rests on: an untraced run is the run there was before there was a
// recorder to leave out.
//
// The recorder is nil when no sink was given — "no trace" is one representation
// rather than two — so every call site records unconditionally and there is no
// branch for a verdict to come to depend on. What this checks is that the branch
// really is absent: the same invocation, with and without a sink, publishes the
// same engine events and reaches the same outcome, and the untraced one asks for
// nothing to be published even when [Options.PublishTrace] is set.
func TestRunWithoutATraceSinkRecordsNothingAndIsUnchanged(t *testing.T) {
	t.Parallel()

	// A run that fails before it copies anything: what is being compared is the
	// two code paths through Run, not the work between them, and a unit test
	// cannot have a toolchain. The traced run of a whole pipeline is
	// TestATracedRunRecordsEveryPhaseStageAndSubprocessOfTheKillableFixture.
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
	// And the sink really was wired up, or the comparison above would hold for
	// an engine that ignores the option.
	if got := typesOf(sink.Events()); len(got) < 2 || got[0] != trace.TypeRunStart || got[len(got)-1] != trace.TypeRunEnd {
		t.Errorf("the recording is %v, want it to open with %s and close with %s",
			got, trace.TypeRunStart, trace.TypeRunEnd)
	}
}

// TestPhaseCompletedFollowsEveryPhaseChangedWithADuration pins the pairing the
// verbose renderer and the report's timing both read.
//
// It drives the phase seam directly rather than a whole run, because the phases
// of a whole run need a toolchain, a snapshot and a test suite between them —
// and none of that is what is being pinned here. What is pinned is that a phase
// is announced once, closed once, closed before the next one opens, and timed
// when it closes, on the engine's stream and in the recording alike.
func TestPhaseCompletedFollowsEveryPhaseChangedWithADuration(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 32)
	sink := trace.NewMemorySink(0)
	s := &session{events: events, clock: tickingClock()}
	s.trace = trace.New(sink, s.now, trace.StartRecord{Kind: trace.StartKindRun})

	for _, phase := range Phases() {
		s.enterPhase(phase, "doing the "+phase.String())
	}
	// The last phase is closed by Run on every return path; here that is this
	// call, and it is idempotent so that a defer and an early return are one
	// span.
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

	// The same spans, in the same order, in [RunOutcome.Timing].
	if len(s.timing.Phases) != len(Phases()) {
		t.Fatalf("timing recorded %d phases, want %d", len(s.timing.Phases), len(Phases()))
	}
	for i, phase := range Phases() {
		if s.timing.Phases[i].Phase != phase || s.timing.Phases[i].Duration <= 0 {
			t.Errorf("timing.Phases[%d] = %+v, want %s with a measured span", i, s.timing.Phases[i], phase)
		}
	}

	// And the recording says the same, one start/end pair per phase and never
	// two ends for one start.
	got := typesOf(sink.Events())
	wantTrace := []string{trace.TypeRunStart}
	for range Phases() {
		wantTrace = append(wantTrace, trace.TypePhaseStart, trace.TypePhaseEnd)
	}
	if !slices.Equal(got, wantTrace) {
		t.Errorf("recording = %v, want %v", got, wantTrace)
	}
}

// TestRunIDOptionIsUsedWhenGiven keeps one run to one identity.
//
// internal/cli mints the id so that it can name the trace directory before the
// engine starts, and the report has to be filed under that same id or the two
// cannot be paired afterwards.
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

	// Nothing named means a fresh id from the run's own clock, which is what
	// every caller before internal/cli grew a `--trace` flag passes.
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

// TestTracedAndPhaseCompletedAreSealedEvents is a compile-time assertion: both
// travel on the engine's stream, so both have to be part of the sealed
// interface a renderer switches over.
func TestTracedAndPhaseCompletedAreSealedEvents(t *testing.T) {
	t.Parallel()

	// The declarations are the assertion: the interface's marker method is
	// unexported, so a type outside this package cannot satisfy it and a type
	// inside it that forgot to would not compile here.
	var traced Event = Traced{Event: trace.Event{Seq: 1, Type: trace.TypeNote}}
	var completed Event = PhaseCompleted{Phase: PhaseDiscover, Duration: time.Second}

	if got := eventNames([]Event{traced, completed}); !slices.Equal(got, []string{"Traced", "PhaseCompleted"}) {
		t.Errorf("events = %v, want the two new ones", got)
	}
}

// TestMutantResultCarriesKilledByAttemptsAndCoveringPackages closes the gap
// between the report and the stream.
//
// All three facts were in the document and in none of the events, so `-v` could
// not say which binary caught a mutant, how many attempts it took, or which
// binaries reach a survivor's line without re-reading the report the run has not
// written yet. They arrive by three routes — a measured mutant, a mutant adopted
// from the cache, and the summary block read back out of the document — and each
// one is checked, because a fact carried by two of the three is a renderer that
// prints it sometimes.
func TestMutantResultCarriesKilledByAttemptsAndCoveringPackages(t *testing.T) {
	t.Parallel()

	const id = "aa"
	covering := []string{"example.com/m/a", "example.com/m/b"}

	// Measured: internal/execute's result, folded into the display data.
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

	// Adopted: the same three facts, second-hand, out of the cache entry.
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

	// And the summary block, which reads the published document rather than the
	// run beside it.
	killedBy := "example.com/m/a"
	rep := &report.Report{Mutants: []report.Mutant{{
		ID:                   id,
		Outcome:              report.OutcomeSurvived,
		KilledBy:             &killedBy,
		Attempts:             3,
		CoveringTestPackages: covering,
	}}}
	block := notable(st, rep)
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

// TestNarrowRecordsOneCoverageMapEventPerMutant is the account of the decision
// that skips most of a run.
//
// A coverage-guided run does not execute the mutants nothing reaches, and the
// only evidence for that is the mapping. One event per mapped mutant — the
// covered ones with the binaries that reach them, the uncovered ones saying so
// explicitly — is what lets a reader check the skipping rather than take it.
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

// TestCachePhaseRecordsOpenLookupAndStoreDecisions is the one event that
// explains an absence of work.
//
// A warm run executes almost nothing, and without the cache's own account there
// is no way to tell that from a run that decided there was nothing to do. The
// store it opened, every lookup and what it answered, and every write-back are
// recorded, expectations included: a mutant the ledger names is never asked
// about, and "never asked" has to be distinguishable from "asked and missed".
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

	// And the second run says which answers it did not have to compute.
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

// cacheRecords splits a recording's cache events by operation, in order.
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

// TestSweepTemporaryRecordsWhatItCollected is the run's account of the disk it
// took back.
//
// It used to be recorded nowhere on purpose: it is a fact about the machine
// rather than about the workspace, so it has no place in a report two runs are
// compared by. A recording is exactly where it does belong — a run that paused
// to delete four gigabytes has an explanation for the pause, and a sweep that
// left something behind has named it.
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

// TestPublishingATraceDoesNotStallTheRecordingBehindASlowConsumer is the
// back-pressure boundary of `-vv`.
//
// A recorded event is handed to every sink under the recorder's own lock, so a
// sink that blocks blocks the recorder — and with the trace published straight
// onto the engine's stream, "blocks" meant "waits for a terminal to draw a
// line". Every execution worker recording an attempt then queues behind one slow
// consumer, and asking for verbosity would have made the run itself slower,
// which is a diagnostic changing the thing it is a diagnostic of.
//
// The hand-off is a bounded buffer with one forwarding goroutine, so the
// recorder's lock is never held across a send onto the stream. A consumer slower
// than the buffer still applies back-pressure eventually — that is the point of
// bounding it — but it applies it to the forwarder rather than to the run.
func TestPublishingATraceDoesNotStallTheRecordingBehindASlowConsumer(t *testing.T) {
	t.Parallel()

	const (
		producers   = 4
		perProducer = 50
		crawl       = 50 * time.Millisecond
		// Generous: serialised behind the consumer the burst below would take
		// (4×50 + 1) × 50ms, which is over ten seconds.
		budget = 2 * time.Second
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

	// The consumer is let off the leash before the teardown, so that draining
	// what is still queued costs the test nothing.
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

// TestARunIDThatIsNotOneIsRefused keeps a caller's identity from naming files
// the run has no business creating.
//
// The id names things: the run's own immutable document in the history store,
// and — from the change after this one — the directory a recording is written
// into. A caller that passes `../../etc` or an empty-looking string is not
// asking for a differently named run, it is making a mistake about the
// invocation, and the cheapest place to find that out is before a workspace has
// been copied.
//
// It is refused rather than replaced. A caller that minted an id has something
// of its own filed under it, and running under a quietly different one would
// leave the two unable to find each other — which is exactly the pairing the
// option exists to make possible.
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
			// Before anything was copied: the point of checking an invocation
			// is to check it before it costs a snapshot.
			if left := testkit.Entries(t, private); len(left) != 0 {
				t.Errorf("the refused run left %v in its temporary directory", left)
			}
			// The run still has a name, so that the failure has one, and it is
			// a real one rather than the value that was refused.
			if !runIDPattern.MatchString(out.RunID) {
				t.Errorf("the refused run reports the id %q, want one of the minted form", out.RunID)
			}
			if events := sink.Events(); len(events) == 0 || events[0].Start.RunID == id {
				t.Errorf("the recording opened under the refused id")
			}
		})
	}

	// And the form itself is accepted, which is what keeps this a check on the
	// spelling rather than on whether anybody passed one.
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
	// The id NewRunID mints is one this check accepts, or the two would
	// disagree about what a run id is.
	if got := NewRunID(time.Now()); !runIDPattern.MatchString(got) {
		t.Errorf("NewRunID minted %q, which this check would refuse", got)
	}
}

// TestSweepReadsTheRunsOwnClock is what makes a recorded sweep pinnable.
//
// The one decision collection makes without a marker to read is about age: a
// directory from before this package existed is unowned, so a day of inactivity
// is the only evidence available that nobody is using it. Asking the wall clock
// for that made the decision — and therefore the `sweep` event it is recorded in
// — depend on what time the test ran at. It asks the run's own clock instead,
// which is the same clock every timestamp in the recording is stamped from, so a
// run and its account of itself agree about when they happened.
func TestSweepReadsTheRunsOwnClock(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	// Unowned: no marker and no lock, which is the only shape the age rule is
	// consulted for. It was made a moment ago, so the wall clock would spare it.
	legacy := filepath.Join(parent, snapshot.DirPrefix+"from-an-older-binary")
	if err := os.Mkdir(legacy, 0o755); err != nil {
		t.Fatal(err)
	}

	spared := &session{clock: func() time.Time { return time.Now() }}
	if got := spared.sweepTemporary(parent); len(got.Removed) != 0 {
		t.Fatalf("the sweep removed %v, want a young unowned directory left alone", got.Removed)
	}

	// The same directory, swept by a run whose clock says a year has passed.
	// Nothing on disk changed; only the clock the decision is made against.
	later := &session{clock: func() time.Time { return time.Now().Add(365 * 24 * time.Hour) }}
	got := later.sweepTemporary(parent)
	if !slices.Equal(got.Removed, []string{legacy}) {
		t.Errorf("the sweep removed %v, want %v: the age rule is reading the wall clock rather than the run's",
			got.Removed, []string{legacy})
	}
}

// TestNotesAreRecordedRightAfterRunStart puts the caller's notes where they
// happened.
//
// The two things worth recording about a recording are decided outside the run:
// a trace directory refused before the engine was called, and the collection of
// older recordings that ran before this one opened its own. Both are facts about
// the moment the recording began, so they belong immediately after the run-start
// and nowhere else — not appended to the end, where they would have to displace
// the run-end a reader relies on being the last line, and not left on a console
// where the account of the run does not have them.
//
// The recorder stamps them, which is the other half: a note carries the sequence
// number, timestamp and elapsed time of the moment it was recorded, exactly like
// every other event, rather than a moment its author had to invent.
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

	// And a run given none records none: the notes are the caller's, not a
	// section of every recording.
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

// TestAPublishedRecordingCarriesTheDigestAndNotTheBytes is the price of `-vv`,
// held down.
//
// A recorded execution carries the captured output so that the sink writing it
// to disk can preserve it beside the stream. The fan-out onto the event stream
// is a copy for a screen: it deep-copies every event, the renderer never prints
// those bytes — an `exec` line is the exit status, the duration and the argument
// vector — and a mutant run can capture a megabyte of them. Publishing them
// would make watching a run cost a copy of every test binary's output.
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

	// The sink that was asked to keep the bytes still has them, which is what
	// makes this a property of the fan-out rather than of the recorder.
	for _, e := range kept.Events() {
		if e.Type == trace.TypeExec && len(e.Exec.Output) != len(captured) {
			t.Errorf("the run's own recording kept %d of %d bytes", len(e.Exec.Output), len(captured))
		}
	}
}
