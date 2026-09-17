// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import "time"

const etaAlpha = 0.2

type estimator struct {
	mean  time.Duration
	count int
}

func (e *estimator) observe(d time.Duration) {
	if d <= 0 {
		return
	}
	if e.count == 0 {
		e.mean = d
		e.count = 1
		return
	}
	e.mean = time.Duration(etaAlpha*float64(d) + (1-etaAlpha)*float64(e.mean))
	e.count++
}

func (e *estimator) estimate(remaining, workers int) (time.Duration, bool) {
	if e.count == 0 || remaining <= 0 {
		return 0, false
	}
	if workers < 1 {
		workers = 1
	}
	waves := (remaining + workers - 1) / workers
	return e.mean * time.Duration(waves), true
}
