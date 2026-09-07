// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// narrowModule is a module with two binaries a default (test-narrowed) run has
// to treat differently, so one run exercises both halves of the narrowing.
const narrowModule = "fixture.example/narrow"

// cleanSource's one test passes on its own, so its binary is clean and its
// mutant is narrowed to the single test that reaches it.
const cleanSource = `package clean

// Positive is reached by TestPositive alone.
func Positive(v int) bool {
	return v > 0
}
`

const cleanSuite = `package clean

import "testing"

func TestPositive(t *testing.T) {
	if !Positive(1) || Positive(0) {
		t.Fatal("Positive is wrong")
	}
}
`

// dirtySource's mutant is reached only by a test that fails on its own, so its
// binary cannot be narrowed and the mutant is measured against the whole
// binary and killed there rather than reported uncovered.
const dirtySource = `package dirty

// Guarded is reached only by TestSecond, which fails when run alone.
func Guarded(v int) bool {
	return v > 0
}
`

const dirtySuite = `package dirty

import "testing"

var ready bool

func TestFirst(t *testing.T) {
	ready = true
}

func TestSecond(t *testing.T) {
	if !ready {
		t.Fatal("TestSecond needs TestFirst to have run first")
	}
	if !Guarded(1) || Guarded(0) {
		t.Fatal("Guarded is wrong")
	}
}
`

// TestTestNarrowingRunsOneTestAndWidensAnOrderDependentOne is the whole of
// test-level narrowing against a real toolchain, in one run.
//
//   - The run's coverage mode is test, and it profiled the tests it could.
//   - The clean binary's mutant is narrowed to TestPositive: the only test that
//     reaches it, named on the mutant and on its one execution.
//   - The dirty binary's TestSecond fails on its own, so it is named in an
//     order-dependent warning and its binary is not narrowed; its mutant is
//     measured against the whole binary and killed there, carrying the binary
//     but no covering test.
//   - Nothing is reported uncovered: every mutant a test reaches was run.
func TestTestNarrowingRunsOneTestAndWidensAnOrderDependentOne(t *testing.T) {
	t.Parallel()

	root := testkit.NewModule(t).Module(narrowModule).
		Source("clean/clean.go", cleanSource).
		Source("clean/clean_test.go", cleanSuite).
		Source("dirty/dirty.go", dirtySource).
		Source("dirty/dirty_test.go", dirtySuite).
		Root()
	opts := optionsAt(t, root)

	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("running the narrow module: %v", err)
	}
	if outcome.Report == nil {
		t.Fatal("the run published no report")
	}

	block := outcome.Report.Coverage
	if block.Mode != report.CoverageTest {
		t.Fatalf("coverage mode = %q, want %q", block.Mode, report.CoverageTest)
	}
	if block.Tests == nil || *block.Tests < 1 {
		t.Errorf("coverage.tests = %v, want the tests the pass profiled", block.Tests)
	}
	if block.MutantsUncovered == nil || *block.MutantsUncovered != 0 {
		t.Errorf("coverage.mutants_uncovered = %v, want 0: every mutant a test reaches was run", block.MutantsUncovered)
	}

	// The order-dependent test is named in a GOM7603 warning.
	warning := warningWithCode(outcome.Report, string(coverage.CodeOrderDependentTests))
	if warning == nil {
		t.Fatalf("no %s warning was published: %+v", coverage.CodeOrderDependentTests, outcome.Report.Warnings)
	}
	if !strings.Contains(warning.Message, "TestSecond") {
		t.Errorf("the order-dependent warning does not name TestSecond: %q", warning.Message)
	}

	cleanPkg := narrowModule + "/clean"
	dirtyPkg := narrowModule + "/dirty"
	positive := onlyMutant(t, outcome.Report, cleanPkg, "gt-to-ge")
	guarded := onlyMutant(t, outcome.Report, dirtyPkg, "gt-to-ge")

	// The clean binary's mutant was narrowed to the one test that reaches it.
	if positive.Outcome != report.OutcomeKilled {
		t.Errorf("the clean mutant is %s, want killed", positive.Outcome)
	}
	if want := []report.TestRef{{Package: cleanPkg, Name: "TestPositive"}}; !equalRefs(positive.CoveringTests, want) {
		t.Errorf("the clean mutant's covering tests = %v, want %v", positive.CoveringTests, want)
	}
	if len(positive.Executions) != 1 || !equalRefs(positive.Executions[0].Tests, []report.TestRef{{Package: cleanPkg, Name: "TestPositive"}}) {
		t.Errorf("the clean mutant's execution was not narrowed to TestPositive: %+v", positive.Executions)
	}

	// The dirty binary's mutant was widened to the whole binary and killed
	// there: it carries the binary but names no covering test.
	if guarded.Outcome != report.OutcomeKilled {
		t.Errorf("the dirty mutant is %s, want killed against the whole binary", guarded.Outcome)
	}
	if len(guarded.CoveringTests) != 0 {
		t.Errorf("the dirty mutant names covering tests %v, want none: it was widened", guarded.CoveringTests)
	}
	if want := []string{dirtyPkg}; !equalStrings(guarded.CoveringTestPackages, want) {
		t.Errorf("the dirty mutant's covering packages = %v, want %v", guarded.CoveringTestPackages, want)
	}
	for _, execution := range guarded.Executions {
		if len(execution.Tests) != 0 {
			t.Errorf("the dirty mutant's execution was narrowed to %v, want the whole binary", execution.Tests)
		}
	}
}

// warningWithCode returns the first warning carrying code, or nil.
func warningWithCode(r *report.Report, code string) *report.Warning {
	for i := range r.Warnings {
		if r.Warnings[i].Code == code {
			return &r.Warnings[i]
		}
	}
	return nil
}

// onlyMutant returns the one report mutant of the given package and rule,
// failing if the fixture does not hold exactly one.
func onlyMutant(t *testing.T, r *report.Report, pkg, rule string) report.Mutant {
	t.Helper()
	var found []report.Mutant
	for _, m := range r.Mutants {
		if m.Package == pkg && m.Rule == rule {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d %s mutants in %s, want 1", len(found), rule, pkg)
	}
	return found[0]
}

func equalRefs(got, want []report.TestRef) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
