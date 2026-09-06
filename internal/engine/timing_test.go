// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

// TestTheTimingIsTheRecordersOwnMeasurement is the reason the recorder's
// closers return a duration.
//
// The report's timing and the recording's `phase-end` and `stage` events
// describe the same spans, and the integration suite asserts they are equal to
// the millisecond. Two independent readings of one clock cannot promise that: a
// phase whose recorder read 88.7 ms and whose engine read 89.1 ms publishes 88
// in one document and 89 in the other, and the pair fails once in a hundred
// runs on a busy machine with nothing wrong. So there is one measurement — the
// recorder's — and the engine stores what the closer returns.
//
// The clock here makes the flake certain instead of rare: every reading is one
// millisecond after the last, so the engine's own pair of readings brackets the
// recorder's and is two milliseconds wider. Before the closers returned
// anything, that difference was exactly what the document carried.
func TestTheTimingIsTheRecordersOwnMeasurement(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 32)
	sink := trace.NewMemorySink(0)
	clock := testkit.NewClock(time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC))
	clock.Tick(time.Millisecond)
	s := &session{events: events, clock: clock.Now}
	s.trace = trace.New(sink, s.now, trace.StartRecord{Kind: trace.StartKindRun})

	s.enterPhase(PhaseBaseline, "measuring the unmutated tests")
	endBuild := s.stage("build", "")
	endBuild(nil)
	endTest := s.stage("test", "run 1/1")
	endTest(nil)
	s.closePhase()
	close(events)
	for range events { //nolint:revive // draining the engine's own stream, which is not what this test reads
	}

	recorded := sink.Events()
	phases := make([]report.PhaseTiming, 0, len(recorded))
	stages := make([]report.StageTiming, 0, len(recorded))
	for _, e := range recorded {
		switch {
		case e.Type == trace.TypePhaseEnd && e.Phase.DurationMS != nil:
			phases = append(phases, report.PhaseTiming{Name: e.Phase.Name, DurationMS: *e.Phase.DurationMS})
		case e.Type == trace.TypeStage && e.Stage.State == trace.StateFinished && e.Stage.DurationMS != nil:
			stages = append(stages, report.StageTiming{
				Phase: e.Stage.Phase, Name: e.Stage.Name,
				DurationMS: *e.Stage.DurationMS, Result: report.StageResult(e.Stage.Result),
			})
		}
	}
	if len(phases) != 1 || len(stages) != 2 {
		t.Fatalf("the recording holds %d phase-ends and %d finished stages, want 1 and 2", len(phases), len(stages))
	}

	if len(s.timing.Phases) != 1 {
		t.Fatalf("the engine timed %d phases, want 1", len(s.timing.Phases))
	}
	if got := s.timing.Phases[0].Duration.Milliseconds(); got != phases[0].DurationMS {
		t.Errorf("the report says the %s phase took %d ms and the recording says %d",
			s.timing.Phases[0].Phase, got, phases[0].DurationMS)
	}
	if len(s.timing.Stages) != len(stages) {
		t.Fatalf("the engine timed %d stages, want %d", len(s.timing.Stages), len(stages))
	}
	for i, stage := range s.timing.Stages {
		if got := stage.Duration.Milliseconds(); got != stages[i].DurationMS {
			t.Errorf("the report says the %s/%s stage took %d ms and the recording says %d",
				stage.Phase, stage.Name, got, stages[i].DurationMS)
		}
	}
}

// TestAnUntracedRunStillTimesItsPhasesAndStages is the other half: the
// recorder's measurement is the truth when there is a recorder, and a run
// without one still has to time itself.
//
// A nil recorder's closers return a zero duration, which is not a measurement,
// and a report of an untraced run that said every phase took 0 ms would be
// worse than one that said nothing.
func TestAnUntracedRunStillTimesItsPhasesAndStages(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 32)
	s := &session{events: events, clock: tickingClock()}

	s.enterPhase(PhaseMutate, "executing the mutants")
	end := s.stage("execute", "3 mutants")
	end(nil)
	s.closePhase()
	close(events)
	for range events { //nolint:revive // as above
	}

	if s.trace != nil {
		t.Fatal("the session has a recorder, so this is not the untraced path")
	}
	if len(s.timing.Phases) != 1 || s.timing.Phases[0].Duration <= 0 {
		t.Errorf("timing.Phases = %+v, want one measured span", s.timing.Phases)
	}
	if len(s.timing.Stages) != 1 || s.timing.Stages[0].Duration <= 0 {
		t.Errorf("timing.Stages = %+v, want one measured span", s.timing.Stages)
	}
}
