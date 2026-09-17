// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"sync"
	"sync/atomic"
)

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
	for i := range controls {
		if !started[i] {
			attempts[i] = controlErrored(controlInterrupted(ctx, "", nil))
		}
	}
	return attempts
}
