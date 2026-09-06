// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import "time"

type prepareTrace struct {
	emit func(PrepareEvent)
	now  func() time.Time
}

type preparePhaseSpan struct {
	trace    prepareTrace
	phase    PreparePhase
	started  time.Time
	observed bool
}

func newPrepareTrace(emit func(PrepareEvent)) prepareTrace {
	return prepareTrace{emit: emit, now: time.Now}
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
