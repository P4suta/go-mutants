// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/runner"
)

// TestRunControlsReturnsEachControlsOwnAnswerInOrder: several controls, one
// pool, and the answers land where the controls were given whichever worker
// ran which.
func TestRunControlsReturnsEachControlsOwnAnswerInOrder(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if strings.Contains(testRunOf(c), "TestFails") {
			return failed("--- FAIL: TestFails\n")
		}
		return passed()
	}}
	opts := options(f, 3)
	opts.ScratchDir = t.TempDir()
	bins := testBins("example.com/a")
	controls := []execute.ControlRun{
		{Timeout: mutantTimeout, Tests: map[string][]string{"example.com/a": {"TestOne"}}},
		{Timeout: mutantTimeout, Tests: map[string][]string{"example.com/a": {"TestFails", "TestOne"}}},
		{Timeout: mutantTimeout, Tests: map[string][]string{"example.com/a": {"TestTwo"}}},
	}

	attempts := execute.RunControls(t.Context(), opts, controls, bins)
	if len(attempts) != 3 {
		t.Fatalf("got %d attempts for 3 controls", len(attempts))
	}
	for i, attempt := range attempts {
		if attempt.Err != nil {
			t.Errorf("control %d: %v", i, attempt.Err)
		}
	}
	if attempts[0].ExitCode != 0 || attempts[2].ExitCode != 0 {
		t.Errorf("the passing controls exited %d and %d, want 0", attempts[0].ExitCode, attempts[2].ExitCode)
	}
	if attempts[1].ExitCode == 0 {
		t.Errorf("the failing control exited 0")
	}
	if len(f.seen()) != 3 {
		t.Errorf("started %d processes, want one per control", len(f.seen()))
	}

	// Each worker was given a scratch directory of its own, as the
	// scheduler's workers are, so two concurrent controls cannot share one.
	dirs := map[string]bool{}
	for _, c := range f.seen() {
		dirs[envValue(c.Env, "TMPDIR")] = true
	}
	for dir := range dirs {
		if !strings.HasPrefix(dir, opts.ScratchDir) {
			t.Errorf("a control ran with TMPDIR=%q, want one under %s", dir, opts.ScratchDir)
		}
	}
}

// TestRunControlsReportsWhatACancellationLeftUnstarted: a control the pool
// never reached is an interruption, never a pass.
func TestRunControlsReportsWhatACancellationLeftUnstarted(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	var once sync.Once
	f := &fake{respond: func(_ context.Context, _ call) runner.Result {
		once.Do(cancel)
		return passed()
	}}
	opts := options(f, 1)
	controls := slices.Repeat([]execute.ControlRun{{Timeout: mutantTimeout}}, 3)

	attempts := execute.RunControls(ctx, opts, controls, testBins("example.com/a"))
	interrupted := 0
	for _, attempt := range attempts {
		if execute.CodeOf(attempt.Err) == execute.CodeInterrupted {
			interrupted++
		}
	}
	if interrupted == 0 {
		t.Errorf("no control reports the interruption: %+v", attempts)
	}
	if len(f.seen()) > 1 {
		t.Errorf("started %d processes after the cancellation, want the pool to stop", len(f.seen()))
	}
}

// TestRunControlsWithNothingRunsNothing is the empty case.
func TestRunControlsWithNothingRunsNothing(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	if got := execute.RunControls(t.Context(), options(f, 2), nil, testBins("example.com/a")); len(got) != 0 {
		t.Errorf("got %d attempts for no controls", len(got))
	}
	if len(f.seen()) != 0 {
		t.Errorf("started %v for no controls", f.programs())
	}
}
