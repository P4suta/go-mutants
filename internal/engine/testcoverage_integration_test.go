// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
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

// sharedModule is a mutant that survives the only test that covers its line but
// is killed, through a package variable, by a test that does not — the exact
// case RunOne's whole-binary confirmation exists for. Both tests pass on their
// own, so the binary is clean and the mutant is narrowed; the kill appears only
// when the whole binary runs.
const sharedModule = "fixture.example/shared"

const sharedSource = `package shared

// mode starts at the value TestModeIsOne expects, so that test passes on its
// own — the binary is clean and the mutant is narrowed rather than widened.
var mode = 1

// Enable sets mode from the sign of x. Its ` + "`>`" + ` is the mutant, and
// TestEnable calls it with 0: unmutated leaves mode at 1, the mutant makes it
// 2. TestEnable asserts nothing about mode, so it passes either way and is the
// only test that covers this line.
func Enable(x int) {
	if x > 0 {
		mode = 2
	} else {
		mode = 1
	}
}

// Mode reports the configured mode.
func Mode() int {
	return mode
}
`

const sharedSuite = `package shared

import "testing"

func TestEnable(t *testing.T) {
	Enable(0)
}

func TestModeIsOne(t *testing.T) {
	if Mode() != 1 {
		t.Fatalf("mode = %d, want 1", Mode())
	}
}
`

// TestANarrowedSurvivorIsKilledByTheWholeBinary is the whole-binary
// confirmation end to end: the `>` in Enable is covered only by TestEnable,
// which passes with the mutant active, so the narrowed run survives it; the
// whole binary kills it through TestModeIsOne, which reads the shared variable
// TestEnable left at 2 under the mutant. Without the confirmation this mutant
// would be a false survivor; with it the verdict matches a package-level run.
func TestANarrowedSurvivorIsKilledByTheWholeBinary(t *testing.T) {
	t.Parallel()

	root := testkit.NewModule(t).Module(sharedModule).
		Source("shared.go", sharedSource).
		Source("shared_test.go", sharedSuite).
		Root()
	opts := optionsAt(t, root)
	// A readable sink, so the two runs behind the confirmation can be checked:
	// the narrowed run and the whole-binary run are both recorded, even though
	// the report keeps only the authoritative one.
	sink := trace.NewMemorySink(0)
	opts.TraceSink = sink

	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("running the shared-state module: %v", err)
	}
	if outcome.Report == nil {
		t.Fatal("the run published no report")
	}
	if outcome.Report.Coverage.Mode != report.CoverageTest {
		t.Fatalf("coverage mode = %q, want %q", outcome.Report.Coverage.Mode, report.CoverageTest)
	}

	mutant := onlyMutant(t, outcome.Report, sharedModule, "gt-to-ge")
	if mutant.Outcome != report.OutcomeKilled {
		t.Fatalf("the shared-state mutant is %s, want killed by the whole-binary confirmation", mutant.Outcome)
	}
	// The confirmation runs the whole binary, so the reported execution names
	// no tests even though coverage found one test reaching the line.
	for _, execution := range mutant.Executions {
		if len(execution.Tests) != 0 {
			t.Errorf("the confirmed kill's execution was narrowed to %v, want the whole binary", execution.Tests)
		}
	}
	if want := []report.TestRef{{Package: sharedModule, Name: "TestEnable"}}; !equalRefs(mutant.CoveringTests, want) {
		t.Errorf("covering tests = %v, want %v: coverage still knows the one test that reaches the line", mutant.CoveringTests, want)
	}

	// The trace records both runs behind the one reported attempt: the narrowed
	// run, whose argv selects TestEnable, and the whole-binary confirmation,
	// whose argv selects nothing. The report keeps only the second; the trace
	// is where the fast-path survival that preceded the kill is visible.
	var narrowed, whole int
	for _, e := range sink.Events() {
		if e.Type != trace.TypeExec || e.Exec == nil || e.Exec.Kind != trace.ExecKindMutantRun || e.Exec.Subject != mutant.ID {
			continue
		}
		if slices.ContainsFunc(e.Exec.Argv, func(a string) bool { return strings.HasPrefix(a, "-test.run=") }) {
			narrowed++
		} else {
			whole++
		}
	}
	if narrowed != 1 || whole != 1 {
		t.Errorf("recorded %d narrowed and %d whole-binary runs of the mutant, want one of each", narrowed, whole)
	}
}
