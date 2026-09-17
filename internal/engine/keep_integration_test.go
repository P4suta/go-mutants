// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/testkit"
)

func TestADeadlineIsAFailureWorthKeepingAndDiagnosing(t *testing.T) {
	t.Parallel()

	opts := options(t, "simple")
	opts.KeepTemp = KeepTempOnFailure

	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	outcome, _, err := watch(t, ctx, opts, func(event Event) {
		if phase, ok := event.(PhaseChanged); ok && phase.Phase == PhaseBaseline {
			<-ctx.Done()
		}
	})

	if err == nil {
		t.Fatal("the run finished inside a deadline it was meant to miss")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want the deadline in the chain", err)
	}
	if Interrupted(err) {
		t.Errorf("Interrupted(%v) = true; a deadline is a failure, not somebody stopping the run", err)
	}
	if outcome.Status != StatusFailed {
		t.Errorf("status = %s, want %s: a run that ran out of time did not do what it was asked",
			outcome.Status, StatusFailed)
	}
	if len(outcome.Preserved) != 2 {
		t.Fatalf("Preserved = %+v, want the snapshot and the scratch directory of a run somebody has to diagnose",
			outcome.Preserved)
	}
	for _, directory := range outcome.Preserved {
		if _, statErr := os.Stat(directory.Path); statErr != nil {
			t.Errorf("the run says it kept %s (%s), which is not there: %v",
				directory.Path, directory.Kind, statErr)
		}
	}
}

func TestACancelledRunIsStillAnInterruptionAndKeepsNothing(t *testing.T) {
	t.Parallel()

	opts := options(t, "simple")
	opts.KeepTemp = KeepTempOnFailure

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	outcome, _, err := watch(t, ctx, opts, func(event Event) {
		if phase, ok := event.(PhaseChanged); ok && phase.Phase == PhaseBaseline {
			cancel()
		}
	})

	if !Interrupted(err) {
		t.Fatalf("Interrupted(%v) = false, want a cancelled run reported as an interruption", err)
	}
	if outcome.Status != StatusInterrupted {
		t.Errorf("status = %s, want %s", outcome.Status, StatusInterrupted)
	}
	if len(outcome.Preserved) != 0 {
		t.Errorf("a cancelled run preserved %+v; nothing went wrong, so there is nothing to keep",
			outcome.Preserved)
	}
}

func TestKeepTempTakesNoPartInTheCacheKey(t *testing.T) {
	t.Parallel()

	root := testkit.Copy(t, "killable")
	cacheRoot := t.TempDir()

	cold := runCached(t, cacheOptions(t, root, cacheRoot))
	stored := reusableRows(cold)
	if len(stored) == 0 {
		t.Fatal("the first run stored nothing, so the second has nothing to find")
	}

	warmOpts := cacheOptions(t, root, cacheRoot)
	warmOpts.KeepTemp = KeepTempAlways
	warm := runCached(t, warmOpts)

	if got := cachedRows(warm); len(got) != len(stored) {
		t.Errorf("the keeping run adopted %d outcomes, want the %d the first run stored",
			len(got), len(stored))
	}
	for id, outcome := range stored {
		mutant, found := mutantByID(warm, id)
		switch {
		case !found:
			t.Errorf("mutant %s is not in the keeping run's report at all: the ids moved", id[:8])
		case !mutant.Cached:
			t.Errorf("mutant %s was measured again by the keeping run: the cache key moved", id[:8])
		case mutant.Outcome != outcome:
			t.Errorf("mutant %s came back %q, want the stored %q", id[:8], mutant.Outcome, outcome)
		}
	}
}

func TestAKeepingRunLeavesItsTemporariesAndAnOrdinaryOneDoesNot(t *testing.T) {
	t.Parallel()

	opts := options(t, "simple")
	opts.KeepTemp = KeepTempAlways
	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(outcome.Preserved) != 2 {
		t.Fatalf("the run preserved %+v, want the snapshot and the scratch directory", outcome.Preserved)
	}
	for _, directory := range outcome.Preserved {
		if _, statErr := os.Stat(directory.Path); statErr != nil {
			t.Errorf("the run says it kept %s (%s), which is not there: %v",
				directory.Path, directory.Kind, statErr)
		}
	}

	plain := options(t, "simple")
	plainOutcome, _, err := collect(t, t.Context(), plain)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(plainOutcome.Preserved) != 0 {
		t.Errorf("a run that was asked to keep nothing preserved %+v", plainOutcome.Preserved)
	}
	if _, statErr := os.Stat(plainOutcome.SnapshotRoot); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("the snapshot %s survived a run that kept nothing (%v)", plainOutcome.SnapshotRoot, statErr)
	}
}
