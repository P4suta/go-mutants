// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/testkit"
)

func TestPrepareTraceReportsSuccessFailureAndSkip(t *testing.T) {
	const measuredDuration = 17 * time.Millisecond
	// A ticking clock rather than a scripted pair of instants: what this test is
	// about is that a span lasts exactly one measured duration, and it says so
	// once for every phase below instead of once per clock read.
	clock := testkit.NewClock(time.Unix(0, 0))
	clock.Tick(measuredDuration)
	var events []PrepareEvent
	trace := prepareTrace{
		emit: func(event PrepareEvent) { events = append(events, event) },
		now:  clock.Now,
	}
	if err := trace.run(PreparePhaseDiscovery, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	want := []PrepareEvent{
		{Phase: PreparePhaseDiscovery, State: PrepareEventStarted},
		{
			Phase:    PreparePhaseDiscovery,
			State:    PrepareEventFinished,
			Result:   PreparePhaseSucceeded,
			Duration: measuredDuration,
		},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("success events = %+v, want %+v", events, want)
	}

	failure := errors.New("phase failed")
	events = nil
	if err := trace.run(PreparePhaseMainValidation, func() error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("failure = %v, want %v", err, failure)
	}
	if len(events) != 2 || events[1].Result != PreparePhaseFailed || events[1].Duration != measuredDuration {
		t.Fatalf("failure events = %+v", events)
	}

	events = nil
	trace.skip(PreparePhaseVerification)
	want = []PrepareEvent{
		{Phase: PreparePhaseVerification, State: PrepareEventStarted},
		{Phase: PreparePhaseVerification, State: PrepareEventFinished, Result: PreparePhaseSkipped},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("skip events = %+v, want %+v", events, want)
	}
}

func TestPrepareTraceWithoutObserverDoesNotReadTheClock(t *testing.T) {
	trace := prepareTrace{now: func() time.Time { panic("clock read") }}
	called := false
	if err := trace.run(PreparePhaseDiscovery, func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("phase work was not called")
	}
	trace.skip(PreparePhaseVerification)
}

func TestPreparePhaseSpanCanFinishAfterAnotherPhase(t *testing.T) {
	mainStarted := time.Time{}
	probeStarted := mainStarted.Add(time.Millisecond)
	probeFinished := probeStarted.Add(time.Millisecond)
	mainFinished := probeFinished.Add(time.Millisecond)
	// A script rather than a tick: this test's subject is the *gaps*, so each
	// instant is named in the assertion below and the clock hands them out in
	// the order the code reads them.
	clock := testkit.NewClock(time.Time{})
	clock.Sequence(mainStarted, probeStarted, probeFinished, mainFinished)
	var events []PrepareEvent
	trace := prepareTrace{
		emit: func(event PrepareEvent) {
			events = append(events, event)
		},
		now: clock.Now,
	}
	main := trace.begin(PreparePhaseBinaryBuild)
	if err := trace.run(PreparePhaseProbeValidation, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	trace.finish(main.complete(nil))
	want := []PrepareEvent{
		{Phase: PreparePhaseBinaryBuild, State: PrepareEventStarted},
		{Phase: PreparePhaseProbeValidation, State: PrepareEventStarted},
		{
			Phase:    PreparePhaseProbeValidation,
			State:    PrepareEventFinished,
			Result:   PreparePhaseSucceeded,
			Duration: probeFinished.Sub(probeStarted),
		},
		{
			Phase:    PreparePhaseBinaryBuild,
			State:    PrepareEventFinished,
			Result:   PreparePhaseSucceeded,
			Duration: mainFinished.Sub(mainStarted),
		},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %+v, want %+v", events, want)
	}
}
