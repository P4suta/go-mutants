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

const runawayRequestBound = 256 << 20

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

func runaway(t *testing.T) *preparedFixture {
	t.Helper()
	prepared := runawayFixture()
	if prepared.err != nil {
		t.Fatalf("preparing the runaway fixture: %v", prepared.err)
	}
	return prepared
}

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

func TestEveryCallReportsWhatItCostAndACallOverItsBoundSaysSo(t *testing.T) {
	prepared := runaway(t)
	mutant := runawayMutant(t, prepared)

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
	if runner.PeakMemoryReachesEveryRun() && control.PeakMemory <= 0 {
		t.Errorf("Control reports no peak on a platform whose accounting reaches every run: %+v", control)
	}

	probe, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
		MemoryLimit: runawayRequestBound,
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if probe.MemoryExceeded {
		t.Errorf("a probe pass over the original program was stopped by a %d byte bound", runawayRequestBound)
	}
	if runner.PeakMemoryReachesEveryRun() && probe.PeakMemory <= 0 {
		t.Errorf("Probe reports no peak on a platform whose accounting reaches every run: %+v", probe)
	}

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
	if result.PeakMemory <= 0 {
		t.Error("a mutant killed for its memory reports no peak")
	}
	if result.KilledBy == "" {
		t.Error("a kill names no suite, and a kill is always by something")
	}
}

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
	if elapsed >= 30*time.Second {
		t.Errorf("Exec took %s, which is the session's whole timeout", elapsed)
	}
}

func TestAFuzzRunIsNotHeldToABoundDerivedFromSomethingElse(t *testing.T) {
	prepared := controlled(t)
	args := []string{
		"-test.run=^$",
		"-test.fuzz=^FuzzSessionIdentity$",
		"-test.fuzztime=100ms",
	}

	control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		Package: killableModule,
		Args:    args,
		Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if control.MemoryExceeded {
		t.Errorf("a fuzz run was stopped by a bound it never asked for: peak %d; output:\n%s",
			control.PeakMemory, control.Output)
	}
	if control.ExitCode != 0 {
		t.Errorf("the fuzz control exited %d, want 0; output:\n%s", control.ExitCode, control.Output)
	}

	bounded, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		Package:     killableModule,
		Args:        args,
		Timeout:     60 * time.Second,
		MemoryLimit: runawayRequestBound,
	})
	if err != nil {
		t.Fatalf("Control with a named limit: %v", err)
	}
	if !runner.MemoryBoundSupported() {
		return
	}
	if bounded.PeakMemory <= 0 {
		t.Error("a bounded fuzz control reports no peak, so nothing measured it")
	}
}
