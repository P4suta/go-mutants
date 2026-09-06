// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of `--keep-temp`: what a real run leaves behind,
// and what it does not change by having been asked to.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/engine/...
//
// The comment above is deliberately not a package doc — integration_test.go
// carries this package's — which is what the blank line below is for.

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

// TestADeadlineIsAFailureWorthKeepingAndDiagnosing settles what a run whose
// context expired is.
//
// A cancellation and a deadline arrive at the same place and mean opposite
// things. Somebody pressed Ctrl-C, or an embedder called cancel: nothing went
// wrong, they asked for it, and leaving a module-sized directory behind every
// time somebody changes their mind is what would make `on-failure` unusable. A
// deadline expiring is the run failing to finish in the time it was given —
// nobody decided it at the moment it happened, and it is exactly the failure
// somebody needs the tree and the bundle for, because "it was too slow" is a
// question about where the time went.
//
// So [Interrupted] asks for [context.Canceled] and nothing else, and the run's
// status is decided by that same predicate rather than by a second one that
// could come to answer differently.
//
// The deadline is made to land inside the baseline by holding the event stream
// until it fires: the engine's sends block, so a consumer that stops draining
// stops the run where it stands — which is after the snapshot and the scratch
// directory exist and before anything has been measured.
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

// TestACancelledRunIsStillAnInterruptionAndKeepsNothing is the other half of
// the same decision, and the half that must not have moved.
//
// Narrowing [Interrupted] to [context.Canceled] is only safe if every real
// cancellation still carries it — and each of them does, because internal/engine,
// internal/validate and internal/execute all wrap the context's own cause rather
// than inventing one.
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

// TestKeepTempTakesNoPartInTheCacheKey is the invariant that would fail
// silently.
//
// A keep is a decision about a directory on the way out of a run, taken after
// every mutant has been measured. If it reached [cache.Context] it would give
// every run that ever asked to keep its temporaries a context of its own and an
// empty cache directory: correct results, no reuse, and nothing anywhere saying
// why the run that was supposed to be fast was not. Two runs over one workspace
// and one cache — the first keeping nothing, the second keeping everything —
// settle it: if the second finds every outcome the first stored, the key did
// not move.
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

// TestAKeepingRunLeavesItsTemporariesAndAnOrdinaryOneDoesNot is the same two
// runs seen from the temporary parent, which is where the price of the option
// is paid.
//
// The directories a keep leaves behind are the whole cost of the feature — a
// kept snapshot is a full copy of the module and nothing will ever remove it —
// so "off by default, and off means gone" is the claim worth checking against a
// real pipeline rather than against a hand-built pair of directories.
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
