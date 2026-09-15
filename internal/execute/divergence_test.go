// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
)

// diverged is what a test binary reports when one of its counted loops passed
// the ceiling this run derived for it: the generated runtime's own status, and
// the line it printed before exiting.
func diverged() runner.Result {
	return runner.Result{
		ExitCode: instrument.DivergedExit,
		Duration: time.Millisecond,
		Output: []byte("go-mutants: the loop at internal/config/position.go:170 ran 1048577 times, " +
			"past the 1048576 this run derived for it from what the original program did under the " +
			"same tests; this mutant does not return\n"),
	}
}

// TestADivergedMutantIsANonReturnThatNeedsNoSecondOpinion is the verdict half
// of ADR 0013.
//
// A timeout is measured twice before it is believed, and the reason is written
// into this package: one timeout on a machine running N test binaries at once
// says as much about the machine as about the mutant. A divergence says nothing
// at all about the machine. It is the count of what one loop did against the
// count of what the original program did in the same tree under the same tests,
// so measuring it again on a quiet machine would produce the same two numbers
// and cost a whole budget to do it.
//
// So it settles where it is read: the outcome is the outcome a mutant that does
// not return gets, and the flag beside it is what tells the scheduler it has
// already been confirmed.
func TestADivergedMutantIsANonReturnThatNeedsNoSecondOpinion(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return diverged() }}
	opts := execute.WithRunner(execute.Options{ScratchDir: t.TempDir()}, f.run)

	attempt := execute.RunOne(t.Context(), opts,
		execute.MutantRun{ID: "deadbeef", Timeout: mutantTimeout}, testBins("example.com/a"))

	if attempt.Outcome != mutation.OutcomeTimedOut {
		t.Errorf("outcome = %s, want %s: a loop that passed its ceiling is a mutant that does not return",
			attempt.Outcome, mutation.OutcomeTimedOut)
	}
	if !attempt.Diverged {
		t.Error("Diverged = false, so the scheduler would pay a whole budget to be told the same two numbers")
	}
	if attempt.KilledBy != "example.com/a" {
		t.Errorf("KilledBy = %q, want the binary whose loop it was", attempt.KilledBy)
	}
	if !contains(attempt.OutputTail, "does not return") {
		t.Errorf("the retained output does not carry the runtime's own account:\n%s", attempt.OutputTail)
	}
}

// TestTheSchedulerDoesNotRemeasureADivergence is the same claim where it is
// paid for.
//
// The retry pass is serial on purpose — it exists so that a timeout is measured
// once with nothing else running — so every mutant it takes costs a whole
// per-mutant budget of wall clock that nothing else can use. A divergence that
// went through it would be the deterministic answer waiting for the
// non-deterministic one to agree.
func TestTheSchedulerDoesNotRemeasureADivergence(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return diverged() }}
	opts := execute.WithRunner(execute.Options{ScratchDir: t.TempDir(), Jobs: 1}, f.run)

	results, err := execute.Schedule(t.Context(), opts,
		mutants(mutantTimeout, "deadbeef"), testBins("example.com/a"), execute.Hooks{})
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Schedule returned %d results, want 1", len(results))
	}
	if got := results[0].Final; got != mutation.OutcomeTimedOut {
		t.Errorf("Final = %s, want %s", got, mutation.OutcomeTimedOut)
	}
	if got := len(results[0].Attempts); got != 1 {
		t.Errorf("the scheduler made %d attempts, want 1: a divergence is the same answer twice", got)
	}
	if got := len(f.seen()); got != 1 {
		t.Errorf("the scheduler started %d processes, want 1", got)
	}
}

// contains is strings.Contains, spelled here so that the assertions above read
// as one line each.
func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
