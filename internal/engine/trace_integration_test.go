// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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

func tracedOptions(t *testing.T, name string) (Options, *trace.MemorySink) {
	t.Helper()
	sink := trace.NewMemorySink(0)
	opts := options(t, name)
	opts.Config.Test.BaselineRuns = 3
	opts.TraceSink = sink
	return opts, sink
}

type stageKey struct{ phase, name string }

func (k stageKey) String() string { return k.phase + "/" + k.name }

func execKinds(events []trace.Event) map[string]int {
	kinds := make(map[string]int)
	for _, e := range events {
		if e.Type == trace.TypeExec {
			kinds[e.Exec.Kind]++
		}
	}
	return kinds
}

func eventsOfType(events []trace.Event, kind string) []trace.Event {
	var out []trace.Event
	for _, e := range events {
		if e.Type == kind {
			out = append(out, e)
		}
	}
	return out
}

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
	if len(outcome.Timing.Phases) != len(wantPhases) {
		t.Errorf("Timing.Phases = %+v, want one span per phase", outcome.Timing.Phases)
	}

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
	if got := finished[stageKey{trace.PhaseBaseline, "test"}]; got != opts.Config.Test.BaselineRuns {
		t.Errorf("the baseline recorded %d test stages, want one per configured run (%d)",
			got, opts.Config.Test.BaselineRuns)
	}

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
	for _, e := range eventsOfType(events, trace.TypeExec) {
		if e.Exec.Kind == trace.ExecKindScopeList && e.Exec.Subject == "" {
			t.Errorf("the scope listing at %d does not say which pattern it resolved", e.Seq)
		}
		if e.Exec.Kind == trace.ExecKindCoverageRun && e.Exec.Subject == "" {
			t.Errorf("the coverage run at %d does not say which package it was for", e.Seq)
		}
		if e.Exec.Kind == trace.ExecKindCovdataTextfmt {
			t.Errorf("the run started `go tool covdata textfmt` at %d, which a written profile does not need", e.Seq)
		}
	}

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
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d carries seq %d: the recording is not in sequence order", i, e.Seq)
		}
	}
}

type failingSink struct{ emits int }

func (s *failingSink) Emit(trace.Event) error { s.emits++; return errors.New("no room on the disk") }
func (s *failingSink) Close() error           { return nil }

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

	want := mutantkit.NormalizeRunReport(t, mustMarshalReport(t, quiet.Report))
	got := mutantkit.NormalizeRunReport(t, mustMarshalReport(t, traced.Report))
	if string(got) != string(want) {
		t.Errorf("the traced run filed a different report:\n%s\nwant\n%s", got, want)
	}
}

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

func mutantByID(r *report.Report, id string) (report.Mutant, bool) {
	for _, m := range r.Mutants {
		if m.ID == id {
			return m, true
		}
	}
	return report.Mutant{}, false
}

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
	names := eventNames(events)
	if last := names[len(names)-1]; last != "RunCompleted" {
		t.Fatalf("the last engine event is %s, want RunCompleted", last)
	}
	if got := published[len(published)-1].Type; got != trace.TypeRunEnd {
		t.Errorf("the last published event is %s, want %s", got, trace.TypeRunEnd)
	}

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
	if got, want := stripTraced(eventNames(events)), eventNames(silent); !slices.Equal(got, want) {
		t.Errorf("publishing the trace changed the event stream:\n%v\nwant\n%v", got, want)
	}
}

func stripTraced(names []string) []string {
	kept := make([]string, 0, len(names))
	for _, name := range names {
		if !strings.HasPrefix(name, "Traced") {
			kept = append(kept, name)
		}
	}
	return kept
}
