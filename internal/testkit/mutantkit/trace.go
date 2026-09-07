// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

// TraceRingSize is how many events a test's recording holds.
//
// A recording is a diagnostic, so it costs a bounded amount of memory rather
// than growing with the run: four thousand events is more than any suite here
// records and a few megabytes at the very worst. What falls out of the ring is
// counted, and the run-end event says how much.
const TraceRingSize = 4096

// TraceTailLines is how much of the ring a failure prints.
//
// The whole ring goes into the kept directory, where a reader can page through
// it; the log gets the end of it. A failure that printed four thousand lines
// would push the assertion that failed off the top of a CI log, which is the one
// thing worse than printing nothing at all.
const TraceTailLines = 200

// traceToolVersion is what the recording says produced it.
//
// It is not go-mutants' own version, and saying so is the point: these events
// were recorded by the test harness driving the engine, not by a run of the
// tool, and a recording that claimed otherwise would be indistinguishable from
// one a user sent in.
const traceToolVersion = "go-mutants test harness"

// Trace is the recording one test writes into, for the options that take a
// [trace.Recorder] — internal/validate's and internal/execute's.
//
// It is the diagnostic half of the keep policy. A test that drives the engine
// fails with an assertion about an outcome — a mutant that lived, a validation
// that rejected — and the question that follows is always the same: what did it
// actually run? The recording answers it. On a failure the last
// [TraceTailLines] events are logged, one line each, and the tail goes into the
// kept account beside them; when the policy keeps this test's directories, the
// whole ring is written into the evidence directory as `trace.jsonl` in the
// encoding [trace.DirSink] writes, so `trace validate`, `trace summary` and
// [trace.Read] all take it.
//
// One recording per test, however many times this is called: a test that hands
// the recorder to two phases is recording one run of itself.
//
// It returns nil for a test that has already taken [TraceSink], and a nil
// [trace.Recorder] is the disabled one — every method on it is nil-safe, so a
// caller needs no branch. The two are alternatives rather than layers: whoever
// holds the sink writes the run-start and the run-end, and a recording with two
// of either is one no reader will accept.
func Trace(t testing.TB) *trace.Recorder {
	t.Helper()
	r := attach(t)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lent {
		return nil
	}
	if r.recorder == nil {
		r.recorder = trace.New(r.sink, nil, trace.StartRecord{
			Kind:        trace.StartKindWorkspace,
			ToolVersion: traceToolVersion,
			PID:         os.Getpid(),
			Root:        testkit.Root(t),
		})
		r.owned = true
	}
	return r.recorder
}

// TraceSink is the same recording for the options that take a [trace.Sink] —
// internal/engine's, and the public API's `OpenOptions.Trace`.
//
// The engine opens a recorder of its own over the sink it is given, which is why
// this exists at all: handing it the recorder above would put two run-start
// events in one stream. So the sink is lent instead, the engine writes the whole
// recording including its accounting, and everything this package does with it —
// the tail on a failure, the file in the kept directory — is unchanged.
func TraceSink(t testing.TB) trace.Sink {
	t.Helper()
	r := attach(t)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lent = true
	return r.sink
}

// A recording is one test's ring and whatever is writing into it.
type recording struct {
	mu       sync.Mutex
	sink     trace.Sink
	ring     *trace.MemorySink
	recorder *trace.Recorder
	// owned says the recorder above is this package's, and therefore that the
	// run-end is this package's to write. A lent sink belongs to the engine,
	// which writes its own.
	owned bool
	lent  bool
}

// recordings holds one per test, for the same reason and in the same shape as
// the harness's own ledger: the helpers that reach for it are spread across the
// suites and have a [testing.TB] and nothing else.
var recordings struct {
	mu     sync.Mutex
	byTest map[string]*recording
}

// attach returns this test's recording, creating it and registering what
// happens to it on the first call.
func attach(t testing.TB) *recording {
	recordings.mu.Lock()
	if recordings.byTest == nil {
		recordings.byTest = map[string]*recording{}
	}
	name := t.Name()
	if existing, ok := recordings.byTest[name]; ok {
		recordings.mu.Unlock()
		return existing
	}
	ring := trace.NewMemorySink(TraceRingSize)
	// Digested: the size and digest of a command's output stay, the bytes do
	// not, so the ring's cost is bounded by the ring rather than by what the
	// tests printed. It is what a library workspace records into when nobody
	// asked for a file.
	fresh := &recording{sink: trace.Digested(ring), ring: ring}
	recordings.byTest[name] = fresh
	recordings.mu.Unlock()

	// Resolved now rather than in the cleanup, for the reason
	// [testkit.DumpFiles] resolves it now: creating the kept directory registers
	// the cleanup that decides its fate, and a cleanup may not be what starts
	// that.
	kept := testkit.KeptDir(t)
	testkit.KeepSection(t, "Trace tail", func() []string { return traceTail(ring, TraceTailLines) })

	t.Cleanup(func() {
		recordings.mu.Lock()
		delete(recordings.byTest, name)
		recordings.mu.Unlock()

		fresh.close(t)
		if t.Failed() {
			tail := traceTail(ring, TraceTailLines)
			held, dropped := accounting(ring)
			t.Logf("trace tail (%d of %d events held, %d lost):\n%s",
				len(tail), held, dropped, strings.Join(tail, "\n"))
		}
		if !testkit.Keeping(t) || kept == "" {
			return
		}
		if err := writeRecording(filepath.Join(kept, trace.FileName), ring.Events()); err != nil {
			t.Logf("mutantkit: the recording could not be kept: %v", err)
		}
	})
	return fresh
}

// close writes the run-end event, when the recorder is this package's to close.
//
// Without it every kept recording is one `trace summary` calls incomplete and
// `trace validate` reports as truncated — which is what an interrupted run
// looks like, so a harness that closed none of its own would have every kept
// trace claiming the test was killed. The run-end is also the only place the
// drop tally is written, so a ring that overflowed could not say so.
//
// The verdict is the test's own. It is recorded once: [trace.Recorder.RunEnd] is
// guarded, so a test that closed its own recording keeps the verdict it chose.
func (r *recording) close(t testing.TB) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.owned {
		return
	}
	verdict := "ok"
	if t.Failed() {
		verdict = "failed"
	}
	r.recorder.RunEnd(verdict, 0, nil)
}

// accounting is what the recording says about itself: how many events the ring
// is holding, and how many it lost.
//
// The loss comes from the run-end event where there is one, because that is the
// authoritative tally — a bounded ring drops an old event without any call
// failing, and the recorder writes what the sink reported at the moment it
// closed. The count of what is *held* is the ring's own length rather than the
// run-end's `events_emitted`, which is taken before the run-end is written and
// is therefore one short of what a reader is looking at.
func accounting(ring *trace.MemorySink) (held, dropped int64) {
	events := ring.Events()
	if len(events) != 0 {
		if last := events[len(events)-1]; last.Type == trace.TypeRunEnd && last.Run != nil {
			return int64(len(events)), last.Run.EventsDropped
		}
	}
	return int64(len(events)), ring.Dropped()
}

// writeRecording writes a ring out as JSON Lines, byte for byte what a
// [trace.DirSink] would have written.
//
// One json.Marshal per event and a newline after it is the whole encoding: the
// sink does the same, which is what makes a kept recording something `trace
// validate` and [trace.Read] accept rather than a second format to support. An
// event that will not marshal is skipped rather than aborting the file — the
// rest of the recording is still worth having, and a diagnostic that fails to
// write because of one line is the failure this whole feature exists to avoid.
func writeRecording(path string, events []trace.Event) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	var failures []error
	for _, event := range events {
		encoded, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			failures = append(failures, marshalErr)
			continue
		}
		if _, writeErr := file.Write(append(encoded, '\n')); writeErr != nil {
			failures = append(failures, writeErr)
			break
		}
	}
	if closeErr := file.Close(); closeErr != nil {
		failures = append(failures, closeErr)
	}
	if len(failures) != 0 {
		return failures[0]
	}
	return nil
}

// traceTail renders the last of a ring, one line per event.
//
// The renderer is written here rather than borrowed, and the duplication is
// deliberate for want of an alternative: internal/console has no exported
// line renderer for a trace event — its two renderers take engine events — so
// the choice was between exporting one from a package whose subject is the
// user-facing display and writing five lines here. If a `-vv` renderer is ever
// exported, this should become a call to it.
//
// The fields are the ones a reader asks for in order: which event, what kind of
// thing it was, what it was about, how it ended, how long it took, and — for a
// command — the argument vector, because that is what somebody re-runs.
func traceTail(ring *trace.MemorySink, limit int) []string {
	events := ring.Events()
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	lines := make([]string, 0, len(events))
	for _, event := range events {
		lines = append(lines, traceLine(event))
	}
	return lines
}

// traceLine is one event as `seq type kind subject exit duration digest argv…`,
// with every field a particular event does not have simply left out.
func traceLine(event trace.Event) string {
	fields := []string{strconv.FormatInt(event.Seq, 10), event.Type}
	switch {
	case event.Start != nil:
		fields = append(fields, event.Start.Kind, event.Start.Root)
	case event.Phase != nil:
		fields = append(fields, event.Phase.Name, durationField(event.Phase.DurationMS))
	case event.Stage != nil:
		fields = append(fields, event.Stage.Name, event.Stage.State, event.Stage.Result,
			durationField(event.Stage.DurationMS))
	case event.Prepare != nil:
		fields = append(fields, event.Prepare.Phase, event.Prepare.State, event.Prepare.Result,
			durationField(event.Prepare.DurationMS))
	case event.Exec != nil:
		fields = append(fields, event.Exec.Kind, event.Exec.Subject,
			"exit="+strconv.Itoa(event.Exec.ExitCode),
			millis(event.Exec.DurationMS), digest8(event.Exec.OutputSHA256),
			strings.Join(event.Exec.Argv, " "))
	case event.Mutant != nil:
		fields = append(fields, event.Mutant.DisplayID, event.Mutant.ID, event.Mutant.Outcome,
			millis(event.Mutant.DurationMS))
	case event.Probe != nil:
		fields = append(fields, event.Probe.Outcome, "exit="+strconv.Itoa(event.Probe.ExitCode))
	case event.Validate != nil:
		fields = append(fields, event.Validate.Tree, event.Validate.Op)
	case event.Cache != nil:
		fields = append(fields, event.Cache.Op, event.Cache.Result)
	case event.Snapshot != nil:
		fields = append(fields, event.Snapshot.Kind, event.Snapshot.Dir, digest8(event.Snapshot.Digest))
	case event.Sweep != nil:
		fields = append(fields, event.Sweep.Parent)
	case event.Artifact != nil:
		fields = append(fields, event.Artifact.Kind, event.Artifact.Path)
	case event.Note != nil:
		fields = append(fields, event.Note.Kind, event.Note.Code, event.Note.Detail)
	case event.Run != nil:
		fields = append(fields, event.Run.Verdict, "exit="+strconv.Itoa(event.Run.ExitCode), event.Run.Error)
	}
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != "" {
			kept = append(kept, field)
		}
	}
	return strings.Join(kept, " ")
}

// durationField renders the duration a span carries only when it has one: a
// started event and an event whose duration was lost are not the same statement,
// which is why the field is a pointer in the first place.
func durationField(ms *int64) string {
	if ms == nil {
		return ""
	}
	return millis(*ms)
}

func millis(ms int64) string { return fmt.Sprintf("%dms", ms) }

// digest8 is the first eight hex digits of an output digest, which is enough to
// tell two captures apart by eye and short enough to sit in a line.
func digest8(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}
