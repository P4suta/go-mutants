// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"slices"
	"sync"
	"testing"
	"time"
)

// clockEpoch is an arbitrary instant every test in this file starts from. It is
// a fixed date rather than time.Now so that a failure reads the same on every
// machine and in every year.
var clockEpoch = time.Date(2026, 8, 19, 10, 11, 12, 0, time.UTC)

// TestClockStandsStillUntilItIsMoved is the default, and the one the dashboard's
// tests depend on: a frame drawn twice without an Advance in between shows the
// same elapsed time, so an assertion about the layout is about the layout.
func TestClockStandsStillUntilItIsMoved(t *testing.T) {
	t.Parallel()

	c := NewClock(clockEpoch)
	if got := c.Now(); !got.Equal(clockEpoch) {
		t.Errorf("Now = %s, want %s", got, clockEpoch)
	}
	if got := c.Now(); !got.Equal(clockEpoch) {
		t.Errorf("a second read moved the clock to %s", got)
	}
	c.Advance(90 * time.Second)
	if got, want := c.Now(), clockEpoch.Add(90*time.Second); !got.Equal(want) {
		t.Errorf("after Advance, Now = %s, want %s", got, want)
	}
	c.Set(clockEpoch)
	if got := c.Now(); !got.Equal(clockEpoch) {
		t.Errorf("after Set, Now = %s, want %s", got, clockEpoch)
	}
}

// TestClockTickAdvancesOnEveryRead is the shape a measured span needs.
//
// internal/report's prepare trace reads the clock once at the start of a phase
// and once at its end, and the assertion is about the difference. Writing that
// as a slice of two instants — which is what the suite did — makes the test
// depend on how many times the code under test happens to read the clock, so a
// second phase means a second slice and a refactor means a rewrite. A tick says
// the thing the test actually means: every span is this long.
func TestClockTickAdvancesOnEveryRead(t *testing.T) {
	t.Parallel()

	const step = 17 * time.Millisecond
	c := NewClock(clockEpoch)
	c.Tick(step)

	var got []time.Time
	for range 4 {
		got = append(got, c.Now())
	}
	want := []time.Time{
		clockEpoch,
		clockEpoch.Add(step),
		clockEpoch.Add(2 * step),
		clockEpoch.Add(3 * step),
	}
	if !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Fatalf("Now returned %v, want %v", got, want)
	}
	// The instant is the one before the advance, so two consecutive reads
	// measure exactly one tick — which is what a start/finish pair is.
	if d := got[1].Sub(got[0]); d != step {
		t.Errorf("a start/finish pair measured %s, want %s", d, step)
	}
}

// TestClockSequenceReturnsEachInstantOnceThenHolds is the other shape: a test
// that names the instants because the *gaps* are what it is about — a span that
// opens, is interrupted by a shorter one, and closes after it.
//
// The last instant repeats rather than the slice running out, because running
// out is a panic in a goroutine the test does not control, and "the code read
// the clock once more than I scripted" should be a wrong duration in a readable
// assertion rather than an index out of range.
func TestClockSequenceReturnsEachInstantOnceThenHolds(t *testing.T) {
	t.Parallel()

	first := clockEpoch
	second := clockEpoch.Add(time.Millisecond)
	third := clockEpoch.Add(3 * time.Millisecond)

	c := NewClock(time.Time{})
	c.Sequence(first, second, third)

	want := []time.Time{first, second, third, third, third}
	var got []time.Time
	for range len(want) {
		got = append(got, c.Now())
	}
	if !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Errorf("Now returned %v, want %v", got, want)
	}
}

// TestClockSequenceGivesWayToTheInstantItWasSetTo keeps the two modes from
// being a mystery when a test uses both: whatever was scripted, Set is the
// clock's new state and reads carry on from there.
func TestClockSequenceGivesWayToTheInstantItWasSetTo(t *testing.T) {
	t.Parallel()

	c := NewClock(clockEpoch)
	c.Sequence(clockEpoch, clockEpoch.Add(time.Second))
	_ = c.Now()
	c.Set(clockEpoch.Add(time.Hour))

	if got, want := c.Now(), clockEpoch.Add(time.Hour); !got.Equal(want) {
		t.Errorf("Now after Set = %s, want %s", got, want)
	}
}

// TestClockIsSafeUnderConcurrentReads is why the state is behind a mutex.
//
// The dashboard reads its clock from the bubbletea update goroutine while the
// test writes it, and internal/runner supervises children on goroutines of
// their own. An unguarded time source in a helper is a data race the `-race`
// job reports in somebody else's package, days later.
func TestClockIsSafeUnderConcurrentReads(t *testing.T) {
	t.Parallel()

	c := NewClock(clockEpoch)
	c.Tick(time.Millisecond)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 32 {
				_ = c.Now()
				c.Advance(time.Millisecond)
			}
		}()
	}
	wg.Wait()
}
