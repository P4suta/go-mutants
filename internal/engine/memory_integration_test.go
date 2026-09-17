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

const runawayMemoryBound = 256 << 20

func TestARunawayMutantIsKilledByTheMemoryBoundNotTheTimeout(t *testing.T) {
	t.Parallel()

	if !runner.MemoryBoundSupported() {
		t.Skip("this platform cannot sample a live process tree, so no bound is enforced on it")
	}

	opts := options(t, "memorybound")
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
	case killed.PeakMemory <= 0:
		t.Error("the bounded mutant reports no peak, and a process killed for its memory reached some")
	case killed.MemoryLimit != runawayMemoryBound:
		t.Errorf("the bounded mutant was measured under %d, want the run's %d", killed.MemoryLimit, runawayMemoryBound)
	}

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
			if execution.PeakMemoryBytes <= 0 {
				t.Errorf("the execution row reports peak_memory_bytes %d, want what it reached", execution.PeakMemoryBytes)
			}
		}
	}
	if rows != 1 {
		t.Errorf("the report carries %d executions with memory_exceeded, want 1", rows)
	}

	recorded := 0
	for _, e := range sink.Events() {
		if e.Type == trace.TypeMutantExec && e.Mutant != nil && e.Mutant.MemoryExceeded {
			recorded++
			if e.Mutant.PeakMemoryBytes <= 0 {
				t.Errorf("the mutant-exec record reports peak_memory_bytes %d", e.Mutant.PeakMemoryBytes)
			}
		}
	}
	if recorded != 1 {
		t.Errorf("the recording holds %d bounded attempts, want 1", recorded)
	}
}

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
			if execution.PeakMemoryBytes > 0 {
				measured++
			}
		}
	}
	if measured == 0 {
		t.Error("no execution in the whole run reported a peak, and every one of them started a process")
	}
}

func TestTheBaselineItselfIsNeverBounded(t *testing.T) {
	t.Parallel()

	opts := options(t, "simple")
	opts.Config.Test.Memory = 1 << 20
	sink := trace.NewMemorySink(0)
	opts.TraceSink = sink

	if _, _, err := collect(t, t.Context(), opts); err != nil {
		t.Fatalf("Run: %v; a bounded baseline could not have completed", err)
	}

	for _, e := range sink.Events() {
		if e.Type != trace.TypeExec || e.Exec == nil {
			continue
		}
		switch e.Exec.Kind {
		case trace.ExecKindBaselineTest, trace.ExecKindBaselineBuild:
			if e.Exec.PeakMemoryBytes <= 0 {
				t.Errorf("the %s command reports no peak, and the bound is derived from one", e.Exec.Kind)
			}
		}
	}
}

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

func TestAMemoryKillIsNotReusedByARunWithADifferentBound(t *testing.T) {
	t.Parallel()

	if !runner.MemoryBoundSupported() {
		t.Skip("this platform enforces no bound, so no measurement is made under one")
	}

	root := testkit.Copy(t, "memorybound")
	cacheRoot := t.TempDir()

	tight := cacheOptions(t, root, cacheRoot)
	tight.Config.Test.Memory = runawayMemoryBound
	cold := runCached(t, tight)
	bounded := memoryKilledIDs(t, cold)
	if len(bounded) != 1 {
		t.Fatalf("the tight run killed %d mutants by the bound, want the one the fixture is built around", len(bounded))
	}
	killed := bounded[0]

	loose := cacheOptions(t, root, cacheRoot)
	loose.Config.Test.Memory = 2 * runawayMemoryBound
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

	tighter := cacheOptions(t, root, cacheRoot)
	tighter.Config.Test.Memory = runawayMemoryBound
	third := runCached(t, tighter)
	for id, outcome := range cachedRows(third) {
		if outcome == report.OutcomeSurvived || outcome == report.OutcomeTimedOut {
			t.Errorf("mutant %s came back %s from an entry measured under a larger bound", id[:8], outcome)
		}
	}
}

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

func TestACachedMemoryKillReadsLikeAMeasuredOne(t *testing.T) {
	t.Parallel()

	if !runner.MemoryBoundSupported() {
		t.Skip("this platform enforces no bound, so no memory kill is measured to be cached")
	}

	root := testkit.Copy(t, "memorybound")
	cacheRoot := t.TempDir()

	cold := cacheOptions(t, root, cacheRoot)
	cold.Config.Test.Memory = runawayMemoryBound
	first := runCached(t, cold)
	killed := memoryKilledIDs(t, first)
	if len(killed) != 1 {
		t.Fatalf("the first run killed %d mutants by the bound, want one", len(killed))
	}
	measured := mutantByID2(t, first, killed[0])
	if measured.PeakMemoryBytes <= 0 {
		t.Fatalf("the measured mutant reports no peak, so there is nothing for the cache to keep")
	}

	warm := cacheOptions(t, root, cacheRoot)
	warm.Config.Test.Memory = runawayMemoryBound
	second := runCached(t, warm)
	adopted := mutantByID2(t, second, killed[0])

	switch {
	case !adopted.Cached:
		t.Fatal("the second run measured the mutant again, so nothing was adopted")
	case len(adopted.Executions) != 0:
		t.Errorf("a cached mutant carries %d execution rows", len(adopted.Executions))
	case !adopted.MemoryExceeded:
		t.Error("the adopted mutant does not say the bound settled it")
	case adopted.PeakMemoryBytes != measured.PeakMemoryBytes:
		t.Errorf("the adopted peak is %d, want the %d the measuring run recorded",
			adopted.PeakMemoryBytes, measured.PeakMemoryBytes)
	}
}

func mutantByID2(t *testing.T, r *report.Report, id string) report.Mutant {
	t.Helper()
	for _, m := range r.Mutants {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("mutant %s is not in the report", id[:8])
	return report.Mutant{}
}
