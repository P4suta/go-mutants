// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"sync"
	"time"

	"github.com/P4suta/go-mutants/trace"
)

type prepareTrace struct {
	emit func(PrepareEvent)
	now  func() time.Time
	// failed is the phase a preparation died in, shared by every copy of this
	// value: the probe tree's phases are driven through a copy carried in
	// [probeTreeOptions], and the one place that reads this is the deferred
	// note at the end of Prepare.
	failed *failedPhase
}

// A failedPhase is the last phase that finished with a failure.
//
// The *last* rather than the first, because that is the one the returned error
// is about: the preparation stops at the phase that failed, and where two
// concurrent builds both fail the error a caller receives is the one that
// finished the pair. It is a pointer so that every copy of a [prepareTrace]
// writes to one value, and it is guarded because the main build and the probe
// build report from two goroutines.
type failedPhase struct {
	mutex sync.Mutex
	phase PreparePhase
}

func (f *failedPhase) observe(event PrepareEvent) {
	if f == nil || event.State != PrepareEventFinished || event.Result != PreparePhaseFailed {
		return
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.phase = event.Phase
}

// detail is what a `prepare-failed` note says: the phase the preparation died
// in and the error, or the error alone when it died outside every phase — a
// refused option, a snapshot a command had already changed.
//
// The error's own text and never the output bytes. A recording is meant to be
// attachable to a bug report, and a failed build's whole console belongs to the
// [BuildError] the caller is holding.
func (f *failedPhase) detail(err error) string {
	if f == nil {
		return err.Error()
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.phase == "" {
		return err.Error()
	}
	return string(f.phase) + ": " + err.Error()
}

type preparePhaseSpan struct {
	trace    prepareTrace
	phase    PreparePhase
	started  time.Time
	observed bool
}

// newPrepareTrace fans one preparation's phase events out to the caller's
// callback and to the workspace's recorder.
//
// Both, always, and in that order. The callback is [PrepareOptions.Trace] and
// its contract does not move: it sees exactly the events it saw before this
// function had a second destination, one start and one finish per phase, in the
// same order, synchronously. The recorder is where the same timeline reaches a
// consumer that is joining two recordings rather than watching one preparation,
// and it is written second so that a sink which blocks cannot delay the
// callback a caller is driving a progress display from.
//
// A workspace always has a recorder, so the fan-out is never empty and the
// clock is always read: a preparation that recorded nothing at all is not a
// state this API has.
func newPrepareTrace(emit func(PrepareEvent), recorder *trace.Recorder) prepareTrace {
	failed := &failedPhase{}
	fanOut := func(event PrepareEvent) {
		// Deferred, with its arguments evaluated now. A callback is a
		// consumer's own code and ordinary Go code panics; without this the
		// panic would unwind past the recorder and the recording would be
		// missing the very event the consumer died on — which is the event
		// somebody debugging that panic is looking for.
		defer recorder.Prepare(string(event.Phase), string(event.State), string(event.Result), event.Duration)
		failed.observe(event)
		if emit != nil {
			emit(event)
		}
	}
	return prepareTrace{emit: fanOut, now: time.Now, failed: failed}
}

func (t prepareTrace) run(phase PreparePhase, work func() error) error {
	span := t.begin(phase)
	err := work()
	t.finish(span.complete(err))
	return err
}

func (t prepareTrace) begin(phase PreparePhase) preparePhaseSpan {
	span := preparePhaseSpan{trace: t, phase: phase}
	if t.emit == nil {
		return span
	}
	t.emit(PrepareEvent{Phase: phase, State: PrepareEventStarted})
	span.started = t.now()
	span.observed = true
	return span
}

func (s preparePhaseSpan) complete(err error) PrepareEvent {
	if !s.observed {
		return PrepareEvent{}
	}
	result := PreparePhaseSucceeded
	if err != nil {
		result = PreparePhaseFailed
	}
	return PrepareEvent{
		Phase:    s.phase,
		State:    PrepareEventFinished,
		Result:   result,
		Duration: s.trace.now().Sub(s.started),
	}
}

func (t prepareTrace) finish(event PrepareEvent) {
	if t.emit != nil {
		t.emit(event)
	}
}

func (t prepareTrace) skip(phase PreparePhase) {
	if t.emit == nil {
		return
	}
	t.emit(PrepareEvent{Phase: phase, State: PrepareEventStarted})
	t.emit(PrepareEvent{
		Phase:  phase,
		State:  PrepareEventFinished,
		Result: PreparePhaseSkipped,
	})
}
