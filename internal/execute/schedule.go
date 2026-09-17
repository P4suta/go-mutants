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

const retryWorker = 0

const (
	mainAttempt  = 1
	retryAttempt = 2
)

type Hooks struct {
	Started  func(id string, worker int)
	Finished func(result MutantResult)
}

func (h Hooks) start(id string, worker int) {
	if h.Started != nil {
		h.Started(id, worker)
	}
}

func (h Hooks) finish(result MutantResult) {
	if h.Finished == nil {
		return
	}
	result.Attempts = slices.Clone(result.Attempts)
	h.Finished(result)
}

type MutantResult struct {
	ID         string
	Attempts   []Attempt
	Final      mutation.Outcome
	KilledBy   string
	Duration   time.Duration
	OutputTail string
	Err        error
}

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

	pending := make([]bool, len(mutants))

	var failed atomic.Pointer[error]

	workers := min(opts.workers(), len(mutants))
	var next atomic.Int64
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerOpts := opts
			workerOpts.ScratchDir = workerScratchDir(opts.ScratchDir, worker)
			workerOpts.Tree = treeFor(opts.Trees, worker)
			workerOpts.Restore = restoreFor(opts.Restores, worker)
			for {
				if ctx.Err() != nil {
					return
				}
				i := int(next.Add(1)) - 1
				if i >= len(mutants) {
					return
				}

				hooks.start(mutants[i].ID, worker)
				attempt := RunOne(ctx, workerOpts, mutants[i], bins)
				attempt.Worker = worker
				record(&results[i], attempt)
				opts.Trace.MutantExec(AttemptRecord(mutants[i], attempt, mainAttempt))
				if treeErr := treeFailure(attempt); treeErr != nil {
					failed.CompareAndSwap(nil, &treeErr)
					return
				}

				if attempt.Outcome == mutation.OutcomeTimedOut {
					if mutants[i].NeverReturns || attempt.Diverged {
						confirm(&results[i], attempt)
						hooks.finish(results[i])
						continue
					}
					pending[i] = true
					continue
				}
				settle(&results[i], attempt)
				hooks.finish(results[i])
			}
		}()
	}
	wg.Wait()

	if broken := failed.Load(); broken != nil {
		return results, *broken
	}

	retryOpts := opts
	retryOpts.ScratchDir = workerScratchDir(opts.ScratchDir, retryWorker)
	retryOpts.Tree = treeFor(opts.Trees, retryWorker)
	retryOpts.Restore = restoreFor(opts.Restores, retryWorker)
	retried := heldBack(pending)
	retryStage := func(string) time.Duration { return 0 }
	if retried > 0 {
		retryStage = opts.Trace.Stage("retry", countNoun(retried, "timeout"))
	}
	unretried := false
	for i := range mutants {
		if !pending[i] {
			continue
		}
		if ctx.Err() != nil {
			results[i].Final = mutation.OutcomeNotRun
			hooks.finish(results[i])
			unretried = true
			continue
		}

		hooks.start(mutants[i].ID, retryWorker)
		attempt := RunOne(ctx, retryOpts, mutants[i], bins)
		attempt.Worker = retryWorker
		record(&results[i], attempt)
		opts.Trace.MutantExec(AttemptRecord(mutants[i], attempt, retryAttempt))
		if treeErr := treeFailure(attempt); treeErr != nil {
			return results, treeErr
		}
		if attempt.Outcome == mutation.OutcomeNotRun {
			unretried = true
		}
		confirm(&results[i], attempt)
		hooks.finish(results[i])
	}
	retryStage(stageResult(unretried))

	if ctx.Err() != nil {
		interrupted := &Error{
			Code:    CodeInterrupted,
			Message: "the execution phase was interrupted",
			Err:     context.Cause(ctx),
		}
		if cut := cutOff(results); cut != nil {
			interrupted.Invocation = cut.Invocation
			interrupted.Output = cut.Output
		}
		return results, interrupted
	}
	return results, nil
}

func AttemptRecord(m MutantRun, attempt Attempt, number int) trace.MutantRecord {
	record := trace.MutantRecord{
		ID:              m.ID,
		DisplayID:       m.DisplayID,
		Attempt:         number,
		Worker:          attempt.Worker,
		Package:         m.Package,
		TimeoutMS:       m.Timeout.Milliseconds(),
		Binaries:        slices.Clone(attempt.Binaries),
		Tests:           testLabels(attempt.Tests),
		Args:            slices.Clone(m.Args),
		ExecSeqs:        slices.Clone(attempt.ExecSeqs),
		Outcome:         attempt.Outcome.String(),
		KilledBy:        attempt.KilledBy,
		DurationMS:      attempt.Duration.Milliseconds(),
		MemoryExceeded:  attempt.MemoryExceeded,
		PeakMemoryBytes: attempt.PeakMemory,
		OutputTail:      attempt.OutputTail,
	}
	if attempt.Err != nil {
		record.Error = attempt.Err.Error()
	}
	return record
}

func stageResult(unretried bool) string {
	if unretried {
		return trace.ResultFailed
	}
	return trace.ResultSucceeded
}

func heldBack(pending []bool) int {
	n := 0
	for _, held := range pending {
		if held {
			n++
		}
	}
	return n
}

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

func record(result *MutantResult, attempt Attempt) {
	result.Attempts = append(result.Attempts, attempt)
	result.Duration += attempt.Duration
}

func settle(result *MutantResult, attempt Attempt) {
	result.Final = attempt.Outcome
	result.KilledBy = attempt.KilledBy
	result.OutputTail = attempt.OutputTail
	result.Err = nil
	if attempt.Outcome == mutation.OutcomeErrored {
		result.Err = attempt.Err
	}
}

func confirm(result *MutantResult, attempt Attempt) {
	switch attempt.Outcome {
	case mutation.OutcomeTimedOut:
		result.Final = mutation.OutcomeTimedOut
		result.KilledBy = attempt.KilledBy
		result.OutputTail = attempt.OutputTail
	case mutation.OutcomeKilled, mutation.OutcomeSurvived:
		result.Final = mutation.OutcomeInconclusive
		result.OutputTail = attempt.OutputTail
	case mutation.OutcomeErrored, mutation.OutcomeInconclusive, mutation.OutcomeNotRun:
		settle(result, attempt)
	}
}

func workerScratchDir(parent string, worker int) string {
	if parent == "" {
		return ""
	}
	return filepath.Join(parent, "w"+strconv.Itoa(worker))
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func treeFor(trees []string, worker int) string {
	if worker < 0 || worker >= len(trees) {
		return ""
	}
	return trees[worker]
}

func treeFailure(attempt Attempt) error {
	if attempt.Err != nil && errors.Is(attempt.Err, ErrTreeNotRestored) {
		return attempt.Err
	}
	return nil
}

func restoreFor(restores []func() error, worker int) func() error {
	if worker < 0 || worker >= len(restores) {
		return nil
	}
	return restores[worker]
}
