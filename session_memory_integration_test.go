// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package gomutants_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/runner"
)

// runawayRequestBound is what the memory tests below allow one target.
//
// It is a request-level bound rather than the session's own, and it is small on
// purpose: the session derives its default from the floor upwards, and a test
// that had to watch a target allocate a gibibyte to prove anything would cost
// the machine a gibibyte on every run of this suite. A quarter of one is far
// above what this fixture's test binary needs and far below anything that could
// inconvenience a machine.
const runawayRequestBound = 256 << 20

// runawayFixture is one session over fixtures/runaway, whose single interesting
// mutant turns a terminating loop into one that appends forever.
//
// Verification is skipped for the reason [controlledFixture] skips it — nothing
// here needs the fixture's own suite run against the instrumented tree, only
// binaries to run it in — and the probe tree is prepared because one of the
// claims below is about a probe pass.
var runawayFixture = sync.OnceValue(func() *preparedFixture {
	return prepareFixtureWith("runaway", nil,
		gomutants.OpenOptions{},
		gomutants.PrepareOptions{
			Probe:              true,
			ProbeCoverPackages: []string{"fixture.example/runaway/..."},
			SkipVerify:         true,
			MutantTimeout:      30 * time.Second,
		})
})

// runaway returns that session, failing the calling test if preparing it did
// not work.
func runaway(t *testing.T) *preparedFixture {
	t.Helper()
	prepared := runawayFixture()
	if prepared.err != nil {
		t.Fatalf("preparing the runaway fixture: %v", prepared.err)
	}
	return prepared
}

// runawayMutant is the fixture's negated loop condition: the one mutant here
// that allocates without bound.
func runawayMutant(t *testing.T, prepared *preparedFixture) gomutants.Mutant {
	t.Helper()
	for _, m := range prepared.catalog.Mutants {
		if strings.Contains(m.Rule, "negate-loop-condition") {
			return m
		}
	}
	t.Fatal("the runaway fixture catalogues no negated loop condition")
	return gomutants.Mutant{}
}

// TestEveryCallReportsWhatItCostAndACallOverItsBoundSaysSo is the library half
// of the memory bound.
//
// The three calls share one process core, so they share one claim: what a call
// cost the machine comes back from every one of them, and a tree the bound
// stopped says which of the two budgets settled it. The negative — that a
// request naming no bound is still bounded, by the session's own — is the half
// a consumer relies on without asking for it.
func TestEveryCallReportsWhatItCostAndACallOverItsBoundSaysSo(t *testing.T) {
	prepared := runaway(t)
	mutant := runawayMutant(t, prepared)

	// A control first: the original program, which fits comfortably.
	control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		MemoryLimit: runawayRequestBound,
	})
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if control.MemoryExceeded {
		t.Errorf("the original program was stopped by a %d byte bound; output: %s",
			runawayRequestBound, control.Output)
	}
	if runner.MemoryBoundSupported() && control.PeakRSS <= 0 {
		t.Error("Control reports no peak, and it started a process")
	}

	// A probe pass, which runs the same tests against a tree with no mutant
	// activated, and is therefore the same shape of answer.
	probe, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
		MemoryLimit: runawayRequestBound,
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if probe.MemoryExceeded {
		t.Errorf("a probe pass over the original program was stopped by a %d byte bound", runawayRequestBound)
	}
	if runner.MemoryBoundSupported() && probe.PeakRSS <= 0 {
		t.Error("Probe reports no peak, and it started a process")
	}

	// And the mutant the fixture exists for.
	result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:      mutant.ID,
		MemoryLimit: runawayRequestBound,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !runner.MemoryBoundSupported() {
		t.Skipf("this platform enforces no bound, so the mutant came back %s having been stopped by nothing",
			result.Outcome)
	}
	if !result.MemoryExceeded {
		t.Fatalf("MemoryExceeded = false for a mutant that allocates without bound (outcome %s); output: %s",
			result.Outcome, result.Output)
	}
	if result.Outcome != gomutants.OutcomeKilled {
		t.Errorf("Outcome = %s, want %s: a bound settles a mutant with the vocabulary that already exists",
			result.Outcome, gomutants.OutcomeKilled)
	}
	if result.PeakRSS <= 0 {
		t.Error("a mutant killed for its memory reports no peak")
	}
	if result.KilledBy == "" {
		t.Error("a kill names no suite, and a kill is always by something")
	}
}

// TestACallThatNamesNoBoundGetsTheSessionsOwn pins the default a consumer never
// writes down.
//
// The session's bound is derived from the floor upwards, so it is a gibibyte
// here and the runaway mutant reaches it — which is the point: a consumer that
// asks for nothing is still protected from a mutant that would otherwise take
// the machine. The timeout is what makes the claim falsifiable rather than a
// wait: without a bound this call would run until the session's 30 seconds
// expired and come back timed out instead.
func TestACallThatNamesNoBoundGetsTheSessionsOwn(t *testing.T) {
	if !runner.MemoryBoundSupported() {
		t.Skip("this platform enforces no bound, so there is no default to inherit")
	}
	prepared := runaway(t)
	mutant := runawayMutant(t, prepared)

	start := time.Now()
	result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{Mutant: mutant.ID})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !result.MemoryExceeded {
		t.Fatalf("MemoryExceeded = false for a request that named no bound (outcome %s, %s); "+
			"the session's own bound was not applied", result.Outcome, elapsed)
	}
	if result.Outcome != gomutants.OutcomeKilled {
		t.Errorf("Outcome = %s, want %s", result.Outcome, gomutants.OutcomeKilled)
	}
	// The session's timeout is 30s and this must not have reached it: a mutant
	// stopped by the deadline instead would be a different answer wearing the
	// same word.
	if elapsed >= 30*time.Second {
		t.Errorf("Exec took %s, which is the session's whole timeout", elapsed)
	}
}
