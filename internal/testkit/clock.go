// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"sync"
	"time"
)

// A Clock is a time source a test moves by hand.
//
// Every place go-mutants measures a duration takes its clock as a function —
// the dashboard's elapsed time, the prepare trace's per-phase spans, the
// runner's step timings — precisely so that a test can decide what time it is.
// Three suites then wrote their own: a struct with a settable field, a slice of
// instants popped one per read, and a second slice for the second half of the
// same test. The slices are the reason this exists. They make a test depend on
// how many times the code under test happens to read the clock, so adding a
// phase means adding instants, an extra read is an index-out-of-range panic on
// a goroutine nobody owns, and the assertion the test is actually making —
// "this span lasted seventeen milliseconds" — is nowhere in the source.
//
// So there is one clock with the three behaviours those tests wanted:
//
//   - stopped, moved by [Clock.Advance] and [Clock.Set]. A frame drawn twice
//     shows the same time.
//   - ticking, by [Clock.Tick]: each read returns the current instant and then
//     advances, so any start/finish pair measures exactly one tick.
//   - scripted, by [Clock.Sequence]: each read returns the next instant, and the
//     last one repeats for ever rather than running out.
//
// Every method is safe to call from any goroutine: production reads these
// clocks from the goroutines it supervises children on, and an unguarded time
// source in a helper is a data race reported by the `-race` job in somebody
// else's package days later.
type Clock struct {
	mu       sync.Mutex
	at       time.Time
	tick     time.Duration
	sequence []time.Time
}

// NewClock returns a clock stopped at an instant.
func NewClock(at time.Time) *Clock { return &Clock{at: at} }

// Now returns what time it is, and is the function a caller injects:
//
//	options{now: clock.Now}
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sequence) > 0 {
		instant := c.sequence[0]
		// The last instant is left in place rather than consumed. A script that
		// ran out would panic inside whatever goroutine read the clock, which
		// turns "the code read the clock once more than I scripted" into a
		// stack trace instead of a duration that is visibly wrong.
		if len(c.sequence) > 1 {
			c.sequence = c.sequence[1:]
		}
		return instant
	}
	instant := c.at
	c.at = c.at.Add(c.tick)
	return instant
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// Set moves the clock to an instant, and abandons any script [Clock.Sequence]
// left, so that a test that scripts one span and then takes the clock back by
// hand reads from where it put it.
func (c *Clock) Set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = at
	c.sequence = nil
}

// Tick makes every read advance the clock by d after returning the current
// instant, so that two consecutive reads are exactly d apart.
//
// That is the shape a measured span wants: `start := now(); work(); finish :=
// now()` reports d whatever else the code does, and it goes on reporting d for
// the second span and the tenth without a test having to script them.
func (c *Clock) Tick(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick = d
}

// Sequence scripts the instants the next reads return, in order, with the last
// one repeating.
//
// It is for the tests a tick cannot express: the ones whose subject is the
// *gaps* — a span that opens, is interrupted by a shorter one, and closes after
// it — where each instant is named in the assertion too.
func (c *Clock) Sequence(times ...time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sequence = append([]time.Time(nil), times...)
}
