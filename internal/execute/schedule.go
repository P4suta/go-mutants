// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// retryWorker is the worker number reported for the serial retry pass. Zero is
// honest rather than arbitrary: the retry runs one mutant at a time with
// nothing else in flight, so there is exactly one worker and it is the first.
const retryWorker = 0

// Hooks are the callbacks [Schedule] publishes progress through.
//
// They are plain functions rather than a channel or an interface so that
// internal/engine can forward them straight into its own event stream without
// this package importing it, and so that a caller with nothing to report can
// leave them nil. Both fields are optional.
//
// The contract, which the retry policy forces:
//
//   - Started fires at the beginning of *every attempt*, including the serial
//     retry of a timed-out mutant. A live dashboard has to be able to show that
//     retry happening rather than a worker apparently stuck.
//   - Finished fires *exactly once per mutant*, and only once the outcome is
//     settled. A first-attempt timeout is not a settled outcome, so a mutant
//     that timed out and was retried produces two Started calls and one
//     Finished. A mutant that was never started produces neither.
//
// Both may be called from several worker goroutines at once and must be safe
// for that. Both are called synchronously: a hook that blocks stalls the worker
// that called it, which is exactly how internal/engine's blocking event channel
// applies back-pressure, and is why neither is called while anything is held.
type Hooks struct {
	// Started announces that an attempt at a mutant has begun on a worker.
	Started func(id string, worker int)
	// Finished announces one mutant's settled result. The [MutantResult] is a
	// copy whose attempts do not alias the slice [Schedule] returns.
	Finished func(result MutantResult)
}

// start invokes the Started hook when there is one.
func (h Hooks) start(id string, worker int) {
	if h.Started != nil {
		h.Started(id, worker)
	}
}

// finish invokes the Finished hook when there is one, with a copy the hook may
// keep.
func (h Hooks) finish(result MutantResult) {
	if h.Finished == nil {
		return
	}
	result.Attempts = slices.Clone(result.Attempts)
	h.Finished(result)
}

// A MutantResult is everything one mutant's execution established.
type MutantResult struct {
	// ID is the activation identity that was scheduled.
	ID string
	// Attempts are the passes made over the test binaries, in the order they
	// were made: one for a mutant that settled first time, two for one that
	// timed out and was retried. Both are kept whatever the verdict, because a
	// report has to be able to show that a confirmed timeout was confirmed and
	// that an inconclusive one was not.
	Attempts []Attempt
	// Final is the settled outcome, and is the only field the score is computed
	// from. It is [mutation.OutcomeNotRun] for a mutant the run never reached,
	// or reached and could not finish.
	Final mutation.Outcome
	// KilledBy is the import path of the test binary that detected the mutant —
	// the one that failed, or the one it hung — and is empty otherwise.
	KilledBy string
	// Duration is the wall-clock time this mutant's child processes took,
	// summed over every attempt.
	Duration time.Duration
	// OutputTail is the deciding attempt's retained output.
	OutputTail string
	// Err is set only when Final is [mutation.OutcomeErrored].
	Err error
}

// Schedule runs every mutant against the test binaries and returns one result
// per mutant, in the order the mutants were given.
//
// # The main pass
//
// [Options.Jobs] workers take mutants from a shared queue. Each writes only its
// own slot in the result slice and gets its own temporary directory under
// [Options.ScratchDir], so no two workers share anything mutable and the result
// order does not depend on who finished first.
//
// # The retry pass
//
// A mutant that timed out is *not* settled by the main pass. Timeouts on a
// machine running N test binaries at once say as much about the machine as
// about the mutant, so every timed-out mutant is held back and, once the queue
// has fully drained, retried one at a time with nothing else running. A second
// timeout is a confirmed detection ([mutation.OutcomeTimedOut]); a retry that
// finishes at all — passing or failing — is [mutation.OutcomeInconclusive],
// because mixed evidence is not detection and is not survival either.
//
// The join between the passes is load-bearing twice over: it is what makes the
// retry serial in fact rather than in intention, and it is what makes the
// retry's writes to a slot an earlier worker wrote race-free.
//
// # Cancellation
//
// A cancelled context stops both passes. Whatever was in flight is killed by
// internal/runner, everything not yet settled is [mutation.OutcomeNotRun] — its
// attempts, including a first timeout that never got its retry, are still
// retained — and the full result slice is returned alongside a
// [CodeInterrupted] error wrapping [context.Cause]. Schedule never returns
// while a worker is still running.
func Schedule(
	ctx context.Context,
	opts Options,
	mutants []MutantRun,
	bins []TestBinary,
	hooks Hooks,
) ([]MutantResult, error) {
	results := make([]MutantResult, len(mutants))
	for i, m := range mutants {
		results[i] = MutantResult{ID: m.ID, Final: mutation.OutcomeNotRun}
	}
	if len(mutants) == 0 {
		return results, nil
	}
	if len(bins) == 0 {
		return results, &Error{
			Code: CodeNoTestBinaries,
			Message: "there are no test binaries to measure " + countNoun(len(mutants), "mutant") +
				" against; reporting them all as survived would be a green produced by running nothing",
		}
	}

	// Held back by the main pass for the serial retry. Each entry is written by
	// the one worker that claimed that index and read only after the join.
	pending := make([]bool, len(mutants))

	workers := min(opts.workers(), len(mutants))
	var next atomic.Int64
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerOpts := opts
			workerOpts.ScratchDir = workerScratchDir(opts.ScratchDir, worker)
			for {
				// Checked before claiming rather than after, so a cancelled run
				// stops taking work instead of draining the queue one wasted
				// start at a time.
				if ctx.Err() != nil {
					return
				}
				i := int(next.Add(1)) - 1
				if i >= len(mutants) {
					return
				}

				hooks.start(mutants[i].ID, worker)
				attempt := RunOne(ctx, workerOpts, mutants[i], bins)
				record(&results[i], attempt)

				if attempt.Outcome == mutation.OutcomeTimedOut {
					// Not a result. The retry pass decides.
					pending[i] = true
					continue
				}
				settle(&results[i], attempt)
				hooks.finish(results[i])
			}
		}()
	}
	wg.Wait()

	retryOpts := opts
	retryOpts.ScratchDir = workerScratchDir(opts.ScratchDir, retryWorker)
	for i := range mutants {
		if !pending[i] {
			continue
		}
		if ctx.Err() != nil {
			// One timeout and no chance to reproduce it is not evidence of
			// anything. The attempt stays in the record; the verdict does not
			// pretend the run measured something it did not.
			results[i].Final = mutation.OutcomeNotRun
			hooks.finish(results[i])
			continue
		}

		hooks.start(mutants[i].ID, retryWorker)
		attempt := RunOne(ctx, retryOpts, mutants[i], bins)
		record(&results[i], attempt)
		confirm(&results[i], attempt)
		hooks.finish(results[i])
	}

	if ctx.Err() != nil {
		return results, &Error{
			Code:    CodeInterrupted,
			Message: "the execution phase was interrupted",
			Err:     context.Cause(ctx),
		}
	}
	return results, nil
}

// record appends an attempt to a result and adds its time to the total. It
// never touches the verdict: deciding that is [settle]'s job on the first pass
// and [confirm]'s on the retry.
func record(result *MutantResult, attempt Attempt) {
	result.Attempts = append(result.Attempts, attempt)
	result.Duration += attempt.Duration
}

// settle promotes a first-pass attempt to the mutant's verdict. It is called
// for every outcome except a timeout, which the retry pass owns.
func settle(result *MutantResult, attempt Attempt) {
	result.Final = attempt.Outcome
	result.KilledBy = attempt.KilledBy
	result.OutputTail = attempt.OutputTail
	result.Err = attempt.Err
}

// confirm applies the timeout retry rule to a mutant that has already timed out
// once.
//
// The two interesting cases are the whole policy. A second timeout is
// detection: a mutant that hangs a machine with nothing else on it has changed
// behaviour the tests noticed. A retry that *finished* is inconclusive whether
// it passed or failed — the two attempts disagree, and a run that reports
// disagreement as a kill inflates the score exactly where it is least entitled
// to.
func confirm(result *MutantResult, attempt Attempt) {
	switch attempt.Outcome {
	case mutation.OutcomeTimedOut:
		result.Final = mutation.OutcomeTimedOut
		result.KilledBy = attempt.KilledBy
		result.OutputTail = attempt.OutputTail
	case mutation.OutcomeKilled, mutation.OutcomeSurvived:
		result.Final = mutation.OutcomeInconclusive
		result.OutputTail = attempt.OutputTail
	default:
		// Errored or not run: the retry established nothing about the mutant, so
		// its own outcome stands rather than the timeout being promoted.
		settle(result, attempt)
	}
}

// workerScratchDir names one worker's temporary directory under the run's
// scratch parent. An empty parent stays empty, which [RunOne] reads as "leave
// the inherited temporary directory alone".
func workerScratchDir(parent string, worker int) string {
	if parent == "" {
		return ""
	}
	return filepath.Join(parent, "w"+strconv.Itoa(worker))
}

// countNoun renders "1 mutant" or "3 mutants".
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
