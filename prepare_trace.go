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

type phaseError struct {
	phase PreparePhase
	err   error
}

func (e *phaseError) Error() string { return e.err.Error() }

func (e *phaseError) Unwrap() error { return e.err }

func inPhase(phase PreparePhase, err error) error {
	if err == nil {
		return nil
	}
	return &phaseError{phase: phase, err: err}
}

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

func newPrepareTrace(emit func(PrepareEvent), recorder *trace.Recorder) prepareTrace {
	fanOut := func(event PrepareEvent) {
		defer recorder.Prepare(string(event.Phase), string(event.State), string(event.Result), event.Duration)
		if emit != nil {
			emit(event)
		}
	}
	return prepareTrace{emit: fanOut, now: time.Now}
}

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
