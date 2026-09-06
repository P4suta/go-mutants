// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

// The committed recordings every scripted event must marshal to, byte for byte:
// the first for a run that went well, the second for one that went wrong.
//
// They are compared and regenerated through [testkit.Golden], which owns this
// repository's one `-update` flag — `mise run golden-update`. This package used
// to register a flag of its own, as internal/report and internal/instrument did;
// three of them in one binary is a "flag redefined" panic before any test runs.
// The files are the wire contract, so a change to either is a change other tools
// see: read the diff before committing it.
const (
	goldenRecording        = "events.golden.jsonl"
	goldenFailureRecording = "events-failure.golden.jsonl"
)

func TestNewEmitsRunStartWithTheSchemaAndStartRecord(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	start := fixtureStartRecord()
	if trace.New(sink, fixtureClock(), start) == nil {
		t.Fatal("New returned no recorder for a usable sink")
	}
	events := sink.Events()
	if len(events) != 1 {
		t.Fatalf("New recorded %d events, want 1", len(events))
	}
	first := events[0]
	if first.Seq != 1 || first.Type != trace.TypeRunStart {
		t.Fatalf("first event is %d/%q, want 1/%q", first.Seq, first.Type, trace.TypeRunStart)
	}
	if first.Schema != trace.SchemaV1 {
		t.Errorf("run-start schema = %q, want %q", first.Schema, trace.SchemaV1)
	}
	if first.Timestamp != fixtureStart.Format(time.RFC3339Nano) {
		t.Errorf("run-start timestamp = %q, want %q", first.Timestamp, fixtureStart.Format(time.RFC3339Nano))
	}
	if first.ElapsedMS != 0 {
		t.Errorf("run-start elapsed_ms = %d, want 0", first.ElapsedMS)
	}
	if first.Start == nil {
		t.Fatal("run-start carries no start payload")
	}
	got := *first.Start
	if got.Kind != start.Kind || got.RunID != start.RunID || got.ToolVersion != start.ToolVersion {
		t.Errorf("run-start identity = %q/%q/%q, want %q/%q/%q",
			got.Kind, got.RunID, got.ToolVersion, start.Kind, start.RunID, start.ToolVersion)
	}
	if got.PID != start.PID || got.Root != start.Root {
		t.Errorf("run-start process = %d in %q, want %d in %q", got.PID, got.Root, start.PID, start.Root)
	}
	if !slices.Equal(got.Args, start.Args) {
		t.Errorf("run-start args = %v, want %v", got.Args, start.Args)
	}
}

func TestNilRecorderIsAnInertNoOp(t *testing.T) {
	t.Parallel()

	if recorder := trace.New(nil, fixtureClock(), fixtureStartRecord()); recorder != nil {
		t.Fatalf("New(nil) returned %v, want a nil recorder", recorder)
	}
	var recorder *trace.Recorder
	// Every method on the disabled recorder, called for the one reason the nil
	// recorder exists: a call site records unconditionally, so a nil receiver
	// that panicked would move the branch back into every caller.
	recorder.PhaseStart(trace.PhaseMutate)()
	recorder.Stage(fixtureStageName, fixtureStageDetail)(trace.ResultSucceeded)
	recorder.Prepare(trace.PreparePhaseDiscovery, trace.StateStarted, "", 0)
	if seq := recorder.Exec(fixtureExecRecord()); seq != 0 {
		t.Errorf("nil Exec returned seq %d, want 0", seq)
	}
	recorder.MutantExec(fixtureMutantRecord())
	recorder.ProbeExec(fixtureProbeRecord())
	recorder.Validate(fixtureValidateRecord())
	recorder.Coverage(fixtureCoverageRecord())
	recorder.Cache(fixtureCacheRecord())
	recorder.Snapshot(fixtureSnapshotRecord())
	recorder.Sweep(fixtureSweepRecord())
	recorder.Artifact(trace.ArtifactTrace, fixtureArtifactPath)
	recorder.Note(trace.NoteTraceUnavailable, "", fixtureNoteDetail)
	recorder.RunEnd(fixtureVerdict, 0, errors.New("ignored"))
}

func TestEveryEventTypeIsRecordedOnceInSequenceOrder(t *testing.T) {
	t.Parallel()

	events := scriptedEvents(t)
	if len(events) != fixtureScriptedCount {
		t.Fatalf("the scripted recording holds %d events, want %d", len(events), fixtureScriptedCount)
	}
	kinds := map[string]int{}
	for i, event := range events {
		if event.Seq != int64(i+1) {
			t.Errorf("event %d carries seq %d", i+1, event.Seq)
		}
		kinds[event.Type]++
		if got := payloadsOf(event); got != 1 {
			t.Errorf("%s event carries %d payloads, want 1", event.Type, got)
		}
	}
	if len(kinds) != fixtureTypeCount {
		t.Errorf("the scripted recording holds %d types, want %d: %v", len(kinds), fixtureTypeCount, kinds)
	}
	if events[0].Type != trace.TypeRunStart {
		t.Errorf("first event is %q, want %q", events[0].Type, trace.TypeRunStart)
	}
	if last := events[len(events)-1]; last.Type != trace.TypeRunEnd {
		t.Errorf("last event is %q, want %q", last.Type, trace.TypeRunEnd)
	}
}

// payloadsOf counts how many payloads an event carries, which the contract says
// is exactly one for every type.
func payloadsOf(event trace.Event) int {
	present := []bool{
		event.Start != nil, event.Phase != nil, event.Stage != nil, event.Prepare != nil,
		event.Exec != nil, event.Mutant != nil, event.Probe != nil, event.Validate != nil,
		event.Coverage != nil, event.Cache != nil, event.Snapshot != nil, event.Sweep != nil,
		event.Artifact != nil, event.Note != nil, event.Run != nil,
	}
	count := 0
	for _, carried := range present {
		if carried {
			count++
		}
	}
	return count
}

// TestRecordedEventsPinTheirJSONFieldNamesAndOrder is the field-name contract.
//
// A byte-exact golden is the right assertion rather than a field-by-field
// comparison: the names, their order, the difference between an absent key and
// a zero-valued one, and the spelling of every enumerated value are what a
// consumer's decoder sees, and none of them would be caught by asserting that
// the values are equal. The names are also how a go-mutants recording joins a
// goatest one, so a rename here is a rename in somebody else's pipeline.
func TestRecordedEventsPinTheirJSONFieldNamesAndOrder(t *testing.T) {
	t.Parallel()

	var lines []string
	for _, event := range scriptedEvents(t) {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshalling the %s event: %v", event.Type, err)
		}
		lines = append(lines, string(encoded))
	}
	got := strings.Join(lines, "\n") + "\n"
	testkit.Golden(t, goldenRecording, []byte(got))
}

func TestExecReducesEnvironmentToSortedDeduplicatedNamesAndNeverValues(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	record := fixtureExecRecord()
	record.EnvNames = []string{"PATH=/usr/bin", "GOFLAGS=-mod=mod", "PATH=/bin", "", "TMPDIR=/tmp/secret"}
	recorder.Exec(record)

	names := sink.Events()[1].Exec.EnvNames
	want := []string{"GOFLAGS", "PATH", "TMPDIR"}
	if !slices.Equal(names, want) {
		t.Fatalf("env_names = %v, want %v", names, want)
	}
	for _, name := range names {
		if strings.Contains(name, "=") {
			t.Errorf("env_names carries a value in %q", name)
		}
	}
	encoded, err := json.Marshal(sink.Events()[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"/usr/bin", "-mod=mod", "/tmp/secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("the recorded event leaked the environment value %q: %s", secret, encoded)
		}
	}
}

func TestExecDigestsOutputWithoutSerialisingIt(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	output := []byte("PASS\nok  \tgithub.com/P4suta/go-mutants/internal/mutation\t0.412s\n")
	record := fixtureExecRecord()
	record.Output = output
	recorder.Exec(record)

	recorded := sink.Events()[1]
	digest := sha256.Sum256(output)
	if recorded.Exec.OutputBytes != len(output) {
		t.Errorf("output_bytes = %d, want %d", recorded.Exec.OutputBytes, len(output))
	}
	if want := hex.EncodeToString(digest[:]); recorded.Exec.OutputSHA256 != want {
		t.Errorf("output_sha256 = %q, want %q", recorded.Exec.OutputSHA256, want)
	}
	if !slices.Equal(recorded.Exec.Output, output) {
		t.Error("the recorded event dropped the output bytes a sink needs to preserve them")
	}
	encoded, err := json.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PASS") {
		t.Errorf("the output bytes were serialised into the event: %s", encoded)
	}
	if strings.Contains(string(encoded), `"Output"`) || strings.Contains(string(encoded), `"output":`) {
		t.Errorf("the event carries an output field: %s", encoded)
	}
}

func TestExecReturnsTheSequenceNumberItRecorded(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	for want := int64(2); want <= 4; want++ {
		if got := recorder.Exec(fixtureExecRecord()); got != want {
			t.Fatalf("Exec returned %d, want %d", got, want)
		}
	}
	events := sink.Events()
	if int64(len(events)) != 4 {
		t.Fatalf("the recording holds %d events, want 4", len(events))
	}
	for _, event := range events[1:] {
		if event.Type != trace.TypeExec {
			t.Fatalf("event %d is %q", event.Seq, event.Type)
		}
	}
	// Nothing is recorded after run-end, so nothing gets a sequence number
	// either — a caller that stored a zero has a seq that matches no event.
	recorder.RunEnd(fixtureVerdict, 0, nil)
	if got := recorder.Exec(fixtureExecRecord()); got != 0 {
		t.Errorf("Exec after RunEnd returned %d, want 0", got)
	}
}

func TestStageIsStampedWithTheOpenPhase(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())

	recorder.Stage("toolchain", "")(trace.ResultSucceeded)
	endPhase := recorder.PhaseStart(trace.PhaseDiscover)
	recorder.Stage("snapshot", "")(trace.ResultSucceeded)
	endPhase()
	recorder.Stage("orphan", "")(trace.ResultSucceeded)

	var stamped []string
	for _, event := range sink.Events() {
		if event.Type == trace.TypeStage {
			stamped = append(stamped, event.Stage.Name+"/"+event.Stage.Phase)
		}
	}
	want := []string{"toolchain/", "toolchain/", "snapshot/" + trace.PhaseDiscover, "snapshot/" + trace.PhaseDiscover, "orphan/", "orphan/"}
	if !slices.Equal(stamped, want) {
		t.Fatalf("stage phases = %v, want %v", stamped, want)
	}
}

func TestStageFinishedCarriesResultAndDurationAndStartedCarriesNeither(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	recorder.Stage(fixtureStageName, fixtureStageDetail)(trace.ResultFailed)

	events := sink.Events()
	if len(events) != 3 {
		t.Fatalf("the recording holds %d events, want 3", len(events))
	}
	started, finished := events[1].Stage, events[2].Stage
	if started.State != trace.StateStarted || finished.State != trace.StateFinished {
		t.Fatalf("states are %q and %q", started.State, finished.State)
	}
	if started.Result != "" || started.DurationMS != nil {
		t.Errorf("the started stage carries result %q and duration %v", started.Result, started.DurationMS)
	}
	if started.Detail != fixtureStageDetail {
		t.Errorf("the started stage detail = %q, want %q", started.Detail, fixtureStageDetail)
	}
	if finished.Result != trace.ResultFailed {
		t.Errorf("the finished stage result = %q, want %q", finished.Result, trace.ResultFailed)
	}
	if finished.DurationMS == nil {
		t.Fatal("the finished stage carries no duration")
	}
	if *finished.DurationMS != fixtureTick.Milliseconds() {
		t.Errorf("the finished stage duration = %d, want %d", *finished.DurationMS, fixtureTick.Milliseconds())
	}
	if finished.Name != fixtureStageName {
		t.Errorf("the finished stage name = %q, want %q", finished.Name, fixtureStageName)
	}
}

func TestPhaseEndIsEmittedOnceHoweverOftenTheCloserRuns(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	endPhase := recorder.PhaseStart(trace.PhaseBaseline)
	endStage := recorder.Stage(fixtureStageName, "")
	endStage(trace.ResultSucceeded)
	endStage(trace.ResultFailed)
	endPhase()
	endPhase()
	endPhase()

	counts := map[string]int{}
	for _, event := range sink.Events() {
		counts[event.Type]++
	}
	if counts[trace.TypePhaseEnd] != 1 {
		t.Errorf("phase-end recorded %d times, want 1", counts[trace.TypePhaseEnd])
	}
	if counts[trace.TypeStage] != 2 {
		t.Errorf("stage recorded %d times, want 2", counts[trace.TypeStage])
	}
	last := sink.Events()[len(sink.Events())-1]
	if last.Type != trace.TypePhaseEnd || last.Phase.Name != trace.PhaseBaseline {
		t.Fatalf("the last event is %q for %v", last.Type, last.Phase)
	}
	if last.Phase.DurationMS == nil {
		t.Fatal("the phase-end carries no duration")
	}
	if *last.Phase.DurationMS != 3*fixtureTick.Milliseconds() {
		t.Errorf("phase duration = %d, want %d", *last.Phase.DurationMS, 3*fixtureTick.Milliseconds())
	}
}

func TestRunEndReportsVerdictExitCodeErrorAndAccounting(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	recorder.Note(trace.NoteWarning, fixtureNoteCode, fixtureNoteDetail)
	recorder.RunEnd("failed", 2, errors.New("baseline is red"))

	events := sink.Events()
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd {
		t.Fatalf("the last event is %q", last.Type)
	}
	if last.Run.Verdict != "failed" || last.Run.ExitCode != 2 {
		t.Errorf("run = %q/%d, want failed/2", last.Run.Verdict, last.Run.ExitCode)
	}
	if last.Run.Error != "baseline is red" {
		t.Errorf("run error = %q", last.Run.Error)
	}
	if last.Run.EventsEmitted != int64(len(events))-1 {
		t.Errorf("events_emitted = %d, want %d", last.Run.EventsEmitted, len(events)-1)
	}
	if last.Run.EventsDropped != 0 {
		t.Errorf("events_dropped = %d, want 0", last.Run.EventsDropped)
	}
}

func TestRunEndIsEmittedOnceAndNothingIsRecordedAfterIt(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	recorder.RunEnd(fixtureVerdict, 0, nil)
	recorder.RunEnd("second", 1, errors.New("ignored"))
	recorder.Artifact(trace.ArtifactTrace, fixtureArtifactPath)
	recorder.Note(trace.NoteWarning, fixtureNoteCode, fixtureNoteDetail)
	recorder.PhaseStart(trace.PhaseReport)()

	events := sink.Events()
	if len(events) != 2 {
		t.Fatalf("the recording holds %d events, want 2: %v", len(events), typesOf(events))
	}
	if events[1].Run.Verdict != fixtureVerdict {
		t.Errorf("run verdict = %q, want %q", events[1].Run.Verdict, fixtureVerdict)
	}
}

func typesOf(events []trace.Event) []string {
	kinds := make([]string, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Type)
	}
	return kinds
}

func TestRunEndCountsTheEventsTheSinkDropped(t *testing.T) {
	t.Parallel()

	// A ring of three: run-start, one of the four artifacts, and the slot the
	// ring holds back for run-end. The two events that fell out are the loss
	// the accounting has to admit to.
	sink := trace.NewMemorySink(3)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	for range 4 {
		recorder.Artifact(trace.ArtifactTrace, fixtureArtifactPath)
	}
	recorder.RunEnd(fixtureVerdict, 0, nil)

	events := sink.Events()
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd {
		t.Fatalf("the last event is %q, want %q", last.Type, trace.TypeRunEnd)
	}
	if last.Run.EventsDropped != 3 {
		t.Errorf("events_dropped = %d, want 3", last.Run.EventsDropped)
	}
	if last.Run.EventsEmitted != 2 {
		t.Errorf("events_emitted = %d, want 2", last.Run.EventsEmitted)
	}
}

func TestConcurrentRecordingKeepsEveryEventAndItsSequenceOrder(t *testing.T) {
	t.Parallel()

	const writers = 100
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, lockedClock(), fixtureStartRecord())
	var wait sync.WaitGroup
	for range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			recorder.Exec(fixtureExecRecord())
		}()
	}
	wait.Wait()
	recorder.RunEnd(fixtureVerdict, 0, nil)

	events := sink.Events()
	if len(events) != writers+2 {
		t.Fatalf("the recording holds %d events, want %d", len(events), writers+2)
	}
	for i, event := range events {
		if event.Seq != int64(i+1) {
			t.Fatalf("event at index %d carries seq %d: the file order and the seq order parted", i, event.Seq)
		}
	}
	if last := events[len(events)-1]; last.Run.EventsDropped != 0 {
		t.Errorf("events_dropped = %d, want 0", last.Run.EventsDropped)
	}
}

// lockedClock is [fixtureClock] made safe for the concurrent recorder test.
func lockedClock() func() time.Time {
	var mutex sync.Mutex
	clock := fixtureClock()
	return func() time.Time {
		mutex.Lock()
		defer mutex.Unlock()
		return clock()
	}
}

func TestIdenticalRecordingsProduceIdenticalBytes(t *testing.T) {
	t.Parallel()

	first, second := encodeAll(t, scriptedEvents(t)), encodeAll(t, scriptedEvents(t))
	if first != second {
		t.Errorf("two recordings of one script differ\n%s", unifiedDiff(first, second))
	}
}

func encodeAll(t *testing.T, events []trace.Event) string {
	t.Helper()
	var builder strings.Builder
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	return builder.String()
}

// unifiedDiff renders the difference between two multi-line documents in the
// familiar `-`/`+` form, so a golden mismatch names the line that moved rather
// than printing two walls of JSON for a reader to compare by eye.
func unifiedDiff(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	// The longest common subsequence of the two line lists, by the standard
	// table: common[i][j] is the length of the LCS of wantLines[i:] and
	// gotLines[j:].
	common := make([][]int, len(wantLines)+1)
	for i := range common {
		common[i] = make([]int, len(gotLines)+1)
	}
	for i := len(wantLines) - 1; i >= 0; i-- {
		for j := len(gotLines) - 1; j >= 0; j-- {
			if wantLines[i] == gotLines[j] {
				common[i][j] = common[i+1][j+1] + 1
				continue
			}
			common[i][j] = max(common[i+1][j], common[i][j+1])
		}
	}
	var builder strings.Builder
	builder.WriteString("--- want\n+++ got\n")
	i, j := 0, 0
	for i < len(wantLines) && j < len(gotLines) {
		switch {
		case wantLines[i] == gotLines[j]:
			builder.WriteString("  " + wantLines[i] + "\n")
			i, j = i+1, j+1
		case common[i+1][j] >= common[i][j+1]:
			builder.WriteString("- " + wantLines[i] + "\n")
			i++
		default:
			builder.WriteString("+ " + gotLines[j] + "\n")
			j++
		}
	}
	for ; i < len(wantLines); i++ {
		builder.WriteString("- " + wantLines[i] + "\n")
	}
	for ; j < len(gotLines); j++ {
		builder.WriteString("+ " + gotLines[j] + "\n")
	}
	return builder.String()
}

// TestExecNeverRecordsANullArgumentVector pins the one field of an `exec` event
// that a reader is entitled to iterate without checking it first.
//
// The refusal path is what makes this more than tidiness: an invalid spec is
// recorded with the argv as it was given, and "as it was given" for a spec with
// no argv at all is nil. `null` would then be an argument vector a strict
// decoder rejects, on exactly the event a developer opened the recording for.
func TestExecNeverRecordsANullArgumentVector(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	recorder.Exec(trace.ExecRecord{Kind: trace.ExecKindGoVersion, ExitCode: -1})

	recorded := sink.Events()[1]
	if recorded.Exec.Argv == nil {
		t.Fatal("the recorded event carries a nil argument vector")
	}
	encoded, err := json.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"argv":[]`) {
		t.Errorf("the recorded event does not carry an empty argv: %s", encoded)
	}
	// And a caller's own slice is cloned rather than kept, so a spec reused for
	// a retry cannot rewrite the command an earlier event recorded.
	argv := []string{"go", "version"}
	recorder.Exec(trace.ExecRecord{Kind: trace.ExecKindGoVersion, Argv: argv})
	argv[1] = "env"
	if got := sink.Events()[2].Exec.Argv[1]; got != "version" {
		t.Errorf("a later write reached the recorded argv: %q", got)
	}
}

// TestProbeExecAlwaysRecordsTheInfectionSetOfAMeasuredPass is the difference
// between "measured nothing" and "measured nothing worth recording".
//
// A measured pass that infected no mutant is the strongest thing the probe
// phase says about a binary: nothing it runs can observe any of them. Omitting
// the empty list would make that claim indistinguishable from a pass that was
// never measured, so the recorder writes `[]` and the schema requires it.
func TestProbeExecAlwaysRecordsTheInfectionSetOfAMeasuredPass(t *testing.T) {
	t.Parallel()

	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, fixtureClock(), fixtureStartRecord())
	recorder.ProbeExec(trace.ProbeRecord{Outcome: trace.ProbeOutcomeMeasured})
	recorder.ProbeExec(trace.ProbeRecord{Outcome: trace.ProbeOutcomeTimedOut, ExitCode: -1})

	events := sink.Events()
	if events[1].Probe.Infected == nil {
		t.Error("a measured pass recorded no infection set at all")
	}
	measured, err := json.Marshal(events[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(measured), `"infected":[]`) {
		t.Errorf("a measured pass that infected nothing does not say so: %s", measured)
	}
	// A pass that was not measured says nothing about any mutant, so it carries
	// no list at all rather than an empty one a reader could mistake for a
	// measurement.
	unmeasured, err := json.Marshal(events[2])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(unmeasured), "infected") {
		t.Errorf("an unmeasured pass carries an infection set: %s", unmeasured)
	}
}

// TestFailureRecordingPinsItsJSONFieldNamesAndOrder is the second half of the
// field-name contract.
//
// The happy-path golden cannot pin an error string, a timeout, a refused event
// or a non-zero exit code, because a recording that has them is a recording of
// a run that went wrong — and those are precisely the fields a reader opens a
// recording to look at. Regenerate the same way as the first golden.
func TestFailureRecordingPinsItsJSONFieldNamesAndOrder(t *testing.T) {
	t.Parallel()

	events := scriptedFailureEvents(t)
	if len(events) != fixtureFailureCount {
		t.Fatalf("the failure recording holds %d events, want %d", len(events), fixtureFailureCount)
	}
	got := encodeAll(t, events)
	testkit.Golden(t, goldenFailureRecording, []byte(got))
	last := events[len(events)-1]
	if last.Type != trace.TypeRunEnd {
		t.Fatalf("the last event is %q", last.Type)
	}
	if last.Run.Verdict != "failed" || last.Run.ExitCode != 2 || last.Run.Error == "" {
		t.Errorf("run = %q/%d/%q", last.Run.Verdict, last.Run.ExitCode, last.Run.Error)
	}
	if last.Run.EventsDropped != 1 {
		t.Errorf("events_dropped = %d, want the one event the sink refused", last.Run.EventsDropped)
	}
	// The recording is short one line, and the accounting is how a reader
	// learns that rather than by counting.
	if want := int64(len(events) - 1); last.Run.EventsEmitted != want {
		t.Errorf("events_emitted = %d, want %d", last.Run.EventsEmitted, want)
	}
}

// TestBinariesAndKilledByNameTestBinariesTheSameWay pins the one join a reader
// makes inside a single event.
//
// A test binary has two names: the file the run executed, and the import path
// of the package it was built from. `argv` is the first, because it is the
// command that ran; `binaries`, `killed_by` and `covering` are the second,
// because that is the name the run report uses and the name that survives a
// temporary directory being deleted. Mixing them would leave `killed_by`
// matching nothing in `binaries` on the very event a reader opens to ask what
// killed a mutant.
func TestBinariesAndKilledByNameTestBinariesTheSameWay(t *testing.T) {
	t.Parallel()

	for _, event := range slices.Concat(scriptedEvents(t), scriptedFailureEvents(t)) {
		switch event.Type {
		case trace.TypeMutantExec:
			assertImportPaths(t, "mutant.binaries", event.Mutant.Binaries)
			if killedBy := event.Mutant.KilledBy; killedBy != "" {
				assertImportPaths(t, "mutant.killed_by", []string{killedBy})
				if !slices.Contains(event.Mutant.Binaries, killedBy) {
					t.Errorf("killed_by %q names nothing in binaries %v", killedBy, event.Mutant.Binaries)
				}
			}
		case trace.TypeProbeExec:
			assertImportPaths(t, "probe.binaries", event.Probe.Binaries)
		case trace.TypeCoverageMap:
			assertImportPaths(t, "coverage.covering", event.Coverage.Covering)
		case trace.TypeExec:
			// The other half of the contract: argv[0] is the file that ran, and
			// stays a file path — argv itself is the whole vector, that
			// executable followed by the arguments it was given.
			argv := event.Exec.Argv
			if argv == nil {
				t.Error("exec argv is null")
				continue
			}
			if len(argv) > 0 && !strings.HasPrefix(argv[0], "/") && argv[0] != "go" {
				t.Errorf("exec argv %v does not begin with a command", argv)
			}
			if len(argv) < 2 {
				t.Errorf("exec argv %v carries no arguments after the executable", argv)
			}
		}
	}
}

// assertImportPaths fails for a name that is a file rather than a package.
func assertImportPaths(t *testing.T, field string, names []string) {
	t.Helper()
	for _, name := range names {
		if strings.HasPrefix(name, "/") || strings.HasSuffix(name, ".test") || strings.HasSuffix(name, ".exe") {
			t.Errorf("%s carries %q, which is a file rather than an import path", field, name)
		}
	}
}
