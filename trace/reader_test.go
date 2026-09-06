// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/trace"
)

// writeStream writes the given lines as a recording and returns its path.
func writeStream(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), trace.FileName)
	contents := ""
	for _, line := range lines {
		contents += line + "\n"
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// scriptedLines is the scripted recording as the lines of a stream.
func scriptedLines(t *testing.T) []string {
	t.Helper()
	lines := make([]string, 0, fixtureScriptedCount)
	for _, event := range scriptedEvents(t) {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(encoded))
	}
	return lines
}

// lineOfType returns the first line of a recording carrying the given type.
// Looking a line up by what it is rather than by where it sits keeps the
// rejection table from breaking every time the scripted recording grows.
func lineOfType(t *testing.T, lines []string, eventType string) string {
	t.Helper()
	for _, line := range lines {
		if strings.Contains(line, `"type":"`+eventType+`"`) {
			return line
		}
	}
	t.Fatalf("the scripted recording holds no %s event", eventType)
	return ""
}

func TestReadReturnsTheRecordingItWasGiven(t *testing.T) {
	t.Parallel()

	path := writeStream(t, scriptedLines(t)...)
	events, err := trace.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != fixtureScriptedCount {
		t.Fatalf("Read returned %d events, want %d", len(events), fixtureScriptedCount)
	}
	if !slices.Equal(seqsOf(events), seqsOf(scriptedEvents(t))) {
		t.Error("Read returned the events out of order")
	}
	// A directory names its stream, which is what lets a caller pass the run
	// directory a sink reported rather than reconstruct the file name.
	fromDirectory, err := trace.Read(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Read of a directory: %v", err)
	}
	if len(fromDirectory) != fixtureScriptedCount {
		t.Errorf("Read of a directory returned %d events", len(fromDirectory))
	}
}

func TestReadRejectsUnknownFieldsTrailingDataAndTwoPayloads(t *testing.T) {
	t.Parallel()

	valid := scriptedLines(t)
	cases := map[string]string{
		// The three rules the docs state and the schema cannot: the schema
		// validates one line at a time, so a rule about where a line sits in
		// the stream is the reader's to enforce.
		// Both lines below have increasing sequence numbers, so the only thing
		// wrong with either is where it sits.
		"a run-start after the first line": `{"seq":1,"type":"note","timestamp":"2026-09-06T12:00:00Z","elapsed_ms":0,"note":{"kind":"warning"}}` + "\n" +
			`{"seq":2,"type":"run-start","schema":"gomutants-trace-v1","timestamp":"2026-09-06T12:00:01Z","elapsed_ms":1000,"start":{"kind":"run","tool_version":"0.1.0-dev","pid":1,"root":"/x"}}`,
		"an event after run-end": `{"seq":1,"type":"run-end","timestamp":"2026-09-06T12:00:00Z","elapsed_ms":0,"run":{"events_emitted":0,"events_dropped":0}}` + "\n" +
			`{"seq":2,"type":"note","timestamp":"2026-09-06T12:00:01Z","elapsed_ms":1000,"note":{"kind":"warning"}}`,
		"infected on an unmeasured probe": `{"seq":1,"type":"probe-exec","timestamp":"2026-09-06T12:00:00Z","elapsed_ms":0,` +
			`"probe":{"outcome":"timed-out","exit_code":-1,"infected":["` + fixtureMutantID + `"]}}`,
		"an unknown envelope field": `{"seq":1,"type":"note","timestamp":"2026-09-06T12:00:00Z","elapsed_ms":0,"note":{"kind":"warning"},"extra":true}`,
		"an unknown payload field":  `{"seq":1,"type":"note","timestamp":"2026-09-06T12:00:00Z","elapsed_ms":0,"note":{"kind":"warning","extra":true}}`,
		"trailing data":             lineOfType(t, valid, trace.TypeNote) + ` {"seq":99}`,
		"two payloads":              `{"seq":1,"type":"note","timestamp":"2026-09-06T12:00:00Z","elapsed_ms":0,"note":{"kind":"warning"},"phase":{"name":"mutate"}}`,
		"no payload at all":         `{"seq":1,"type":"note","timestamp":"2026-09-06T12:00:00Z","elapsed_ms":0}`,
		"a foreign payload":         `{"seq":1,"type":"note","timestamp":"2026-09-06T12:00:00Z","elapsed_ms":0,"phase":{"name":"mutate"}}`,
		"an unknown type":           `{"seq":1,"type":"invented","timestamp":"2026-09-06T12:00:00Z","elapsed_ms":0}`,
		"an empty envelope":         `{"seq":0,"type":"","timestamp":"","elapsed_ms":0}`,
		"a schema off run-start":    `{"seq":1,"type":"note","schema":"gomutants-trace-v1","timestamp":"2026-09-06T12:00:00Z","elapsed_ms":0,"note":{"kind":"warning"}}`,
		"a run-start without one":   `{"seq":1,"type":"run-start","timestamp":"2026-09-06T12:00:00Z","elapsed_ms":0,"start":{"kind":"run"}}`,
		"a sequence that repeats":   valid[0] + "\n" + valid[0],
		"a second run-end":          valid[len(valid)-1] + "\n" + valid[len(valid)-1],
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := writeStream(t, line)
			if events, err := trace.Read(path); err == nil {
				t.Fatalf("Read accepted %s and returned %d events", name, len(events))
			}
			if _, err := trace.ReadSummary(path); err == nil {
				t.Fatalf("ReadSummary accepted %s", name)
			}
		})
	}
}

func TestReadSummaryReportsMissingIncompleteAndLossyRecordings(t *testing.T) {
	t.Parallel()

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), trace.FileName)
		summary, err := trace.ReadSummary(path)
		if err != nil {
			t.Fatalf("ReadSummary: %v", err)
		}
		if !summary.Missing {
			t.Error("a recording that does not exist is not reported as missing")
		}
		if summary.Events != 0 || summary.HasRunEnd {
			t.Errorf("a missing recording holds %d events, run-end %v", summary.Events, summary.HasRunEnd)
		}
		// The maps are never nil, so a caller may index them without a branch.
		if summary.Counts == nil || summary.ExecByKind == nil || summary.StageDurationMS == nil {
			t.Error("a missing recording returned nil maps")
		}
	})

	t.Run("incomplete", func(t *testing.T) {
		t.Parallel()
		lines := scriptedLines(t)
		summary, err := trace.ReadSummary(writeStream(t, lines[:len(lines)-1]...))
		if err != nil {
			t.Fatalf("ReadSummary: %v", err)
		}
		if summary.Missing {
			t.Error("a readable prefix is reported as missing")
		}
		if summary.HasRunEnd {
			t.Error("a recording without its last line claims to have a run-end")
		}
		if summary.Events != fixtureScriptedCount-1 {
			t.Errorf("Events = %d, want %d", summary.Events, fixtureScriptedCount-1)
		}
		if summary.Verdict != "" {
			t.Errorf("Verdict = %q for a recording that never closed", summary.Verdict)
		}
	})

	t.Run("lossy by accounting", func(t *testing.T) {
		t.Parallel()
		sink := trace.NewMemorySink(3)
		recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
		for range 4 {
			recorder.Artifact(trace.ArtifactTrace, fixtureArtifactPath)
		}
		recorder.RunEnd(fixtureVerdict, 0, nil)
		lines := make([]string, 0, 3)
		for _, event := range sink.Events() {
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, string(encoded))
		}
		summary, err := trace.ReadSummary(writeStream(t, lines...))
		if err != nil {
			t.Fatalf("ReadSummary: %v", err)
		}
		if !summary.HasRunEnd {
			t.Fatal("the ring lost its run-end")
		}
		if summary.EventsDropped != 3 {
			t.Errorf("EventsDropped = %d, want 3", summary.EventsDropped)
		}
		// The ring dropped the first events, so the recording begins partway
		// through and the gap is counted rather than glossed over.
		if summary.MissingSequences != 3 {
			t.Errorf("MissingSequences = %d, want 3", summary.MissingSequences)
		}
		if summary.FirstSequence != 4 {
			t.Errorf("FirstSequence = %d, want 4", summary.FirstSequence)
		}
	})

	t.Run("lossy by gap", func(t *testing.T) {
		t.Parallel()
		lines := scriptedLines(t)
		without := slices.Concat(lines[:5], lines[7:])
		summary, err := trace.ReadSummary(writeStream(t, without...))
		if err != nil {
			t.Fatalf("ReadSummary: %v", err)
		}
		if summary.MissingSequences != 2 {
			t.Errorf("MissingSequences = %d, want 2", summary.MissingSequences)
		}
		if summary.EventsDropped != 0 {
			t.Errorf("EventsDropped = %d: the run reported no loss, the file has one", summary.EventsDropped)
		}
	})
}

func TestReadSummaryTalliesExecByKindAndStageDurations(t *testing.T) {
	t.Parallel()

	summary, err := trace.ReadSummary(writeStream(t, scriptedLines(t)...))
	if err != nil {
		t.Fatalf("ReadSummary: %v", err)
	}
	if summary.Events != fixtureScriptedCount || !summary.HasRunEnd {
		t.Fatalf("Events = %d, run-end %v", summary.Events, summary.HasRunEnd)
	}
	if summary.Verdict != fixtureVerdict || summary.ExitCode != 0 {
		t.Errorf("Verdict = %q, exit %d", summary.Verdict, summary.ExitCode)
	}
	if len(summary.Counts) != fixtureTypeCount {
		t.Errorf("Counts holds %d types, want %d: %v", len(summary.Counts), fixtureTypeCount, summary.Counts)
	}
	if summary.Counts[trace.TypeStage] != 2 {
		t.Errorf("Counts[stage] = %d, want 2", summary.Counts[trace.TypeStage])
	}
	tally, ok := summary.ExecByKind[trace.ExecKindMutantRun]
	if !ok {
		t.Fatalf("ExecByKind has no %q: %v", trace.ExecKindMutantRun, summary.ExecByKind)
	}
	if tally.Count != 1 {
		t.Errorf("ExecByKind[%q].Count = %d, want 1", trace.ExecKindMutantRun, tally.Count)
	}
	// One command of each labelled kind the scripted recording runs, which is
	// the tally a performance question is asked of.
	if len(summary.ExecByKind) != 3 {
		t.Errorf("ExecByKind holds %d kinds, want 3: %v", len(summary.ExecByKind), summary.ExecByKind)
	}
	if got := summary.ExecByKind[trace.ExecKindValidateBuild]; got.DurationMS == 0 {
		t.Errorf("ExecByKind[%q] = %+v, want its duration summed", trace.ExecKindValidateBuild, got)
	}
	// A stage is keyed by its phase and its name together, because the same
	// stage name appears in more than one phase and two spans that share a key
	// would be added together.
	if got, want := summary.StageDurationMS[trace.PhaseMutate+"/"+fixtureStageName], fixtureTick.Milliseconds(); got != want {
		t.Errorf("StageDurationMS[%s/%s] = %d, want %d", trace.PhaseMutate, fixtureStageName, got, want)
	}
	if len(summary.StageDurationMS) != 1 {
		t.Errorf("StageDurationMS holds %d stages, want 1: %v", len(summary.StageDurationMS), summary.StageDurationMS)
	}
	if summary.PhaseDurationMS[trace.PhaseMutate] == 0 {
		t.Errorf("PhaseDurationMS holds %v", summary.PhaseDurationMS)
	}
	if summary.PrepareDurationMS[trace.PreparePhaseDiscovery] != fixturePrepareTime.Milliseconds() {
		t.Errorf("PrepareDurationMS = %v", summary.PrepareDurationMS)
	}
	if summary.MutantOutcomes[trace.OutcomeKilled] != 1 {
		t.Errorf("MutantOutcomes = %v", summary.MutantOutcomes)
	}
}

func TestDiffReportsDeltasPerTypePhaseAndStage(t *testing.T) {
	t.Parallel()

	before, err := trace.ReadSummary(writeStream(t, scriptedLines(t)...))
	if err != nil {
		t.Fatal(err)
	}

	// A second recording of the same script plus one more mutant execution and
	// one more command, closed with a different verdict.
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	endPhase := recorder.PhaseStart(trace.PhaseMutate)
	recorder.Stage(fixtureStageName, fixtureStageDetail)(trace.ResultSucceeded)
	// The same number of commands as the recording before it, one of which
	// changed kind: a per-type count cannot see that, and ExecByKind can.
	moved := fixtureExecRecord()
	moved.Kind = trace.ExecKindGoTestC
	recorder.Exec(moved)
	recorder.Exec(fixtureProbeExecRecord())
	recorder.Exec(fixtureValidateExecRecord())
	survived := fixtureMutantRecord()
	survived.Outcome = trace.OutcomeSurvived
	survived.KilledBy = ""
	recorder.MutantExec(survived)
	endPhase()
	recorder.RunEnd("survivors", 1, nil)
	lines := make([]string, 0, len(sink.Events()))
	for _, event := range sink.Events() {
		encoded, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		lines = append(lines, string(encoded))
	}
	after, err := trace.ReadSummary(writeStream(t, lines...))
	if err != nil {
		t.Fatal(err)
	}

	diff := trace.Diff(before, after)
	if diff.EventsDelta != after.Events-before.Events {
		t.Errorf("EventsDelta = %d, want %d", diff.EventsDelta, after.Events-before.Events)
	}
	if diff.BeforeVerdict != fixtureVerdict || diff.AfterVerdict != "survivors" {
		t.Errorf("verdicts = %q -> %q", diff.BeforeVerdict, diff.AfterVerdict)
	}
	if !diff.BeforeRunEnd || !diff.AfterRunEnd {
		t.Error("both recordings closed, and the diff says otherwise")
	}
	if diff.CountDelta[trace.TypeNote] != -1 {
		t.Errorf("CountDelta[note] = %d, want -1", diff.CountDelta[trace.TypeNote])
	}
	if diff.CountDelta[trace.TypeExec] != 0 {
		t.Errorf("CountDelta[exec] = %d, want 0", diff.CountDelta[trace.TypeExec])
	}
	// One command moved from one kind to another, which a per-type count could
	// never show.
	if got := diff.ExecDelta[trace.ExecKindMutantRun]; got.Count != -1 {
		t.Errorf("ExecDelta[mutant-run].Count = %d, want -1", got.Count)
	}
	if got := diff.ExecDelta[trace.ExecKindGoTestC]; got.Count != 1 {
		t.Errorf("ExecDelta[go-test-c].Count = %d, want 1", got.Count)
	}
	if diff.MutantOutcomeDelta[trace.OutcomeKilled] != -1 || diff.MutantOutcomeDelta[trace.OutcomeSurvived] != 1 {
		t.Errorf("MutantOutcomeDelta = %v", diff.MutantOutcomeDelta)
	}
	stage := trace.PhaseMutate + "/" + fixtureStageName
	if _, ok := diff.StageDurationDeltaMS[stage]; !ok {
		t.Errorf("StageDurationDeltaMS has no %q: %v", stage, diff.StageDurationDeltaMS)
	}
	if _, ok := diff.PhaseDurationDeltaMS[trace.PhaseMutate]; !ok {
		t.Errorf("PhaseDurationDeltaMS has no %q: %v", trace.PhaseMutate, diff.PhaseDurationDeltaMS)
	}
	if diff.PrepareDurationDeltaMS[trace.PreparePhaseDiscovery] != -fixturePrepareTime.Milliseconds() {
		t.Errorf("PrepareDurationDeltaMS = %v", diff.PrepareDurationDeltaMS)
	}
	if diff.EventsDroppedDelta != 0 || diff.MissingSequencesDelta != 0 {
		t.Errorf("neither recording lost anything: %+v", diff)
	}
}

func TestReadRefusesALineTooLongToBeAnEvent(t *testing.T) {
	t.Parallel()

	// A stream is read with a bounded buffer, because a diagnostic reader must
	// not be a way to exhaust memory on a file somebody else wrote.
	path := writeStream(t, `{"seq":1,"type":"note","timestamp":"2026-09-06T12:00:00Z","elapsed_ms":0,"note":{"kind":"warning","detail":"`+
		strings.Repeat("x", 17<<20)+`"}}`)
	if _, err := trace.Read(path); err == nil {
		t.Fatal("Read accepted a line beyond the maximum")
	}
}
