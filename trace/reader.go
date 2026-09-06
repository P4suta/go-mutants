// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	// readBufferSize and maximumLineBytes bound the reader. A recording is a
	// file somebody else wrote — a CI runner, a colleague's bug report — so a
	// line that would exhaust memory is refused rather than read.
	readBufferSize   = 64 << 10
	maximumLineBytes = 16 << 20
)

// An ExecTally is how many commands of one kind a recording holds and how long
// they took between them.
type ExecTally struct {
	Count      int
	DurationMS int64
}

// A Summary is what one recording says about itself, without the events.
//
// It is what `trace summary` prints and what a baseline is compared against. A
// reader has two things to check before trusting a recording: that [HasRunEnd]
// is true, and that [EventsDropped] and [MissingSequences] are zero. A
// recording failing the first was interrupted; one failing the second is lossy,
// and says so.
type Summary struct {
	// Path is the stream that was read, which is the file named even when a
	// directory was given.
	Path string

	// Missing is a recording that does not exist. It is returned rather than
	// reported as an error, because "there is no trace for that run" is an
	// answer.
	Missing bool

	Events           int
	FirstSequence    int64
	LastSequence     int64
	MissingSequences int64
	HasRunEnd        bool
	EventsDropped    int64
	Verdict          string
	ExitCode         int
	Error            string

	// Counts is how many events of each type the recording holds.
	Counts map[string]int

	// PhaseDurationMS is the total time each phase reported.
	PhaseDurationMS map[string]int64

	// StageDurationMS is the total time each stage reported, keyed by
	// `<phase>/<name>` — or by the bare name for a stage recorded with no
	// phase open. The same stage name appears in more than one phase, so a key
	// of the name alone would add two unrelated spans together.
	StageDurationMS map[string]int64

	// PrepareDurationMS is the total time each library preparation stage
	// reported.
	PrepareDurationMS map[string]int64

	// ExecByKind is how many commands of each kind ran and how long they took.
	// It is the number a performance question is asked of: "where did the run
	// go" is answered by the kinds, not by the event types.
	ExecByKind map[string]ExecTally

	// MutantOutcomes is how many attempts reached each outcome.
	MutantOutcomes map[string]int
}

// A SummaryDiff is the difference between two recordings. It is a read-only
// diagnostic comparison, never a verdict comparison and never evidence.
type SummaryDiff struct {
	EventsDelta           int
	MissingSequencesDelta int64
	EventsDroppedDelta    int64

	BeforeVerdict string
	AfterVerdict  string
	BeforeRunEnd  bool
	AfterRunEnd   bool

	CountDelta             map[string]int
	PhaseDurationDeltaMS   map[string]int64
	StageDurationDeltaMS   map[string]int64
	PrepareDurationDeltaMS map[string]int64
	ExecDelta              map[string]ExecTally
	MutantOutcomeDelta     map[string]int
}

// Read returns every event of a recording, in the order the file holds them.
//
// It is strict on purpose. A recording is a contract, and a reader that
// silently accepted a document outside it would report a run that never
// happened. Refused, in full:
//
//   - an unknown field, in the envelope or in a payload;
//   - a trailing value after an event;
//   - an event carrying no payload, two payloads, or a payload its type does
//     not name;
//   - an unknown event type;
//   - a `schema` on anything but a run-start, or a run-start without one;
//   - a run-start anywhere but the first line;
//   - any event after a run-end, a second run-end included;
//   - a sequence number that does not increase;
//   - a started stage or preparation stage carrying a result or a duration, or
//     a finished one carrying no duration;
//   - a phase-start carrying a duration, or a phase-end carrying none;
//   - an infection set on a probe pass that was not measured.
//
// The last four are rules the schema cannot state: the schema validates one
// line at a time, so where a line sits in a stream is the reader's to enforce.
//
// A run-start that is missing altogether is *not* refused. A bounded ring that
// overflowed begins partway through the run it recorded, and that is a lossy
// recording rather than an invalid one — [Summary.MissingSequences] is where it
// says so.
//
// path may name the stream or the run directory that holds it.
func Read(path string) ([]Event, error) {
	var events []Event
	_, err := readStream(path, func(event Event) { events = append(events, event) })
	if err != nil {
		return nil, err
	}
	return events, nil
}

// ReadSummary reads a recording and returns what it says about itself.
//
// A recording that does not exist is [Summary.Missing] rather than an error;
// every other failure of the file is an error, because a recording that cannot
// be parsed is not a recording that says nothing.
func ReadSummary(path string) (Summary, error) {
	summary, err := readStream(path, nil)
	if err != nil {
		return Summary{}, err
	}
	return summary, nil
}

// readStream is the one pass both readers make over a file.
func readStream(path string, keep func(Event)) (Summary, error) {
	stream := path
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		stream = filepath.Join(path, FileName)
	}
	summary := newSummary(stream)
	file, err := os.Open(stream)
	if errors.Is(err, os.ErrNotExist) {
		summary.Missing = true
		return summary, nil
	}
	if err != nil {
		return Summary{}, fmt.Errorf("go-mutants: open trace %s: %w", stream, err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, readBufferSize), maximumLineBytes)
	var previous int64
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		event, err := decodeEvent(line, summary.Events+1)
		if err != nil {
			return Summary{}, err
		}
		if err := summary.accept(event, &previous); err != nil {
			return Summary{}, err
		}
		if keep != nil {
			keep(event)
		}
	}
	if err := scanner.Err(); err != nil {
		return Summary{}, fmt.Errorf("go-mutants: read trace %s: %w", stream, err)
	}
	return summary, nil
}

func newSummary(stream string) Summary {
	return Summary{
		Path:              stream,
		Counts:            map[string]int{},
		PhaseDurationMS:   map[string]int64{},
		StageDurationMS:   map[string]int64{},
		PrepareDurationMS: map[string]int64{},
		ExecByKind:        map[string]ExecTally{},
		MutantOutcomes:    map[string]int{},
	}
}

// decodeEvent decodes one line strictly and checks it against the contract.
func decodeEvent(line []byte, ordinal int) (Event, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var event Event
	if err := decoder.Decode(&event); err != nil {
		return Event{}, fmt.Errorf("go-mutants: decode trace event %d: %w", ordinal, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Event{}, fmt.Errorf("go-mutants: trace event %d has trailing data", ordinal)
	}
	if err := validateEvent(event, ordinal); err != nil {
		return Event{}, err
	}
	return event, nil
}

// accept folds one event into the summary and checks the stream's own
// invariants: sequence numbers increase, and there is at most one run-end.
func (summary *Summary) accept(event Event, previous *int64) error {
	// Where a line sits is a rule of the stream rather than of the line, so it
	// is checked here and not in validateEvent. A run-start opens a recording
	// and a run-end closes it: a reader who found the last line has read the
	// whole run, which is only true if nothing follows it.
	if summary.HasRunEnd {
		return fmt.Errorf("go-mutants: trace event %d follows the run-end event", event.Seq)
	}
	if event.Type == TypeRunStart && summary.Events > 0 {
		return fmt.Errorf("go-mutants: trace event %d is a run-start that is not the first line", event.Seq)
	}
	if *previous != 0 {
		if event.Seq <= *previous {
			return fmt.Errorf("go-mutants: trace sequence %d does not follow %d", event.Seq, *previous)
		}
		summary.MissingSequences += event.Seq - *previous - 1
	} else {
		summary.FirstSequence = event.Seq
		summary.MissingSequences += event.Seq - 1
	}
	*previous = event.Seq
	summary.LastSequence = event.Seq
	summary.Events++
	summary.Counts[event.Type]++

	switch event.Type {
	case TypePhaseEnd:
		summary.PhaseDurationMS[event.Phase.Name] += *event.Phase.DurationMS
	case TypeStage:
		if event.Stage.State == StateFinished {
			summary.StageDurationMS[stageKey(event.Stage)] += *event.Stage.DurationMS
		}
	case TypePrepare:
		if event.Prepare.State == StateFinished {
			summary.PrepareDurationMS[event.Prepare.Phase] += *event.Prepare.DurationMS
		}
	case TypeExec:
		tally := summary.ExecByKind[event.Exec.Kind]
		tally.Count++
		tally.DurationMS += event.Exec.DurationMS
		summary.ExecByKind[event.Exec.Kind] = tally
	case TypeMutantExec:
		if event.Mutant.Outcome != "" {
			summary.MutantOutcomes[event.Mutant.Outcome]++
		}
	case TypeRunEnd:
		// A second run-end is refused by the rule above rather than here: it
		// is one case of "nothing follows the last line".
		summary.HasRunEnd = true
		summary.EventsDropped = event.Run.EventsDropped
		summary.Verdict = event.Run.Verdict
		summary.ExitCode = event.Run.ExitCode
		summary.Error = event.Run.Error
	}
	return nil
}

// stageKey is how a stage is named in [Summary.StageDurationMS].
func stageKey(record *StageRecord) string {
	if record.Phase == "" {
		return record.Name
	}
	return record.Phase + "/" + record.Name
}

// validateEvent checks one event against the contract the schema states: a
// well-formed envelope, exactly the payload the type names, and the two
// span-shaped payloads' started/finished rules.
func validateEvent(event Event, ordinal int) error {
	if event.Seq <= 0 || event.Type == "" || event.Timestamp == "" || event.ElapsedMS < 0 {
		return fmt.Errorf("go-mutants: trace event %d has an invalid envelope", ordinal)
	}
	if event.Type == TypeRunStart {
		if event.Schema != SchemaV1 {
			return fmt.Errorf("go-mutants: trace event %d has unsupported schema %q", ordinal, event.Schema)
		}
	} else if event.Schema != "" {
		return fmt.Errorf("go-mutants: trace event %d is a %s carrying a schema", ordinal, event.Type)
	}
	want, known := payloadOf(event, event.Type)
	if !known {
		return fmt.Errorf("go-mutants: trace event %d has unknown type %q", ordinal, event.Type)
	}
	if !want {
		return fmt.Errorf("go-mutants: trace event %d is missing its %s payload", ordinal, event.Type)
	}
	if carried := countPayloads(event); carried != 1 {
		return fmt.Errorf("go-mutants: trace event %d has %d payloads", ordinal, carried)
	}
	return spanRules(event, ordinal)
}

// payloadOf reports whether the event carries the payload its type names, and
// whether the type is one this contract knows at all.
func payloadOf(event Event, eventType string) (carried, known bool) {
	switch eventType {
	case TypeRunStart:
		return event.Start != nil, true
	case TypePhaseStart, TypePhaseEnd:
		return event.Phase != nil, true
	case TypeStage:
		return event.Stage != nil, true
	case TypePrepare:
		return event.Prepare != nil, true
	case TypeExec:
		return event.Exec != nil, true
	case TypeMutantExec:
		return event.Mutant != nil, true
	case TypeProbeExec:
		return event.Probe != nil, true
	case TypeValidate:
		return event.Validate != nil, true
	case TypeCoverageMap:
		return event.Coverage != nil, true
	case TypeCache:
		return event.Cache != nil, true
	case TypeSnapshot:
		return event.Snapshot != nil, true
	case TypeSweep:
		return event.Sweep != nil, true
	case TypeArtifact:
		return event.Artifact != nil, true
	case TypeNote:
		return event.Note != nil, true
	case TypeRunEnd:
		return event.Run != nil, true
	default:
		return false, false
	}
}

func countPayloads(event Event) int {
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

// spanRules checks the payloads whose fields depend on each other: the two
// that come in started/finished pairs, a phase that is only timed when it
// ends, the accounting a run-end is read for, and the outcome that licenses a
// probe pass's infection set.
func spanRules(event Event, ordinal int) error {
	switch event.Type {
	case TypePhaseStart:
		if event.Phase.DurationMS != nil {
			return fmt.Errorf("go-mutants: trace event %d is a phase-start carrying a duration", ordinal)
		}
	case TypePhaseEnd:
		if event.Phase.DurationMS == nil || *event.Phase.DurationMS < 0 {
			return fmt.Errorf("go-mutants: trace event %d is a phase-end without a duration", ordinal)
		}
	case TypeStage:
		return spanState(event.Stage.State, event.Stage.Result, event.Stage.DurationMS, ordinal, "stage")
	case TypePrepare:
		return spanState(event.Prepare.State, event.Prepare.Result, event.Prepare.DurationMS, ordinal, "prepare")
	case TypeProbeExec:
		// Facts come from a measured pass alone, so an infection set beside
		// any other outcome would be a measurement to one reader and an error
		// to another. An empty set is still a measurement, which is why the
		// test is on the field being present rather than on its length.
		measured := event.Probe.Outcome == ProbeOutcomeMeasured
		if event.Probe.Infected != nil && !measured {
			return fmt.Errorf("go-mutants: trace event %d records infections for a probe pass that was not measured", ordinal)
		}
		if measured && event.Probe.Infected == nil {
			return fmt.Errorf("go-mutants: trace event %d is a measured probe pass with no infection set", ordinal)
		}
	case TypeRunEnd:
		if event.Run.EventsEmitted < 0 || event.Run.EventsDropped < 0 {
			return fmt.Errorf("go-mutants: trace event %d has negative accounting", ordinal)
		}
	}
	return nil
}

func spanState(state, result string, duration *int64, ordinal int, name string) error {
	switch state {
	case StateStarted:
		if result != "" || duration != nil {
			return fmt.Errorf("go-mutants: trace event %d has a started %s carrying a result or a duration", ordinal, name)
		}
	case StateFinished:
		if duration == nil || *duration < 0 {
			return fmt.Errorf("go-mutants: trace event %d has a finished %s without a duration", ordinal, name)
		}
	default:
		return fmt.Errorf("go-mutants: trace event %d has an invalid %s state %q", ordinal, name, state)
	}
	return nil
}

// Diff reports what changed between two recordings.
func Diff(before, after Summary) SummaryDiff {
	diff := SummaryDiff{
		EventsDelta:           after.Events - before.Events,
		MissingSequencesDelta: after.MissingSequences - before.MissingSequences,
		EventsDroppedDelta:    after.EventsDropped - before.EventsDropped,
		BeforeVerdict:         before.Verdict,
		AfterVerdict:          after.Verdict,
		BeforeRunEnd:          before.HasRunEnd,
		AfterRunEnd:           after.HasRunEnd,
		CountDelta:            map[string]int{},
		PhaseDurationDeltaMS:  map[string]int64{},
		StageDurationDeltaMS:  map[string]int64{},

		PrepareDurationDeltaMS: map[string]int64{},
		ExecDelta:              map[string]ExecTally{},
		MutantOutcomeDelta:     map[string]int{},
	}
	countDelta(diff.CountDelta, before.Counts, after.Counts)
	countDelta(diff.MutantOutcomeDelta, before.MutantOutcomes, after.MutantOutcomes)
	durationDelta(diff.PhaseDurationDeltaMS, before.PhaseDurationMS, after.PhaseDurationMS)
	durationDelta(diff.StageDurationDeltaMS, before.StageDurationMS, after.StageDurationMS)
	durationDelta(diff.PrepareDurationDeltaMS, before.PrepareDurationMS, after.PrepareDurationMS)
	for kind, tally := range before.ExecByKind {
		delta := diff.ExecDelta[kind]
		delta.Count -= tally.Count
		delta.DurationMS -= tally.DurationMS
		diff.ExecDelta[kind] = delta
	}
	for kind, tally := range after.ExecByKind {
		delta := diff.ExecDelta[kind]
		delta.Count += tally.Count
		delta.DurationMS += tally.DurationMS
		diff.ExecDelta[kind] = delta
	}
	return diff
}

func countDelta(into, before, after map[string]int) {
	for key, count := range before {
		into[key] -= count
	}
	for key, count := range after {
		into[key] += count
	}
}

func durationDelta(into, before, after map[string]int64) {
	for key, duration := range before {
		into[key] -= duration
	}
	for key, duration := range after {
		into[key] += duration
	}
}
