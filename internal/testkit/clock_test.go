// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"slices"
	"sync"
	"testing"
	"time"
)

var clockEpoch = time.Date(2026, 8, 19, 10, 11, 12, 0, time.UTC)

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
	if d := got[1].Sub(got[0]); d != step {
		t.Errorf("a start/finish pair measured %s, want %s", d, step)
	}
}

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
