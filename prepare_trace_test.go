// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestPrepareTraceReportsSuccessFailureAndSkip(t *testing.T) {
	const measuredDuration = 17 * time.Millisecond
	clock := []time.Time{time.Unix(0, 0), time.Unix(0, 0).Add(measuredDuration)}
	var events []PrepareEvent
	trace := prepareTrace{
		emit: func(event PrepareEvent) { events = append(events, event) },
		now: func() time.Time {
			instant := clock[0]
			clock = clock[1:]
			return instant
		},
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
	clock = []time.Time{time.Unix(0, 0), time.Unix(0, 0).Add(measuredDuration)}
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
