// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import "time"

type prepareTrace struct {
	emit func(PrepareEvent)
	now  func() time.Time
}

func newPrepareTrace(emit func(PrepareEvent)) prepareTrace {
	return prepareTrace{emit: emit, now: time.Now}
}

func (t prepareTrace) run(phase PreparePhase, work func() error) error {
	if t.emit == nil {
		return work()
	}
	t.emit(PrepareEvent{Phase: phase, State: PrepareEventStarted})
	started := t.now()
	err := work()
	duration := t.now().Sub(started)
	result := PreparePhaseSucceeded
	if err != nil {
		result = PreparePhaseFailed
	}
	t.emit(PrepareEvent{
		Phase:    phase,
		State:    PrepareEventFinished,
		Result:   result,
		Duration: duration,
	})
	return err
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
