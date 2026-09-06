// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package trace

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"time"
)

// A Recorder writes one run's events into a [Sink].
//
// Every method is safe to call from many goroutines at once, and every method
// is nil-receiver safe. The nil recorder is the disabled trace: call sites
// record unconditionally, which keeps the traced and the untraced path
// identical and leaves no branch for a verdict to depend on.
type Recorder struct {
	sink    Sink
	now     func() time.Time
	started time.Time

	mutex sync.Mutex
	seq   int64
	// attempts counts the events handed to the sink, and failures the ones it
	// refused. A sink that counts its own drops is authoritative; failures is
	// the fallback for one that does not.
	attempts int64
	failures int64
	ended    bool
	// openPhase is the phase a stage is stamped with. It is recorder state
	// rather than a caller's argument so that no call site can mislabel one.
	openPhase string
}

// New opens a recording on sink and records its run-start event.
//
// A nil sink returns a nil recorder rather than an inert one, so that "no
// trace" is one representation rather than two. A nil now defaults to
// [time.Now].
func New(sink Sink, now func() time.Time, start StartRecord) *Recorder {
	if sink == nil {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	started := now()
	recorder := &Recorder{sink: sink, now: now, started: started}
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.emitLocked(started, Event{Type: TypeRunStart, Schema: SchemaV1, Start: &start})
	return recorder
}

// PhaseStart records the start of a phase and returns the closer that ends it.
//
// The closer is once-guarded and records the phase's duration, so a phase that
// is closed by a defer and again on an early return is still one span. Until it
// runs, every stage this recorder records is stamped with this phase.
func (recorder *Recorder) PhaseStart(name string) func() {
	if recorder == nil {
		return func() {}
	}
	recorder.mutex.Lock()
	started := recorder.now()
	recorder.openPhase = name
	recorder.emitLocked(started, Event{Type: TypePhaseStart, Phase: &PhaseRecord{Name: name}})
	recorder.mutex.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			recorder.mutex.Lock()
			defer recorder.mutex.Unlock()
			moment := recorder.now()
			if recorder.openPhase == name {
				recorder.openPhase = ""
			}
			elapsed := durationMS(moment.Sub(started))
			recorder.emitLocked(moment, Event{
				Type:  TypePhaseEnd,
				Phase: &PhaseRecord{Name: name, DurationMS: &elapsed},
			})
		})
	}
}

// Stage records the start of one step inside a phase and returns the closer
// that finishes it with a result.
//
// The closer is once-guarded, like a phase's. The phase stamped on both events
// is the one that was open when the stage started, so a stage that outlives its
// phase is still attributed to it.
func (recorder *Recorder) Stage(name, detail string) func(result string) {
	if recorder == nil {
		return func(string) {}
	}
	recorder.mutex.Lock()
	started := recorder.now()
	phase := recorder.openPhase
	recorder.emitLocked(started, Event{
		Type:  TypeStage,
		Stage: &StageRecord{Phase: phase, Name: name, State: StateStarted, Detail: detail},
	})
	recorder.mutex.Unlock()

	var once sync.Once
	return func(result string) {
		once.Do(func() {
			recorder.mutex.Lock()
			defer recorder.mutex.Unlock()
			moment := recorder.now()
			elapsed := durationMS(moment.Sub(started))
			recorder.emitLocked(moment, Event{
				Type: TypeStage,
				Stage: &StageRecord{
					Phase:      phase,
					Name:       name,
					State:      StateFinished,
					Result:     result,
					DurationMS: &elapsed,
				},
			})
		})
	}
}

// Prepare records one preparation stage of a library mutation session.
//
// The duration is recorded on the finished event alone, including when it is
// zero: a skipped stage is a started/finished pair with a zero duration, which
// is what lets a reader tell an intentional omission from an event that was
// lost.
func (recorder *Recorder) Prepare(phase, state, result string, duration time.Duration) {
	if recorder == nil {
		return
	}
	record := PrepareRecord{Phase: phase, State: state, Result: result}
	if state == StateFinished {
		elapsed := durationMS(duration)
		record.DurationMS = &elapsed
	}
	recorder.emit(Event{Type: TypePrepare, Prepare: &record})
}

// Every method below that takes a record struct is split in two: a wrapper
// holding nothing but the nil check, and a body marked `//go:noinline` that
// takes the record's address.
//
// The split is what makes the disabled recorder free, and it is worth stating
// because it reads as an indirection nobody needs. Call sites record
// unconditionally, so every one of these runs on every run whether or not a
// recording was asked for. Taking `&record` inside a method the compiler then
// inlines moves the *caller's* copy to the heap — escape analysis decides per
// variable rather than per branch — so a nil recorder was costing an allocation
// of eighty to a hundred and twelve bytes per mutant, per cache lookup and per
// mapped mutant, on a run that records nothing at all. With the address taken in
// a body that is never inlined into the wrapper, the record is copied onto that
// body's frame and escapes there, and the nil path never reaches it.
// TestNilRecorderCostsNoAllocation is the pin.

// Exec records one executed command and returns the sequence number it was
// recorded at, or zero when nothing was recorded.
//
// The returned sequence is how the rest of the recording points at a command:
// a mutant attempt names the executions it ran, a validation step names the
// compile it spent. Two reductions happen here rather than in any caller, so
// that no future call site can undo them: the environment becomes names alone,
// and the captured output becomes a size and a digest.
func (recorder *Recorder) Exec(record ExecRecord) int64 {
	if recorder == nil {
		return 0
	}
	return recorder.recordExec(record)
}

//go:noinline
func (recorder *Recorder) recordExec(record ExecRecord) int64 {
	// An argument vector is the one field of an exec event a reader may
	// iterate without checking it first, so it is never null. The refusal path
	// is why: a spec that could not be run is recorded with its argv as it was
	// given, and "as given" for a spec with no argv is nil. The clone is for
	// the same reason a caller may reuse a spec for a retry — an event already
	// recorded is not a place for a later write to arrive.
	if record.Argv == nil {
		record.Argv = []string{}
	} else {
		record.Argv = slices.Clone(record.Argv)
	}
	record.EnvNames = environmentNames(record.EnvNames)
	if len(record.Output) > 0 {
		digest := sha256.Sum256(record.Output)
		record.OutputBytes = len(record.Output)
		record.OutputSHA256 = hex.EncodeToString(digest[:])
	}
	return recorder.emit(Event{Type: TypeExec, Exec: &record})
}

// MutantExec records one attempt at one mutant and returns the sequence number
// it was recorded at, or zero when nothing was recorded.
//
// The sequence is returned for the reason [Recorder.Exec]'s is: something has
// to be able to point at the event afterwards. An execution phase discards it,
// because its attempts are only ever read back out of the recording — but the
// library API hands one attempt straight to its caller, and the `TraceSeq` on
// that result is the whole join between a consumer's own recording and this
// one.
func (recorder *Recorder) MutantExec(record MutantRecord) int64 {
	if recorder == nil {
		return 0
	}
	return recorder.recordMutantExec(record)
}

//go:noinline
func (recorder *Recorder) recordMutantExec(record MutantRecord) int64 {
	return recorder.emit(Event{Type: TypeMutantExec, Mutant: &record})
}

// ProbeExec records one pass through the probe tree and returns the sequence
// number it was recorded at, or zero when nothing was recorded.
//
// A measured pass always records its infection set, empty included: "measured
// and infected nothing" is the strongest statement the probe phase makes, and
// it would be indistinguishable from "not measured" if the empty list were
// omitted. A pass that reached no outcome is left exactly as it was given,
// because a recorder that quietly dropped a caller's facts would hide the bug
// rather than the field.
func (recorder *Recorder) ProbeExec(record ProbeRecord) int64 {
	if recorder == nil {
		return 0
	}
	return recorder.recordProbeExec(record)
}

//go:noinline
func (recorder *Recorder) recordProbeExec(record ProbeRecord) int64 {
	if record.Outcome == ProbeOutcomeMeasured && record.Infected == nil {
		record.Infected = []string{}
	}
	return recorder.emit(Event{Type: TypeProbeExec, Probe: &record})
}

// Validate records one validation or bisection step.
func (recorder *Recorder) Validate(record ValidateRecord) {
	if recorder == nil {
		return
	}
	recorder.recordValidate(record)
}

//go:noinline
func (recorder *Recorder) recordValidate(record ValidateRecord) {
	recorder.emit(Event{Type: TypeValidate, Validate: &record})
}

// Coverage records how coverage placed one mutant.
func (recorder *Recorder) Coverage(record CoverageRecord) {
	if recorder == nil {
		return
	}
	recorder.recordCoverage(record)
}

//go:noinline
func (recorder *Recorder) recordCoverage(record CoverageRecord) {
	recorder.emit(Event{Type: TypeCoverageMap, Coverage: &record})
}

// Cache records one cache decision.
func (recorder *Recorder) Cache(record CacheRecord) {
	if recorder == nil {
		return
	}
	recorder.recordCache(record)
}

//go:noinline
func (recorder *Recorder) recordCache(record CacheRecord) {
	recorder.emit(Event{Type: TypeCache, Cache: &record})
}

// Snapshot records one frozen tree.
func (recorder *Recorder) Snapshot(record SnapshotRecord) {
	if recorder == nil {
		return
	}
	recorder.recordSnapshot(record)
}

//go:noinline
func (recorder *Recorder) recordSnapshot(record SnapshotRecord) {
	recorder.emit(Event{Type: TypeSnapshot, Snapshot: &record})
}

// Sweep records what collection reclaimed.
func (recorder *Recorder) Sweep(record SweepRecord) {
	if recorder == nil {
		return
	}
	recorder.recordSweep(record)
}

//go:noinline
func (recorder *Recorder) recordSweep(record SweepRecord) {
	recorder.emit(Event{Type: TypeSweep, Sweep: &record})
}

// Artifact records a file or directory the run wrote or kept.
func (recorder *Recorder) Artifact(kind, path string) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeArtifact, Artifact: &ArtifactRecord{Kind: kind, Path: path}})
}

// Note records something the run could not do.
func (recorder *Recorder) Note(kind, code, detail string) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeNote, Note: &NoteRecord{Kind: kind, Code: code, Detail: detail}})
}

// RunEnd closes the recording with the run's verdict and its accounting.
//
// It is recorded once, and nothing is recorded after it: a recording has one
// last line, and a reader who found it has read the whole run. The accounting
// is taken before the event is written, which is why a bounded ring holds its
// last slot back for it.
func (recorder *Recorder) RunEnd(verdict string, exitCode int, err error) {
	if recorder == nil {
		return
	}
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	if recorder.ended {
		return
	}
	record := &RunRecord{Verdict: verdict, ExitCode: exitCode}
	if err != nil {
		record.Error = err.Error()
	}
	record.EventsDropped = recorder.droppedLocked()
	record.EventsEmitted = max(recorder.attempts-record.EventsDropped, 0)
	recorder.emitLocked(recorder.now(), Event{Type: TypeRunEnd, Run: record})
	recorder.ended = true
}

// droppedLocked asks the sink how much it lost, and falls back to counting
// refusals. A sink that reports its own drops is authoritative: a bounded ring
// discards an old event without any call failing, and that is a loss too.
func (recorder *Recorder) droppedLocked() int64 {
	if dropper, ok := recorder.sink.(Dropper); ok {
		return dropper.Dropped()
	}
	return recorder.failures
}

func (recorder *Recorder) emit(event Event) int64 {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return recorder.emitLocked(recorder.now(), event)
}

// emitLocked stamps the envelope and hands the event to the sink, returning the
// sequence number it was given. A sink failure is counted, never returned: a
// diagnostic that makes the tool less reliable than it was without it inverts
// the point of the feature.
func (recorder *Recorder) emitLocked(moment time.Time, event Event) int64 {
	if recorder.ended {
		return 0
	}
	recorder.seq++
	event.Seq = recorder.seq
	event.Timestamp = moment.UTC().Format(time.RFC3339Nano)
	event.ElapsedMS = durationMS(moment.Sub(recorder.started))
	recorder.attempts++
	if !recorder.deliver(event) {
		recorder.failures++
	}
	return event.Seq
}

// deliver hands one event to the sink and reports whether it was kept.
//
// A sink that panics is counted exactly as one that returned an error. That is
// not defensiveness about this package's own sinks: a Sink is an interface, an
// embedder's implementation of it is ordinary Go code, and ordinary Go code
// panics. Without the recover the panic unwinds through this recorder — through
// the lock, on whichever goroutine was recording, which during a mutation run is
// one of the execution workers — and takes the whole process with it. A
// diagnostic that can kill the run it is a diagnostic of inverts the point of
// having one; see the fail-open argument in the package documentation.
//
// The lock is not released by the recover, and does not need to be: the caller
// holds it and unlocks on its own deferred path, so a panicking sink costs the
// event and leaves the recorder usable for the next one.
func (recorder *Recorder) deliver(event Event) (kept bool) {
	defer func() {
		if recover() != nil {
			kept = false
		}
	}()
	return recorder.sink.Emit(event) == nil
}

// environmentNames reduces environment entries to their names, sorted and
// deduplicated. An entry with no name at all contributes nothing, and an empty
// result is nil so that the field is omitted rather than written as an empty
// array.
func environmentNames(entries []string) []string {
	if len(entries) == 0 {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name, _, _ := strings.Cut(entry, "=")
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) == 0 {
		return nil
	}
	return names
}
