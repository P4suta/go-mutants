// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The recording of a real run, judged against the run it is a recording of.
//
// Everything the trace claims is checkable only against a toolchain: which
// subprocesses a run starts, in which phase, how many attempts a mutant took,
// which binary killed it. A unit test can prove the recorder records what it is
// handed; only a run can prove the engine hands it the run.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/engine/...
//
// The comment above is deliberately not a package doc — integration_test.go
// carries this package's — which is what the blank line below is for.

package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/trace"
)

// tracedOptions is [options] with a recording attached and three baseline runs.
//
// The third baseline run is the only setting here that is not the suite's
// default, and it earns its two seconds: one observation cannot show that each
// one is recorded as a stage of its own with its own place in the sequence,
// which is the whole of what a per-run stage detail is for.
func tracedOptions(t *testing.T, name string) (Options, *trace.MemorySink) {
	t.Helper()
	sink := trace.NewMemorySink(0)
	opts := options(t, name)
	opts.Config.Test.BaselineRuns = 3
	opts.TraceSink = sink
	return opts, sink
}

// stageKey names one recorded stage the way a reader has to read it: by phase
// and name together, because `build` happens in the baseline and again in the
// report, and `discover` is both a phase and a stage inside another one.
type stageKey struct{ phase, name string }

func (k stageKey) String() string { return k.phase + "/" + k.name }

// execKinds tallies a recording's `exec` events by their label.
func execKinds(events []trace.Event) map[string]int {
	kinds := make(map[string]int)
	for _, e := range events {
		if e.Type == trace.TypeExec {
			kinds[e.Exec.Kind]++
		}
	}
	return kinds
}

// eventsOfType returns the events of one type, in order.
func eventsOfType(events []trace.Event, kind string) []trace.Event {
	var out []trace.Event
	for _, e := range events {
		if e.Type == kind {
			out = append(out, e)
		}
	}
	return out
}

// TestATracedRunRecordsEveryPhaseStageAndSubprocessOfTheKillableFixture is the
// account of one whole run, checked against the run.
//
// It is one test rather than ten because the claim is a single one: the
// recording *is* the run. Splitting it would mean paying for a full pipeline
// several times over to assert one paragraph of the same stream each time, and
// the interesting failures are the joins — a mutant execution whose killing
// binary disagrees with the report's, a phase that was left open, a stage
// nobody closed.
func TestATracedRunRecordsEveryPhaseStageAndSubprocessOfTheKillableFixture(t *testing.T) {
	t.Parallel()

	opts, sink := tracedOptions(t, "killable")
	outcome, published, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK || outcome.Report == nil {
		t.Fatalf("status = %s with report %v, want a completed run", outcome.Status, outcome.Report != nil)
	}
	events := sink.Events()
	if len(events) == 0 {
		t.Fatal("the run recorded nothing")
	}

	// Every line of it is a document of the published contract. This is the
	// assertion that keeps a field added in a hurry from producing a recording
	// no consumer can read.
	for _, event := range events {
		line, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			t.Fatalf("encoding event %d (%s): %v", event.Seq, event.Type, marshalErr)
		}
		if validateErr := schemas.Validate(schemas.TraceEventV1, line); validateErr != nil {
			t.Errorf("event %d (%s) does not satisfy %s: %v\n%s",
				event.Seq, event.Type, schemas.TraceEventV1, validateErr, line)
		}
	}

	// The recording opens by saying whose it is, and the run id is the one the
	// report was filed under: a recording that could not be paired with its
	// report is a recording of nothing in particular.
	first := events[0]
	if first.Type != trace.TypeRunStart || first.Start == nil {
		t.Fatalf("the first event is %s, want %s", first.Type, trace.TypeRunStart)
	}
	if first.Start.RunID != outcome.Report.RunID {
		t.Errorf("run-start.run_id = %q, want the report's %q", first.Start.RunID, outcome.Report.RunID)
	}
	if first.Start.ToolVersion != testToolVersion || first.Start.PID == 0 || first.Start.Root == "" {
		t.Errorf("run-start = %+v, want the tool version, the pid and the root", *first.Start)
	}

	// The phases, in order, each closed before the next opens. A phase that
	// stayed open would make every stage after it read as belonging to it.
	var phases []string
	open := ""
	for _, e := range events {
		switch e.Type {
		case trace.TypePhaseStart:
			if open != "" {
				t.Fatalf("the %s phase started while %s was still open", e.Phase.Name, open)
			}
			open = e.Phase.Name
			phases = append(phases, e.Phase.Name)
		case trace.TypePhaseEnd:
			if open != e.Phase.Name {
				t.Fatalf("the %s phase ended while %q was open", e.Phase.Name, open)
			}
			if e.Phase.DurationMS == nil {
				t.Errorf("the %s phase ended without a duration", e.Phase.Name)
			}
			open = ""
		}
	}
	if open != "" {
		t.Errorf("the %s phase was never closed", open)
	}
	wantPhases := []string{trace.PhaseDiscover, trace.PhaseBaseline, trace.PhaseMutate, trace.PhaseReport}
	if !slices.Equal(phases, wantPhases) {
		t.Errorf("phases = %v, want %v", phases, wantPhases)
	}
	// And the outcome carries the same spans, so that a report can state them
	// without re-reading the stream.
	if len(outcome.Timing.Phases) != len(wantPhases) {
		t.Errorf("Timing.Phases = %+v, want one span per phase", outcome.Timing.Phases)
	}

	// Every stage the engine names, finished. A stage that started and never
	// finished is a phase whose account has a hole in it exactly where the run
	// stopped doing what it said it was doing.
	want := []stageKey{
		{trace.PhaseDiscover, "toolchain"},
		{trace.PhaseDiscover, "sweep"},
		{trace.PhaseDiscover, "snapshot"},
		{trace.PhaseDiscover, "scope"},
		{trace.PhaseBaseline, "build"},
		{trace.PhaseBaseline, "test"},
		{trace.PhaseBaseline, "timeout"},
		{trace.PhaseMutate, "discover"},
		{trace.PhaseMutate, "catalog"},
		{trace.PhaseMutate, "validate"},
		{trace.PhaseMutate, "instrumented-baseline"},
		{trace.PhaseMutate, "drift"},
		{trace.PhaseMutate, "selection"},
		{trace.PhaseMutate, "build-binaries"},
		{trace.PhaseMutate, "coverage-profile"},
		{trace.PhaseMutate, "coverage-narrow"},
		{trace.PhaseMutate, "cache-lookup"},
		{trace.PhaseMutate, "execute"},
		{trace.PhaseMutate, "cache-store"},
		{trace.PhaseReport, "build"},
		{trace.PhaseReport, "history"},
		{trace.PhaseReport, "artifacts"},
	}
	started := map[stageKey]int{}
	finished := map[stageKey]int{}
	for _, e := range eventsOfType(events, trace.TypeStage) {
		key := stageKey{e.Stage.Phase, e.Stage.Name}
		switch e.Stage.State {
		case trace.StateStarted:
			started[key]++
			if e.Stage.DurationMS != nil || e.Stage.Result != "" {
				t.Errorf("the started %s carries a result or a duration: %+v", key, *e.Stage)
			}
		case trace.StateFinished:
			finished[key]++
			if e.Stage.DurationMS == nil {
				t.Errorf("the finished %s carries no duration", key)
			}
			if e.Stage.Result != trace.ResultSucceeded {
				t.Errorf("%s finished %q, want %q in a run that completed",
					key, e.Stage.Result, trace.ResultSucceeded)
			}
		}
	}
	for _, key := range want {
		if finished[key] == 0 {
			t.Errorf("no finished %s stage was recorded", key)
		}
		if started[key] != finished[key] {
			t.Errorf("%s started %d times and finished %d", key, started[key], finished[key])
		}
	}
	// The baseline's timed runs are one stage each, so that a suite that got
	// slower between two observations shows the second one.
	if got := finished[stageKey{trace.PhaseBaseline, "test"}]; got != opts.Config.Test.BaselineRuns {
		t.Errorf("the baseline recorded %d test stages, want one per configured run (%d)",
			got, opts.Config.Test.BaselineRuns)
	}

	// Every subprocess, labelled. The counts that are a property of this
	// fixture — one module, one package with tests, one test binary — are
	// written out; the rest are floors, because how many compiles validation
	// spends is its own business.
	kinds := execKinds(events)
	exact := map[string]int{
		trace.ExecKindGoVersion:            1,
		trace.ExecKindScopeList:            1,
		trace.ExecKindBaselineBuild:        1,
		trace.ExecKindBaselineTest:         opts.Config.Test.BaselineRuns,
		trace.ExecKindInstrumentedBaseline: 1,
		trace.ExecKindGoList:               1,
	}
	for kind, n := range exact {
		if kinds[kind] != n {
			t.Errorf("recorded %d %s executions, want %d", kinds[kind], kind, n)
		}
	}
	for _, kind := range []string{
		trace.ExecKindValidateBuild,
		trace.ExecKindGoTestC,
		trace.ExecKindCoverageRun,
		trace.ExecKindCovdataTextfmt,
		trace.ExecKindMutantRun,
	} {
		if kinds[kind] == 0 {
			t.Errorf("no %s execution was recorded", kind)
		}
	}
	for _, e := range eventsOfType(events, trace.TypeExec) {
		if !slices.Contains(trace.ExecKinds(), e.Exec.Kind) {
			t.Errorf("event %d is labelled %q, which is not a kind of this contract", e.Seq, e.Exec.Kind)
		}
		if len(e.Exec.Argv) == 0 {
			t.Errorf("the %s execution at %d recorded no argv", e.Exec.Kind, e.Seq)
		}
	}
	// The two kinds whose subject is the whole point of having one.
	for _, e := range eventsOfType(events, trace.TypeExec) {
		if e.Exec.Kind == trace.ExecKindScopeList && e.Exec.Subject == "" {
			t.Errorf("the scope listing at %d does not say which pattern it resolved", e.Seq)
		}
		if e.Exec.Kind == trace.ExecKindCovdataTextfmt && e.Exec.Subject == "" {
			t.Errorf("the profile rendering at %d does not say which package it was for", e.Seq)
		}
	}

	// One attempt per execution, joined to the report by id, and agreeing with
	// it about which binary caught the mutant.
	byID := make(map[string]report.Mutant, len(outcome.Report.Mutants))
	for _, m := range outcome.Report.Mutants {
		byID[m.ID] = m
	}
	attempts := eventsOfType(events, trace.TypeMutantExec)
	if len(attempts) == 0 {
		t.Fatal("no mutant execution was recorded")
	}
	perMutant := map[string]int{}
	for _, e := range attempts {
		record := e.Mutant
		mutant, known := byID[record.ID]
		if !known {
			t.Errorf("the attempt at %d names %s, which is not in the report", e.Seq, record.ID)
			continue
		}
		perMutant[record.ID]++
		if record.DisplayID != mutant.DisplayID {
			t.Errorf("the attempt at %s says display id %q, the report says %q",
				record.ID[:8], record.DisplayID, mutant.DisplayID)
		}
		if record.Package != mutant.Package {
			t.Errorf("the attempt at %s says package %q, the report says %q",
				record.ID[:8], record.Package, mutant.Package)
		}
		if record.Outcome == trace.OutcomeKilled {
			if mutant.KilledBy == nil || record.KilledBy != *mutant.KilledBy {
				t.Errorf("the attempt at %s was killed by %q, the report says %v",
					record.ID[:8], record.KilledBy, mutant.KilledBy)
			}
		}
	}
	for id, n := range perMutant {
		if byID[id].Attempts != n {
			t.Errorf("mutant %s recorded %d attempts, the report says %d", id[:8], n, byID[id].Attempts)
		}
	}

	// One coverage decision per mutant the mapping was asked about, which is
	// every mutant this run selected: the covered ones and the ones nothing
	// reaches together.
	pass, mappedAtAll := coverageMappedOf(published)
	if !mappedAtAll {
		t.Fatal("the run did no coverage pass, so there is no mapping to have recorded")
	}
	mapped := eventsOfType(events, trace.TypeCoverageMap)
	if got, want := len(mapped), pass.Covered+pass.Uncovered; got != want {
		t.Errorf("recorded %d coverage-map events, want one per selected mutant (%d)", got, want)
	}
	for _, e := range mapped {
		mutant, known := byID[e.Coverage.MutantID]
		if !known {
			t.Errorf("the coverage decision at %d names a mutant the report does not have", e.Seq)
			continue
		}
		if e.Coverage.Uncovered != mutant.Uncovered {
			t.Errorf("the coverage decision on %s says uncovered=%t, the report says %t",
				mutant.DisplayID, e.Coverage.Uncovered, mutant.Uncovered)
		}
		if !slices.Equal(e.Coverage.Covering, mutant.CoveringTestPackages) {
			t.Errorf("the coverage decision on %s names %v, the report names %v",
				mutant.DisplayID, e.Coverage.Covering, mutant.CoveringTestPackages)
		}
	}

	// The cache, which is the one event that explains an absence of work.
	opens, lookups, stores := 0, 0, 0
	for _, e := range eventsOfType(events, trace.TypeCache) {
		switch e.Cache.Op {
		case trace.CacheOpOpen:
			opens++
			if e.Cache.Directory == "" || e.Cache.ContextKey == "" {
				t.Errorf("the cache was opened without naming its directory or its context: %+v", *e.Cache)
			}
		case trace.CacheOpLookup:
			lookups++
		case trace.CacheOpStore:
			stores++
		}
	}
	if opens != 1 {
		t.Errorf("recorded %d cache opens, want one", opens)
	}
	if lookups == 0 || stores == 0 {
		t.Errorf("recorded %d lookups and %d stores, want the cold run's misses and write-backs",
			lookups, stores)
	}

	// The files the run wrote, each named once, each at the path the run
	// reports.
	artifacts := map[string]string{}
	for _, e := range eventsOfType(events, trace.TypeArtifact) {
		if e.Artifact.Path == "" {
			t.Errorf("the %s artifact at %d has no path", e.Artifact.Kind, e.Seq)
		}
		if e.Artifact.Kind == trace.ArtifactCoverageProfile {
			continue
		}
		artifacts[e.Artifact.Kind] = e.Artifact.Path
	}
	wantArtifacts := map[string]string{
		trace.ArtifactReportRun:    outcome.RunPath,
		trace.ArtifactReportLatest: outcome.LatestPath,
		trace.ArtifactReportJSON:   outcome.Artifacts.ProjectionPath,
		trace.ArtifactReportHTML:   outcome.Artifacts.HTMLPath,
	}
	for kind, path := range wantArtifacts {
		if artifacts[kind] != path {
			t.Errorf("the %s artifact is recorded at %q, want %q", kind, artifacts[kind], path)
		}
	}

	// The last line, and the accounting that tells a complete recording from a
	// lossy one.
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd || last.Run == nil {
		t.Fatalf("the last event is %s, want %s", last.Type, trace.TypeRunEnd)
	}
	if last.Run.Verdict != string(StatusOK) || last.Run.ExitCode != 0 {
		t.Errorf("run-end = %+v, want verdict %q and exit code 0", *last.Run, StatusOK)
	}
	if last.Run.EventsDropped != 0 {
		t.Errorf("run-end reports %d dropped events, want none from an unbounded sink", last.Run.EventsDropped)
	}
	if got, want := last.Run.EventsEmitted, int64(len(events)-1); got != want {
		t.Errorf("run-end reports %d emitted events, want %d — every line but its own", got, want)
	}
	// Nothing is recorded after it, and the sequence is the order of the file.
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d carries seq %d: the recording is not in sequence order", i, e.Seq)
		}
	}
}

// failingSink is a sink that refuses everything, which is the one failure a
// diagnostic has to survive without costing the run.
type failingSink struct{ emits int }

func (s *failingSink) Emit(trace.Event) error { s.emits++; return errors.New("no room on the disk") }
func (s *failingSink) Close() error           { return nil }

// TestATraceThatCannotBeWrittenChangesNothingAboutTheRun is the invariant a
// diagnostic lives or dies by.
//
// A trace is not evidence. A sink that refuses every event — a full disk, a
// read-only directory, a broken pipe — costs the events and nothing else: the
// same mutants, the same verdicts, the same exit status, the same document. A
// recorder that could change a verdict would make the tool less trustworthy with
// the diagnostic than without it, which inverts the point of having one.
func TestATraceThatCannotBeWrittenChangesNothingAboutTheRun(t *testing.T) {
	t.Parallel()

	quiet, _, quietErr := collect(t, t.Context(), untraced(options(t, "killable")))
	sink := &failingSink{}
	loud := options(t, "killable")
	loud.TraceSink = sink
	traced, _, tracedErr := collect(t, t.Context(), loud)

	if (quietErr == nil) != (tracedErr == nil) {
		t.Fatalf("the untraced run ended %v and the traced one %v", quietErr, tracedErr)
	}
	if quiet.Status != traced.Status {
		t.Errorf("status = %s traced, %s untraced", traced.Status, quiet.Status)
	}
	if quiet.Verdict.Code != traced.Verdict.Code {
		t.Errorf("exit code = %s traced, %s untraced", traced.Verdict.Code, quiet.Verdict.Code)
	}
	if sink.emits == 0 {
		t.Fatal("the failing sink was never asked to write anything, so nothing was proven")
	}
	if len(traced.Warnings) != len(quiet.Warnings) {
		t.Errorf("the traced run published %v, the untraced one %v", traced.Warnings, quiet.Warnings)
	}

	// The published documents, normalised for the facts that are about the run
	// rather than about the code: the run id, the wall clock, the durations and
	// the paths. Everything else — every mutant id, every outcome, the score,
	// the digests — has to be identical byte for byte.
	want := mutantkit.NormalizeRunReport(t, mustMarshalReport(t, quiet.Report))
	got := mutantkit.NormalizeRunReport(t, mustMarshalReport(t, traced.Report))
	if string(got) != string(want) {
		t.Errorf("the traced run filed a different report:\n%s\nwant\n%s", got, want)
	}
}

// TestTraceOptionsTakeNoPartInMutantIdsOrTheCacheKey is the other half of the
// same invariant, and the one that would fail silently.
//
// A trace option that reached [cache.Context] would give every traced run a
// context of its own and an empty cache directory: correct results, no cache,
// and no message anywhere saying why the run that was supposed to be fast was
// not. Two runs over one workspace and one cache — the first untraced, the
// second traced — settle it: if the second finds every outcome the first stored,
// the key did not move.
func TestTraceOptionsTakeNoPartInMutantIdsOrTheCacheKey(t *testing.T) {
	t.Parallel()

	root := testkit.Copy(t, "killable")
	cacheRoot := t.TempDir()

	cold := runCached(t, cacheOptions(t, root, cacheRoot))
	stored := reusableRows(cold)
	if len(stored) == 0 {
		t.Fatal("the first run stored nothing, so the second has nothing to find")
	}

	warmOpts := cacheOptions(t, root, cacheRoot)
	sink := trace.NewMemorySink(0)
	warmOpts.TraceSink = sink
	warmOpts.PublishTrace = true
	warm := runCached(t, warmOpts)

	if got := cachedRows(warm); len(got) != len(stored) {
		t.Errorf("the traced run adopted %d outcomes, want the %d the untraced run stored",
			len(got), len(stored))
	}
	for id, outcome := range stored {
		mutant, found := mutantByID(warm, id)
		switch {
		case !found:
			t.Errorf("mutant %s is not in the traced run's report at all: the ids moved", id[:8])
		case !mutant.Cached:
			t.Errorf("mutant %s was measured again by the traced run: the cache key moved", id[:8])
		case mutant.Outcome != outcome:
			t.Errorf("mutant %s came back %q, want the stored %q", id[:8], mutant.Outcome, outcome)
		}
	}
	// And the recording of the warm run says the same thing from the other
	// side, which is what makes a hit legible at all.
	hits := 0
	for _, e := range eventsOfType(sink.Events(), trace.TypeCache) {
		if e.Cache.Op == trace.CacheOpLookup && e.Cache.Result == trace.CacheResultHit {
			hits++
		}
	}
	if hits != len(stored) {
		t.Errorf("the recording holds %d cache hits, want %d", hits, len(stored))
	}
}

// mutantByID finds one mutant in a report.
func mutantByID(r *report.Report, id string) (report.Mutant, bool) {
	for _, m := range r.Mutants {
		if m.ID == id {
			return m, true
		}
	}
	return report.Mutant{}, false
}

// TestPublishTraceForwardsEveryTraceEventAsTracedInSequenceOrder is how `-vv`
// gets its lines without a second recording.
//
// The verbose renderer draws the trace, and the trace already travels through
// one channel: the engine's own event stream. Publishing each recorded event as
// it is recorded means the two orders are one order — a renderer printing them
// in the order they arrive is printing the recording — and it means a run that
// is not being asked for them pays nothing, because the tee is only built when
// somebody asked.
func TestPublishTraceForwardsEveryTraceEventAsTracedInSequenceOrder(t *testing.T) {
	t.Parallel()

	opts, sink := tracedOptions(t, "simple")
	opts.PublishTrace = true
	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	var published []trace.Event
	for _, e := range events {
		if traced, ok := e.(Traced); ok {
			published = append(published, traced.Event)
		}
	}
	recorded := sink.Events()
	if len(published) != len(recorded) {
		t.Fatalf("published %d Traced events, want the %d the sink kept", len(published), len(recorded))
	}
	for i := range recorded {
		if published[i].Seq != recorded[i].Seq || published[i].Type != recorded[i].Type {
			t.Fatalf("Traced[%d] = %d/%s, want %d/%s",
				i, published[i].Seq, published[i].Type, recorded[i].Seq, recorded[i].Type)
		}
		if published[i].Seq != int64(i+1) {
			t.Fatalf("Traced[%d] carries seq %d: the stream is not in sequence order", i, published[i].Seq)
		}
	}
	// The last recorded event is the run-end, and it is published before the
	// terminal engine event: a renderer that stopped at RunCompleted would
	// otherwise lose the accounting that says whether the recording is whole.
	names := eventNames(events)
	if last := names[len(names)-1]; last != "RunCompleted" {
		t.Fatalf("the last engine event is %s, want RunCompleted", last)
	}
	if got := published[len(published)-1].Type; got != trace.TypeRunEnd {
		t.Errorf("the last published event is %s, want %s", got, trace.TypeRunEnd)
	}

	// A run nobody asked to publish keeps the stream it always had.
	silentOpts, _ := tracedOptions(t, "simple")
	_, silent, err := collect(t, t.Context(), silentOpts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, e := range silent {
		if _, ok := e.(Traced); ok {
			t.Fatalf("a run with PublishTrace off published %s", fmt.Sprintf("%T", e))
		}
	}
	// Which is also the only difference between the two streams.
	if got, want := stripTraced(eventNames(events)), eventNames(silent); !slices.Equal(got, want) {
		t.Errorf("publishing the trace changed the event stream:\n%v\nwant\n%v", got, want)
	}
}

// stripTraced removes the published trace from a list of event names, so that
// two runs' streams can be compared for everything else.
func stripTraced(names []string) []string {
	kept := make([]string, 0, len(names))
	for _, name := range names {
		if !strings.HasPrefix(name, "Traced") {
			kept = append(kept, name)
		}
	}
	return kept
}
