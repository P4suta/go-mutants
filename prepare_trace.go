// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"time"

	"github.com/P4suta/go-mutants/trace"
)

type prepareTrace struct {
	emit func(PrepareEvent)
	now  func() time.Time
}

// A phaseError is an error one named preparation phase produced.
//
// It exists because the phase a `prepare-failed` note names has to be the phase
// of the error the *caller* was given, and nothing about the order the events
// arrived in can establish that. Two preparation builds run at once and either
// one's failure cancels the other, so the cancelled build also finishes as a
// failure — later, in the general case — and a note that named the last phase
// to report a failure would name the phase that was merely collateral while
// quoting the error from the phase that actually broke. Tagging the error at
// the point it leaves its phase makes the two halves of that sentence come from
// one place.
//
// It is deliberately invisible to everything but [prepareFailedDetail]: Error
// returns the wrapped message byte for byte, so no diagnostic changes, and
// Unwrap keeps every errors.Is and errors.As a consumer writes working through
// it.
type phaseError struct {
	phase PreparePhase
	err   error
}

func (e *phaseError) Error() string { return e.err.Error() }

func (e *phaseError) Unwrap() error { return e.err }

// inPhase tags an error with the phase that produced it, and leaves nil alone.
func inPhase(phase PreparePhase, err error) error {
	if err == nil {
		return nil
	}
	return &phaseError{phase: phase, err: err}
}

// prepareFailedDetail is what a `prepare-failed` note says: the phase the
// preparation died in and the error, or the error alone when it died outside
// every phase — a refused option, a snapshot a command had already changed.
//
// The error's own text and never the output bytes. A recording is meant to be
// attachable to a bug report, and a failed build's whole console belongs to the
// [BuildError] the caller is holding.
func prepareFailedDetail(err error) string {
	var phased *phaseError
	if !errors.As(err, &phased) {
		return err.Error()
	}
	return string(phased.phase) + ": " + err.Error()
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
	fanOut := func(event PrepareEvent) {
		// Deferred, with its arguments evaluated now. A callback is a
		// consumer's own code and ordinary Go code panics; without this the
		// panic would unwind past the recorder and the recording would be
		// missing the very event the consumer died on — which is the event
		// somebody debugging that panic is looking for.
		defer recorder.Prepare(string(event.Phase), string(event.State), string(event.Result), event.Duration)
		if emit != nil {
			emit(event)
		}
	}
	return prepareTrace{emit: fanOut, now: time.Now}
}

// run times one phase and tags whatever it failed with as that phase's.
//
// The tag travels with the error rather than being remembered here, because the
// probe tree's phases and the binary build run concurrently: what a reader of
// the recording needs is the phase of the failure the caller was handed, and
// only the error itself carries that from the phase it happened in to the one
// place that writes the note. See [phaseError].
func (t prepareTrace) run(phase PreparePhase, work func() error) error {
	span := t.begin(phase)
	err := work()
	t.finish(span.complete(err))
	return inPhase(phase, err)
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
