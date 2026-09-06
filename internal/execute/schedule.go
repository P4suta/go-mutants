// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/trace"
)

// retryWorker is the worker number reported for the serial retry pass. Zero is
// honest rather than arbitrary: the retry runs one mutant at a time with
// nothing else in flight, so there is exactly one worker and it is the first.
const retryWorker = 0

// The attempt numbers the two passes record. They are the passes themselves
// rather than a counter, because that is what makes an attempt readable
// without its neighbours: attempt 1 is the concurrent pass, on a machine with
// everything else running, and attempt 2 is the serial retry on a quiet one —
// which is the whole reason a mutant that timed out once is not yet a
// detection.
const (
	mainAttempt  = 1
	retryAttempt = 2
)

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
//
// That error names one of the test binaries the signal cut off, with what it
// had printed, because "what was still running" is the first question a Ctrl-C
// raises and this is the last layer that can answer it: what the attempts know
// is reduced to a count of attempts by the time a report is built. See [cutOff]
// for which one it names and why that is not a race.
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
				opts.Trace.MutantExec(AttemptRecord(mutants[i], attempt, mainAttempt, worker))

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
	// Timed once it has something to do. The retry is the one part of an
	// execution phase that is deliberately serial, so on a queue full of
	// timeouts it is where the wall-clock time of a run goes — and a stage in
	// every recording of every run, most of them empty, is a line a reader
	// learns to skip past the one time it mattered.
	retried := heldBack(pending)
	retryStage := func(string) {}
	if retried > 0 {
		retryStage = opts.Trace.Stage("retry", countNoun(retried, "timeout"))
	}
	// Whether the pass left a mutant unretried. Two different things reach it:
	// a mutant the pass never started, and one whose retry a cancellation cut
	// off. The second is invisible in the control flow below — the context was
	// clear when the attempt began and [RunOne] reports a killed child as
	// not-run rather than as an error — so it is read off the attempt.
	unretried := false
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
			unretried = true
			continue
		}

		hooks.start(mutants[i].ID, retryWorker)
		attempt := RunOne(ctx, retryOpts, mutants[i], bins)
		record(&results[i], attempt)
		opts.Trace.MutantExec(AttemptRecord(mutants[i], attempt, retryAttempt, retryWorker))
		if attempt.Outcome == mutation.OutcomeNotRun {
			// Started and killed. Nothing else produces this outcome here: a
			// retry that ran is killed, survived or timed out, and a failure of
			// go-mutants itself is errored.
			unretried = true
		}
		confirm(&results[i], attempt)
		hooks.finish(results[i])
	}
	// Succeeded says the pass ran to its end, which it does whether or not the
	// retries reproduced anything: what each one decided is the attempt's own
	// event, and a stage that reported a mutant's verdict would be a second,
	// coarser answer to a question already answered. A pass that left a mutant
	// unretried did not do the one thing it exists for — that mutant's timeout
	// was never reproduced and its verdict stays not-run — and says so.
	retryStage(stageResult(unretried))

	if ctx.Err() != nil {
		interrupted := &Error{
			Code:    CodeInterrupted,
			Message: "the execution phase was interrupted",
			Err:     context.Cause(ctx),
		}
		// What was still running is the one thing a reader of a Ctrl-C asks
		// for, and this is the last place that knows it: the attempts hold it,
		// and a report keeps the *number* of attempts rather than the attempts
		// themselves, so an error that did not carry it up would be the end of
		// the trail. The message is untouched — this puts a command under it,
		// it does not restate the failure.
		if cut := cutOff(results); cut != nil {
			interrupted.Invocation = cut.Invocation
			interrupted.Output = cut.Output
		}
		return results, interrupted
	}
	return results, nil
}

// AttemptRecord is one attempt as the recording holds it.
//
// It is built whether or not there is a recorder, because a nil recorder is the
// disabled trace and the branch that skipped this would be a second path
// through the scheduler for a verdict to come to depend on. What it costs is
// one struct per attempt, against a child process.
//
// It is exported because [Schedule] is not the only caller that runs one
// mutant: the library API's session runs exactly one at a time and records the
// same summary for it. One builder rather than two is what keeps a recording
// made through the API and a recording made by a run describing an attempt the
// same way, field for field.
//
// The outcome is spelled with [mutation.Outcome]'s own name, which is the one
// the report and the cache already use: a trace and a report saying different
// words about one mutant would be two vocabularies to reconcile for no gain.
func AttemptRecord(m MutantRun, attempt Attempt, number, worker int) trace.MutantRecord {
	record := trace.MutantRecord{
		ID:        m.ID,
		DisplayID: m.DisplayID,
		Attempt:   number,
		Worker:    worker,
		Package:   m.Package,
		TimeoutMS: m.Timeout.Milliseconds(),
		// Cloned on the way in, for the reason [trace.Recorder.Exec] clones an
		// argument vector: the record is handed to a sink that may keep it, and
		// a caller may reuse a mutant's arguments for the retry.
		Binaries:   slices.Clone(attempt.Binaries),
		Args:       slices.Clone(m.Args),
		ExecSeqs:   slices.Clone(attempt.ExecSeqs),
		Outcome:    attempt.Outcome.String(),
		KilledBy:   attempt.KilledBy,
		DurationMS: attempt.Duration.Milliseconds(),
		// The deciding binary's tail. A sink writing to disk keeps it, and the
		// bounded ring drops it, so the recording of a long run stays bounded
		// while the one on disk stays readable.
		OutputTail: attempt.OutputTail,
	}
	if attempt.Err != nil {
		record.Error = attempt.Err.Error()
	}
	return record
}

// stageResult is what the retry pass came to: failed when it left a mutant
// unretried, succeeded when it reproduced every timeout it was given.
func stageResult(unretried bool) string {
	if unretried {
		return trace.ResultFailed
	}
	return trace.ResultSucceeded
}

// heldBack is how many mutants the main pass left for the retry.
func heldBack(pending []bool) int {
	n := 0
	for _, held := range pending {
		if held {
			n++
		}
	}
	return n
}

// cutOff returns the failure of an attempt the cancellation ended, or nil when
// nothing was in flight — a run stopped between mutants, or one where every
// worker had already finished.
//
// With several workers there were several, and this names the first in the
// mutants' own order rather than whichever goroutine happened to lose the race.
// The determinism is the point: two interrupted runs of the same queue print
// the same command, and a diagnostic naming a different binary each time would
// read as a fact about that binary rather than about the signal.
func cutOff(results []MutantResult) *Error {
	for _, result := range results {
		for _, attempt := range result.Attempts {
			var failure *Error
			if errors.As(attempt.Err, &failure) &&
				failure.Code == CodeInterrupted && failure.Invocation != nil {
				return failure
			}
		}
	}
	return nil
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
//
// Only the verdict's own error is promoted with it. An attempt a cancellation
// cut off carries [CodeInterrupted] naming the binary that was still running —
// see [Attempt.Err] — and that belongs to the attempt's record rather than to a
// mutant whose verdict is simply "not run": [MutantResult.Err] means this
// mutant errored, the interruption itself is what [Schedule] returns, and
// letting the two blur would make "did this mutant error?" answerable two
// different ways. Nothing is lost by leaving it where it is, because [record]
// keeps every attempt.
func settle(result *MutantResult, attempt Attempt) {
	result.Final = attempt.Outcome
	result.KilledBy = attempt.KilledBy
	result.OutputTail = attempt.OutputTail
	result.Err = nil
	if attempt.Outcome == mutation.OutcomeErrored {
		result.Err = attempt.Err
	}
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
