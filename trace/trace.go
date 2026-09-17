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

type Recorder struct {
	sink    Sink
	now     func() time.Time
	started time.Time

	mutex     sync.Mutex
	seq       int64
	attempts  int64
	failures  int64
	ended     bool
	openPhase string
}

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

func (recorder *Recorder) PhaseStart(name string) func() time.Duration {
	if recorder == nil {
		return func() time.Duration { return 0 }
	}
	recorder.mutex.Lock()
	started := recorder.now()
	recorder.openPhase = name
	recorder.emitLocked(started, Event{Type: TypePhaseStart, Phase: &PhaseRecord{Name: name}})
	recorder.mutex.Unlock()

	var once sync.Once
	var span time.Duration
	return func() time.Duration {
		once.Do(func() {
			recorder.mutex.Lock()
			defer recorder.mutex.Unlock()
			moment := recorder.now()
			if recorder.openPhase == name {
				recorder.openPhase = ""
			}
			span = moment.Sub(started)
			elapsed := durationMS(span)
			recorder.emitLocked(moment, Event{
				Type:  TypePhaseEnd,
				Phase: &PhaseRecord{Name: name, DurationMS: &elapsed},
			})
		})
		return span
	}
}

func (recorder *Recorder) Stage(name, detail string) func(result string) time.Duration {
	if recorder == nil {
		return func(string) time.Duration { return 0 }
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
	var span time.Duration
	return func(result string) time.Duration {
		once.Do(func() {
			recorder.mutex.Lock()
			defer recorder.mutex.Unlock()
			moment := recorder.now()
			span = moment.Sub(started)
			elapsed := durationMS(span)
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
		return span
	}
}

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

func (recorder *Recorder) Exec(record ExecRecord) int64 {
	if recorder == nil {
		return 0
	}
	return recorder.recordExec(record)
}

//go:noinline
func (recorder *Recorder) recordExec(record ExecRecord) int64 {
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

func (recorder *Recorder) Artifact(kind, path string) {
	if recorder == nil {
		return
	}
	recorder.emit(Event{Type: TypeArtifact, Artifact: &ArtifactRecord{Kind: kind, Path: path}})
}

func (recorder *Recorder) Note(kind, code, detail string) int64 {
	if recorder == nil {
		return 0
	}
	return recorder.emit(Event{Type: TypeNote, Note: &NoteRecord{Kind: kind, Code: code, Detail: detail}})
}

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

func (recorder *Recorder) deliver(event Event) (kept bool) {
	defer func() {
		if recover() != nil {
			kept = false
		}
	}()
	return recorder.sink.Emit(event) == nil
}

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
