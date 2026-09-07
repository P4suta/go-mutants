// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"sync"
	"sync/atomic"
)

// RunControls runs several controls through the same worker pool [Schedule]
// runs mutants through, and returns their attempts in the order the controls
// were given.
//
// It exists for test-level narrowing, which has one control per distinct set
// of tests a mutant is narrowed to and wants them all before the first mutant
// runs: whether "these tests, run together, pass without a mutant" is what
// licenses reading a failure under a mutant as the mutant's doing, and a run
// that learnt it afterwards would have to take verdicts back. The pool is the
// scheduler's, down to the per-worker scratch directory, because the controls
// are the same binaries under the same bound and there is no second opinion
// about how many of them the machine can hold.
//
// A cancelled context stops the pool; every control not yet started comes back
// as the interruption [RunControl] itself reports, so a caller reading the
// slice sees an error and not a pass for a control that never ran.
func RunControls(ctx context.Context, opts Options, controls []ControlRun, bins []TestBinary) []ControlAttempt {
	attempts := make([]ControlAttempt, len(controls))
	if len(controls) == 0 {
		return attempts
	}
	started := make([]bool, len(controls))

	workers := min(opts.workers(), len(controls))
	var next atomic.Int64
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerOpts := opts
			workerOpts.ScratchDir = workerScratchDir(opts.ScratchDir, worker)
			for {
				if ctx.Err() != nil {
					return
				}
				i := int(next.Add(1)) - 1
				if i >= len(controls) {
					return
				}
				started[i] = true
				attempts[i] = RunControl(ctx, workerOpts, controls[i], bins)
			}
		}()
	}
	wg.Wait()
	// Only a cancelled context leaves a control unstarted, and it is reported
	// as the interruption it is rather than as the zero value, which would
	// read as a control that passed.
	for i := range controls {
		if !started[i] {
			attempts[i] = controlErrored(controlInterrupted(ctx, "", nil))
		}
	}
	return attempts
}
