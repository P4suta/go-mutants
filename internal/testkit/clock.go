// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"sync"
	"time"
)

type Clock struct {
	mu       sync.Mutex
	at       time.Time
	tick     time.Duration
	sequence []time.Time
}

func NewClock(at time.Time) *Clock { return &Clock{at: at} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sequence) > 0 {
		instant := c.sequence[0]
		if len(c.sequence) > 1 {
			c.sequence = c.sequence[1:]
		}
		return instant
	}
	instant := c.at
	c.at = c.at.Add(c.tick)
	return instant
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func (c *Clock) Set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = at
	c.sequence = nil
}

func (c *Clock) Tick(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tick = d
}

func (c *Clock) Sequence(times ...time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sequence = append([]time.Time(nil), times...)
}
