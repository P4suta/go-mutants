// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

// runawayMemoryBound is what the `runaway` fixture's mutants are allowed.
//
// It is explicit rather than derived, and that is the point of running it here
// at all. The derived bound is a gibibyte at its floor, and a test that had to
// watch a process allocate one to prove anything would be a test that costs the
// machine a gibibyte on every run of the suite. Explicit is a documented path —
// `test.memory` replaces a derived bound exactly as `test.timeout` replaces a
// derived timeout — so this exercises the enforcement without exercising the
// arithmetic, which [TestMemoryBoundIsDerivedFromTheBaselinePeak] does for
// nothing.
//
// A quarter of a gibibyte is comfortably above what the fixture's own test
// binary needs — a three-row table over a countdown — and comfortably below
// anything that could inconvenience a machine.
const runawayMemoryBound = 256 << 20

// TestARunawayMutantIsKilledByTheMemoryBoundNotTheTimeout is the incident,
// reproduced and then stopped.
//
// PR #58 widened this repository's own dogfood gate to `internal/config`, and
// two of the mutants it brought in never return and allocate without bound. One
// of them reached eleven gigabytes in twelve seconds locally and took a GitHub
// runner down entirely — the job did not fail, it *vanished*, with "The runner
// has received a shutdown signal" — because the per-mutant timeout is derived
// from a suite that finishes in milliseconds and is therefore ten seconds, and
// ten seconds is a very long time to allocate for.
//
// So the assertions are about which of the two budgets settled it. The outcome
// alone would be `killed` either way, and a run whose timeout had done the work
// would look identical in the score.
func TestARunawayMutantIsKilledByTheMemoryBoundNotTheTimeout(t *testing.T) {
	t.Parallel()

	if !runner.MemoryBoundSupported() {
		t.Skip("this platform cannot sample a live process tree, so no bound is enforced on it")
	}

	opts := options(t, "runaway")
	opts.Config.Test.Memory = runawayMemoryBound
	sink := trace.NewMemorySink(0)
	opts.TraceSink = sink

	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}
	if outcome.Memory != runawayMemoryBound || outcome.MemorySource != MemorySourceExplicit {
		t.Fatalf("the run applied %d (%s), want the configured %d (%s)",
			outcome.Memory, outcome.MemorySource, runawayMemoryBound, MemorySourceExplicit)
	}

	// The event stream first, because it is what a console renders and what a
	// user sees. Exactly one mutant is expected to reach the bound; the fixture
	// documents which and why.
	var bounded []MutantResult
	for _, e := range events {
		if finished, ok := e.(MutantFinished); ok && finished.Result.MemoryExceeded {
			bounded = append(bounded, finished.Result)
		}
	}
	if len(bounded) != 1 {
		t.Fatalf("%d mutants were stopped by the bound, want exactly the one the fixture is built around:\n%s",
			len(bounded), strings.Join(results(events), "\n"))
	}
	killed := bounded[0]
	switch {
	case killed.Outcome != mutation.OutcomeKilled:
		t.Errorf("the bounded mutant is %s, want killed: a mutant that took the machine has been observably changed",
			killed.Outcome)
	case killed.Rule != "negate-loop-condition":
		t.Errorf("the bounded mutant is %s, want the negated loop condition", killed.Rule)
	case killed.KilledBy == "":
		t.Error("the bounded mutant names no suite, and a kill is always by something")
	case killed.PeakRSS <= 0:
		t.Error("the bounded mutant reports no peak, and a process killed for its memory reached some")
	case killed.MemoryLimit != runawayMemoryBound:
		t.Errorf("the bounded mutant was measured under %d, want the run's %d", killed.MemoryLimit, runawayMemoryBound)
	}

	// It was the bound and not the deadline. A mutant that reached the timeout
	// would have been retried serially and settled as a timeout or as
	// inconclusive, so this is two claims at once: the duration is well under
	// the budget, and no mutant in the run timed out at all.
	if killed.Duration >= outcome.Timeout {
		t.Errorf("the bounded mutant took %s against a %s timeout: the deadline could have been what stopped it",
			killed.Duration, outcome.Timeout)
	}
	for _, e := range events {
		if finished, ok := e.(MutantFinished); ok {
			switch finished.Result.Outcome {
			case mutation.OutcomeTimedOut, mutation.OutcomeInconclusive:
				t.Errorf("mutant %s came back %s: the bound did not reach it first",
					finished.Result.DisplayID, finished.Result.Outcome)
			}
		}
	}

	// And the report says the same thing, which is the half that outlives the
	// run: `memory_exceeded` on the execution row, beside an outcome of killed.
	if outcome.Report == nil {
		t.Fatal("the run published no report")
	}
	rows := 0
	for _, m := range outcome.Report.Mutants {
		for _, execution := range m.Executions {
			if !execution.MemoryExceeded {
				continue
			}
			rows++
			if m.Outcome != report.OutcomeKilled {
				t.Errorf("mutant %s carries memory_exceeded under outcome %s, want killed", m.ID[:8], m.Outcome)
			}
			if execution.Outcome != report.OutcomeKilled {
				t.Errorf("the execution row is %s, want killed", execution.Outcome)
			}
			if execution.PeakRSSBytes <= 0 {
				t.Errorf("the execution row reports peak_rss_bytes %d, want what it reached", execution.PeakRSSBytes)
			}
		}
	}
	if rows != 1 {
		t.Errorf("the report carries %d executions with memory_exceeded, want 1", rows)
	}

	// The recording is the third place the same fact has to appear, because it
	// is the one a person reaches for when a run on somebody else's machine did
	// something they cannot reproduce.
	recorded := 0
	for _, e := range sink.Events() {
		if e.Type == trace.TypeMutantExec && e.Mutant != nil && e.Mutant.MemoryExceeded {
			recorded++
			if e.Mutant.PeakRSSBytes <= 0 {
				t.Errorf("the mutant-exec record reports peak_rss_bytes %d", e.Mutant.PeakRSSBytes)
			}
		}
	}
	if recorded != 1 {
		t.Errorf("the recording holds %d bounded attempts, want 1", recorded)
	}
}

// TestAnOrdinaryRunReportsAPeakForEveryExecutionAndBoundsNone pins the other
// side of the measurement: every mutant a run executes says what it cost,
// whether or not anything stopped it, and a run nobody bounded stops nothing.
func TestAnOrdinaryRunReportsAPeakForEveryExecutionAndBoundsNone(t *testing.T) {
	t.Parallel()

	outcome, _, err := collect(t, t.Context(), options(t, "killable"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Report == nil {
		t.Fatal("the run published no report")
	}
	if runner.MemoryBoundSupported() {
		if outcome.MemorySource != MemorySourceDerived || outcome.Memory < MinDerivedMemory {
			t.Errorf("the run applied %d (%s), want at least the %d floor, derived",
				outcome.Memory, outcome.MemorySource, MinDerivedMemory)
		}
		if outcome.PeakBaseline <= 0 {
			t.Error("the baseline measured no peak, so the bound was derived from nothing")
		}
	}

	measured := 0
	for _, m := range outcome.Report.Mutants {
		for _, execution := range m.Executions {
			if execution.MemoryExceeded {
				t.Errorf("mutant %s was stopped by a bound in a run whose suite fits in it", m.ID[:8])
			}
			if execution.PeakRSSBytes > 0 {
				measured++
			}
		}
	}
	if measured == 0 {
		t.Error("no execution in the whole run reported a peak, and every one of them started a process")
	}
}

// TestTheBaselineItselfIsNeverBounded pins the one command the bound may not
// apply to.
//
// The bound is derived from what the baseline runs cost, so bounding them would
// be deriving a budget from a measurement taken under that budget. It is also
// the one measurement that has to be allowed to be as large as the project
// really is: a suite that legitimately needs four gigabytes gets a sixteen
// gigabyte bound, and it can only get there by having been measured unbounded.
func TestTheBaselineItselfIsNeverBounded(t *testing.T) {
	t.Parallel()

	opts := options(t, "simple")
	opts.Config.Test.Memory = 1 << 20
	sink := trace.NewMemorySink(0)
	opts.TraceSink = sink

	// A one-mebibyte bound is below what any Go test binary needs, so a
	// baseline that were bounded by it could not finish. That the run gets as
	// far as publishing is itself half the assertion.
	if _, _, err := collect(t, t.Context(), opts); err != nil {
		t.Fatalf("Run: %v; a bounded baseline could not have completed", err)
	}

	for _, e := range sink.Events() {
		if e.Type != trace.TypeExec || e.Exec == nil {
			continue
		}
		switch e.Exec.Kind {
		case trace.ExecKindBaselineTest, trace.ExecKindBaselineBuild:
			if e.Exec.PeakRSSBytes <= 0 {
				t.Errorf("the %s command reports no peak, and the bound is derived from one", e.Exec.Kind)
			}
		}
	}
}

// TestTheMemoryBoundIsPublishedOnceBesideTheBaseline pins where in the stream
// the budget's second half arrives.
func TestTheMemoryBoundIsPublishedOnceBesideTheBaseline(t *testing.T) {
	t.Parallel()

	_, events, err := collect(t, t.Context(), options(t, "simple"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	baseline, derived := -1, -1
	for i, e := range events {
		switch e.(type) {
		case BaselineCompleted:
			baseline = i
		case MemoryDerived:
			if derived >= 0 {
				t.Error("MemoryDerived was published more than once")
			}
			derived = i
		}
	}
	switch {
	case derived < 0:
		t.Fatal("no MemoryDerived event was published")
	case baseline < 0:
		t.Fatal("no BaselineCompleted event was published")
	case derived != baseline+1:
		t.Errorf("MemoryDerived is event %d and BaselineCompleted is %d, want the budget's two halves together",
			derived, baseline)
	}
	if _, ok := events[derived].(MemoryDerived); !ok {
		t.Fatalf("event %d is %T", derived, events[derived])
	}
	if e := events[derived].(MemoryDerived); e.Source == MemorySourceDerived && e.Peak <= 0 {
		t.Errorf("a derived bound was published with a peak of %d, which is nothing to derive from", e.Peak)
	}
}

// TestAMemoryKillIsNotReusedByARunWithADifferentBound is the cache half of the
// bound, and it is the failure that would have been silent.
//
// A memory kill is settled as `killed`, so the cache stores it like any other
// kill. The bound is deliberately not in the key — a derived bound follows the
// baseline peak, so keying on it would give every machine a cache of its own —
// which leaves exactly one thing standing between a correct run and a wrong
// one: the rule that an entry is only evidence about a compatible bound. This
// drives it both ways round with a mutant whose verdict really does depend on
// the number.
func TestAMemoryKillIsNotReusedByARunWithADifferentBound(t *testing.T) {
	t.Parallel()

	if !runner.MemoryBoundSupported() {
		t.Skip("this platform enforces no bound, so no measurement is made under one")
	}

	root := testkit.Copy(t, "runaway")
	cacheRoot := t.TempDir()

	// A tight bound: the runaway mutant is killed by it, and the entry says so.
	tight := cacheOptions(t, root, cacheRoot)
	tight.Config.Test.Memory = runawayMemoryBound
	cold := runCached(t, tight)
	bounded := memoryKilledIDs(t, cold)
	if len(bounded) != 1 {
		t.Fatalf("the tight run killed %d mutants by the bound, want the one the fixture is built around", len(bounded))
	}
	killed := bounded[0]

	// The same workspace under a bound thirty times larger. Every other mutant
	// is adopted — the key did not move — and the one the bound killed is
	// measured again, because thirty times the memory might have let it finish.
	loose := cacheOptions(t, root, cacheRoot)
	loose.Config.Test.Memory = 8 << 30
	warm := runCached(t, loose)

	adopted := cachedRows(warm)
	if _, reused := adopted[killed]; reused {
		t.Error("the mutant killed by a 256 MiB bound was adopted by a run allowed 8 GiB, " +
			"which never asked whether it would have finished")
	}
	if len(adopted) == 0 {
		t.Error("the larger run adopted nothing at all, so the bound has reached the cache key")
	}
	for id := range reusableRows(cold) {
		if id == killed {
			continue
		}
		if _, reused := adopted[id]; !reused {
			t.Errorf("mutant %s was measured again although the bound settled nothing about it", id[:8])
		}
	}

	// And the other direction. An entry measured under a large bound is not
	// evidence about a smaller one, because the smaller one might have killed
	// it for its memory before it reached any verdict.
	tighter := cacheOptions(t, root, cacheRoot)
	tighter.Config.Test.Memory = runawayMemoryBound
	third := runCached(t, tighter)
	for id, outcome := range cachedRows(third) {
		if outcome == report.OutcomeSurvived || outcome == report.OutcomeTimedOut {
			t.Errorf("mutant %s came back %s from an entry measured under a larger bound", id[:8], outcome)
		}
	}
}

// memoryKilledIDs names every mutant a run reported as stopped by the bound.
func memoryKilledIDs(t *testing.T, r *report.Report) []string {
	t.Helper()
	var out []string
	for _, m := range r.Mutants {
		for _, execution := range m.Executions {
			if execution.MemoryExceeded {
				out = append(out, m.ID)
				break
			}
		}
	}
	return out
}
