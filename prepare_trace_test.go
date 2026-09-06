// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
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

// TestPrepareOptionsTraceCallbackStillReceivesEveryPhaseEvent is the fan-out.
//
// A preparation now has two audiences and they are not the same audience. The
// callback is [PrepareOptions.Trace]: a consumer driving a progress display off
// it was there first, and its contract does not move — one start and one finish
// per phase, in phase order, synchronously, with the same durations. The
// recorder is where the same timeline reaches somebody joining two recordings
// afterwards, and it has to see every one of those events too, because a
// timeline missing the phase a preparation died in is exactly the timeline
// somebody is reading.
//
// The nine phases are driven here rather than by a real preparation, and one of
// each shape: a phase that succeeded, one that failed, and one that was skipped
// — a skip being a started/finished pair with a zero duration, which is what
// lets a reader tell an intentional omission from an event that was lost.
func TestPrepareOptionsTraceCallbackStillReceivesEveryPhaseEvent(t *testing.T) {
	const measuredDuration = 3 * time.Millisecond
	clock := testkit.NewClock(time.Unix(0, 0))
	clock.Tick(measuredDuration)

	var callback []PrepareEvent
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, clock.Now, trace.StartRecord{Kind: trace.StartKindWorkspace})
	phases := newPrepareTrace(func(event PrepareEvent) { callback = append(callback, event) }, recorder)
	phases.now = clock.Now

	failure := errors.New("phase failed")
	for i, phase := range KnownPreparePhases() {
		switch i % 3 {
		case 0:
			if err := phases.run(phase, func() error { return nil }); err != nil {
				t.Fatalf("%s: %v", phase, err)
			}
		case 1:
			if err := phases.run(phase, func() error { return failure }); !errors.Is(err, failure) {
				t.Fatalf("%s = %v, want %v", phase, err, failure)
			}
		default:
			phases.skip(phase)
		}
	}

	// Every phase this build emits, a start and a finish each: the number a
	// consumer's own timeline is built out of, and the number the recording has
	// to agree with. Derived from the list rather than written down, so that a
	// phase added to the engine moves both sides of the comparison at once.
	wantEvents := 2 * len(KnownPreparePhases())
	if len(callback) != wantEvents {
		t.Fatalf("the callback saw %d events, want %d: %+v", len(callback), wantEvents, callback)
	}

	var recorded []trace.Event
	for _, event := range sink.Events() {
		if event.Type == trace.TypePrepare {
			recorded = append(recorded, event)
		}
	}
	if len(recorded) != wantEvents {
		t.Fatalf("the recorder saw %d prepare events, want %d", len(recorded), wantEvents)
	}
	for i, event := range callback {
		record := recorded[i].Prepare
		if record.Phase != string(event.Phase) || record.State != string(event.State) ||
			record.Result != string(event.Result) {
			t.Errorf("event %d: the recording says %+v and the callback %+v", i, *record, event)
			continue
		}
		if event.State == PrepareEventStarted {
			if record.DurationMS != nil {
				t.Errorf("event %d: a started %s carries a duration", i, event.Phase)
			}
			continue
		}
		if record.DurationMS == nil {
			t.Errorf("event %d: a finished %s carries none, and a reader cannot tell that"+
				" from an event that was lost", i, event.Phase)
			continue
		}
		if got, want := *record.DurationMS, event.Duration.Milliseconds(); got != want {
			t.Errorf("event %d: the recording timed %s at %d ms and the callback at %d", i, event.Phase, got, want)
		}
	}
}

// TestPrepareTraceRecordsWithoutACallback is the other half of the fan-out: a
// caller that asked for no phase events still records them.
//
// It is the case a consumer of the recording is in — nothing is watching the
// preparation live, and the whole account of it is read afterwards — so a
// fan-out that only fired when somebody had supplied a callback would produce
// recordings whose contents depended on an option that has nothing to do with
// them.
func TestPrepareTraceRecordsWithoutACallback(t *testing.T) {
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, time.Now, trace.StartRecord{Kind: trace.StartKindWorkspace})
	phases := newPrepareTrace(nil, recorder)
	phases.skip(PreparePhaseVerification)

	var recorded []*trace.PrepareRecord
	for _, event := range sink.Events() {
		if event.Type == trace.TypePrepare {
			recorded = append(recorded, event.Prepare)
		}
	}
	if len(recorded) != 2 {
		t.Fatalf("a skipped phase recorded %d events, want a started/finished pair", len(recorded))
	}
	if recorded[1].Result != string(PreparePhaseSkipped) {
		t.Errorf("the finish says %q, want %q", recorded[1].Result, PreparePhaseSkipped)
	}
	if recorded[1].DurationMS == nil || *recorded[1].DurationMS != 0 {
		t.Errorf("the finish of a skipped phase carries %v, want a zero duration", recorded[1].DurationMS)
	}
}

// TestAPanickingPrepareCallbackDoesNotLoseTheRecordersEvent is why the fan-out
// records with a defer.
//
// [PrepareOptions.Trace] is a consumer's own code, and ordinary Go code panics.
// A fan-out that recorded after calling it would lose exactly one event to such
// a panic: the one the consumer died on, which is the event somebody debugging
// that panic opens the recording to find. Recording it first would be the
// mirror-image bug — the callback's contract is that it is called
// synchronously, and a recorder that ran ahead of it would reorder the two for
// every consumer that reads both.
func TestAPanickingPrepareCallbackDoesNotLoseTheRecordersEvent(t *testing.T) {
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, time.Now, trace.StartRecord{Kind: trace.StartKindWorkspace})
	seen := 0
	phases := newPrepareTrace(func(PrepareEvent) {
		seen++
		if seen == 2 {
			panic("the consumer's phase callback panicked")
		}
	}, recorder)

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("the callback's panic did not reach the caller; a fan-out that swallowed" +
					" it would hide a consumer's bug inside the engine")
			}
		}()
		_ = phases.run(PreparePhaseDiscovery, func() error { return nil })
	}()

	var recorded []*trace.PrepareRecord
	for _, event := range sink.Events() {
		if event.Type == trace.TypePrepare {
			recorded = append(recorded, event.Prepare)
		}
	}
	if len(recorded) != 2 {
		t.Fatalf("the recording holds %d prepare events, want the start and the finish the"+
			" callback was given — the second of them is the one it died on", len(recorded))
	}
	if recorded[1].State != string(PrepareEventFinished) {
		t.Errorf("the second record is %+v, want the finish", *recorded[1])
	}
}
