// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package tui

import "time"

// etaAlpha is the weight one fresh observation carries in the moving average.
//
// A fifth is a deliberate compromise. A larger weight makes the estimate chase
// every slow mutant and produces a number that jumps while a user is reading
// it; a smaller one keeps quoting the fast mutants of a package whose tests
// have since got slower. Five observations to move most of the way there is
// about the responsiveness a person watching a progress line expects.
const etaAlpha = 0.2

// An estimator predicts how much of a run is left, from how long the mutants
// that have settled took.
//
// It averages durations rather than counting completions per second because a
// completion rate is a fact about the machine as well as the run — a rate
// measured while three workers were still starting up under-reports the
// throughput of the other five for the rest of the run. A per-mutant mean,
// divided by the number of workers, is a statement about the work itself.
//
// The mean is exponentially weighted so that it follows a run that changes
// character partway through, which mutation runs reliably do: one package's
// tests take ten milliseconds and the next one's take four seconds.
type estimator struct {
	mean  time.Duration
	count int
}

// observe folds one settled mutant's duration into the mean.
//
// A non-positive duration is ignored rather than averaged in. It means the
// mutant was never actually measured — a not-run left over from a cancelled
// schedule reports zero — and treating "we did not run this" as "this took no
// time" would collapse the estimate to nothing exactly when a user is watching
// a cancelled run wind down.
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

// estimate returns how long the remaining mutants should take on workers
// workers, and whether an estimate exists at all.
//
// It is undefined until something has been measured, and the second result
// says so rather than a zero saying it. Zero is a lie a progress line tells
// well: "eta 00:00" on a run that has not started reads as "about to finish".
//
// The arithmetic is the obvious one — a full worker-width wave costs one mean
// duration, and there are ceil(remaining/workers) waves left — which is exactly
// right when every mutant costs the same and optimistic at the tail, where the
// last wave is partly idle. No attempt is made to model the tail: an ETA is a
// rough answer to "have I time for coffee", and a more elaborate one would
// still be wrong for the reason all of them are, which is that the next package
// may be slower than every package so far.
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
