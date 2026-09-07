// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"maps"
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

// decideState is a state carrying display coordinates for the given mutant ids,
// so a mutant filed as uncovered has somewhere to be recorded.
func decideState(ids ...string) *state {
	st := &state{results: map[string]report.MutantResult{}, display: map[string]MutantResult{}}
	for _, id := range ids {
		st.display[id] = MutantResult{ID: id, DisplayID: id, Outcome: mutation.OutcomeSurvived}
	}
	return st
}

// reliableSets marks each given selection reliable.
func reliableSets(sets ...map[string][]string) setVerdicts {
	v := setVerdicts{reliable: make(map[string]bool)}
	for _, set := range sets {
		v.reliable[setKey(set)] = true
	}
	return v
}

// TestDecideRunsNarrowsToAReliableSet: a mutant whose covering tests passed
// their control runs against exactly those tests, and the binary they belong
// to is what it is measured against.
func TestDecideRunsNarrowsToAReliableSet(t *testing.T) {
	t.Parallel()

	s := &session{events: make(chan Event, 8)}
	bins := []execute.TestBinary{{ImportPath: "example.com/m/a"}, {ImportPath: "example.com/m/b"}}
	runs := []execute.MutantRun{{ID: "m1", Timeout: 1}}
	decided := []coverage.Mutant{{ID: "m1", Path: "a.go", StartLine: 1, EndLine: 1}}
	sel := map[string][]string{"example.com/m/a": {"TestA"}}
	plans := map[string]mutantPlan{"m1": {tests: sel}}

	covered, result := s.decideRuns(binaryIndex(bins), runs, decided, plans, reliableSets(sel), len(bins), 1, decideState("m1"))
	if len(covered) != 1 {
		t.Fatalf("covered %d runs, want 1", len(covered))
	}
	if !maps.EqualFunc(covered[0].Tests, sel, slices.Equal) {
		t.Errorf("run.Tests = %v, want %v", covered[0].Tests, sel)
	}
	if want := []int{0}; !slices.Equal(covered[0].Binaries, want) {
		t.Errorf("run.Binaries = %v, want %v (only a's index)", covered[0].Binaries, want)
	}
	if got := result.coveringTests["m1"]; len(got) != 1 || got[0] != (report.TestRef{Package: "example.com/m/a", Name: "TestA"}) {
		t.Errorf("coveringTests = %v, want the one test", got)
	}
	if result.widened != 0 {
		t.Errorf("widened = %d, want 0", result.widened)
	}
}

// TestDecideRunsWidensAnUnreliableSet: a mutant whose covering tests fail
// together loses the narrowing and runs against the whole binaries that reach
// it, counted as widened.
func TestDecideRunsWidensAnUnreliableSet(t *testing.T) {
	t.Parallel()

	s := &session{events: make(chan Event, 8)}
	bins := []execute.TestBinary{{ImportPath: "example.com/m/a"}}
	runs := []execute.MutantRun{{ID: "m1", Timeout: 1}}
	decided := []coverage.Mutant{{ID: "m1", Path: "a.go", StartLine: 1, EndLine: 1}}
	sel := map[string][]string{"example.com/m/a": {"TestA", "TestB"}}
	plans := map[string]mutantPlan{"m1": {tests: sel}}

	// No set marked reliable.
	covered, result := s.decideRuns(binaryIndex(bins), runs, decided, plans, setVerdicts{reliable: map[string]bool{}}, len(bins), 2, decideState("m1"))
	if len(covered) != 1 {
		t.Fatalf("covered %d runs, want the widened one", len(covered))
	}
	if covered[0].Tests != nil {
		t.Errorf("run.Tests = %v, want nil after widening", covered[0].Tests)
	}
	if want := []int{0}; !slices.Equal(covered[0].Binaries, want) {
		t.Errorf("run.Binaries = %v, want the whole binary %v", covered[0].Binaries, want)
	}
	if result.widened != 1 {
		t.Errorf("widened = %d, want 1", result.widened)
	}
	if got := result.coveringTests["m1"]; got != nil {
		t.Errorf("coveringTests = %v, want none for a widened mutant", got)
	}
}

// TestDecideRunsFilesAnUnreachedMutant: a mutant no test and no binary reaches
// is a survivor the run never executes.
func TestDecideRunsFilesAnUnreachedMutant(t *testing.T) {
	t.Parallel()

	s := &session{events: make(chan Event, 8)}
	bins := []execute.TestBinary{{ImportPath: "example.com/m/a"}}
	runs := []execute.MutantRun{{ID: "m1", Timeout: 1}}
	decided := []coverage.Mutant{{ID: "m1", Path: "a.go", StartLine: 1, EndLine: 1}}
	st := decideState("m1")

	covered, _ := s.decideRuns(binaryIndex(bins), runs, decided, map[string]mutantPlan{"m1": {}}, setVerdicts{}, len(bins), 0, st)
	if len(covered) != 0 {
		t.Fatalf("covered %d runs, want none", len(covered))
	}
	got, ok := st.results["m1"]
	if !ok {
		t.Fatal("the unreached mutant was not filed")
	}
	if got.Outcome != mutation.OutcomeSurvived || !got.Uncovered {
		t.Errorf("filed as %+v, want an uncovered survivor", got)
	}
}

// TestDecideRunsRunsADirtyBinaryWhole: a mutant reached only through a dirty
// binary — one with an order-dependent test — runs against the whole binary,
// with no test selection.
func TestDecideRunsRunsADirtyBinaryWhole(t *testing.T) {
	t.Parallel()

	s := &session{events: make(chan Event, 8)}
	bins := []execute.TestBinary{{ImportPath: "example.com/m/a"}, {ImportPath: "example.com/m/b"}}
	runs := []execute.MutantRun{{ID: "m1", Timeout: 1}}
	decided := []coverage.Mutant{{ID: "m1", Path: "b.go", StartLine: 1, EndLine: 1}}
	plans := map[string]mutantPlan{"m1": {binaries: []string{"example.com/m/b"}}}

	covered, result := s.decideRuns(binaryIndex(bins), runs, decided, plans, setVerdicts{}, len(bins), 0, decideState("m1"))
	if len(covered) != 1 {
		t.Fatalf("covered %d runs, want 1", len(covered))
	}
	if covered[0].Tests != nil {
		t.Errorf("run.Tests = %v, want nil for a whole-binary run", covered[0].Tests)
	}
	if want := []int{1}; !slices.Equal(covered[0].Binaries, want) {
		t.Errorf("run.Binaries = %v, want b's index %v", covered[0].Binaries, want)
	}
	if got := result.covering["m1"]; !slices.Equal(got, []string{"example.com/m/b"}) {
		t.Errorf("covering = %v, want [b]", got)
	}
}

// TestCoveringBinariesUnionsTestsAndDirtyBinaries: the binaries a mutant is
// measured against are those its narrowed tests belong to and the dirty ones
// run whole, deduplicated and sorted.
func TestCoveringBinariesUnionsTestsAndDirtyBinaries(t *testing.T) {
	t.Parallel()

	got := coveringBinaries(
		map[string][]string{"example.com/m/b": {"TestB"}, "example.com/m/a": {"TestA"}},
		[]string{"example.com/m/b", "example.com/m/c"},
	)
	want := []string{"example.com/m/a", "example.com/m/b", "example.com/m/c"}
	if !slices.Equal(got, want) {
		t.Errorf("coveringBinaries = %v, want %v", got, want)
	}
}

// TestSetKeyIsIndependentOfOrder: the same set of tests keys the same however
// the map or the slices were built, which is what makes one control serve
// every mutant that shares a set.
func TestSetKeyIsIndependentOfOrder(t *testing.T) {
	t.Parallel()

	a := map[string][]string{"p": {"TestA", "TestB"}, "q": {"TestC"}}
	b := map[string][]string{"q": {"TestC"}, "p": {"TestB", "TestA"}}
	if setKey(a) != setKey(b) {
		t.Errorf("setKey is order-dependent:\n%q\n%q", setKey(a), setKey(b))
	}
	if setKey(a) == setKey(map[string][]string{"p": {"TestA"}, "q": {"TestC"}}) {
		t.Error("setKey collides two different sets")
	}
}

// TestSetVerdictsTreatsTheEmptySetAsReliable: a mutant with no narrowed tests
// has no control to fail, so it is never widened for lack of one.
func TestSetVerdictsTreatsTheEmptySetAsReliable(t *testing.T) {
	t.Parallel()

	var v setVerdicts
	if !v.ok(nil) {
		t.Error("the empty set is not reliable")
	}
	if v.ok(map[string][]string{"p": {"TestA"}}) {
		t.Error("an unrecorded set was treated as reliable")
	}
}
