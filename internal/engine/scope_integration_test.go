// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of the test-scope tests: what a `test.command` that
// names package patterns really does to a run.
//
// Everything interesting about scoping is invisible to a mock. Whether a binary
// was built is a file that exists; whether a suite ran is a process that
// started; whether coverage was collected is a profile a real toolchain wrote.
// So these run the real pipeline against the two-package coverage fixture and
// assert what came out of it.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/engine/...
package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/report"
)

// The coverage fixture's two test packages, which are the whole point of using
// it here: a scope can name one and leave the other out.
const (
	scopeCorePackage   = "fixture.example/coverage/core"
	scopeCallerPackage = "fixture.example/coverage/caller"
)

// TestScopedTestCommandBuildsAndRunsOnlyTheScopedPackage is the feature end to
// end, and it is deliberately asserted against the *same* fixture the unscoped
// coverage run uses, so the two tests read as one before-and-after.
//
// Unscoped, this module builds two test binaries and every mutant that any suite
// reaches is killed. Scoped to `./core/...`, only `core`'s binary is built —
// which is what a project whose `test.command` names three of forty packages is
// paying for — and the consequence is visible in the report rather than only in
// the clock: the mutants that were killed by `caller`'s suite are now uncovered
// survivors, because the suite that killed them is one this run was told not to
// count.
//
// That is the honest reading and not a loss of fidelity. A scoped command says
// which suites measure the project; a mutant those suites do not reach is one
// they would not have caught, and reporting it as an uncovered survivor is the
// same answer the run would reach by building every binary and watching the
// scoped ones pass.
func TestScopedTestCommandBuildsAndRunsOnlyTheScopedPackage(t *testing.T) {
	privateTempDir(t)
	opts := options(t, "coverage")
	opts.TestArgv = []string{"go", "test", "./core/..."}

	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, OutputOf(err))
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	// Recognised, so the optimisation is on rather than given up: a scoped
	// command is `go test` over package patterns, which go-mutants can attribute
	// coverage from because it built the binaries the patterns name.
	if warning, found := warningWith(events, Code(coverage.CodeCustomTestCommand)); found {
		t.Errorf("a scoped `go test` was treated as an opaque command: %s", warning.Message)
	}
	block := outcome.Report.Coverage
	if block.Mode != report.CoveragePackage {
		t.Fatalf("coverage mode = %q, want %q for a command go-mutants can read", block.Mode, report.CoveragePackage)
	}
	// One binary, where the unscoped run of this fixture reports two. This is
	// the assertion the whole feature lives or dies on: `caller`'s test binary
	// was never compiled, so its suite cannot have run against a single mutant.
	if block.Binaries == nil || *block.Binaries != 1 {
		t.Fatalf("coverage.binaries = %v, want only the scoped package's 1", block.Binaries)
	}

	// Each mutant and what the scope makes of it. Three are reached by `core`'s
	// own suite; the other eight are reached only from `caller`, or from
	// nowhere, and are uncovered survivors either way.
	//
	// The key is the rule and the bytes it rewrites, because a rule alone does
	// not name one mutant here: the return family fires on all four functions.
	want := map[string]struct {
		covering []string
		outcome  report.Outcome
		killedBy string
	}{
		"return-true v > 0":  {covering: []string{scopeCorePackage}, outcome: report.OutcomeKilled, killedBy: scopeCorePackage},
		"return-false v > 0": {covering: []string{scopeCorePackage}, outcome: report.OutcomeKilled, killedBy: scopeCorePackage},
		"gt-to-ge >":         {covering: []string{scopeCorePackage}, outcome: report.OutcomeKilled, killedBy: scopeCorePackage},

		// core.Differs is reached only by the caller package's suite, which this
		// scope leaves out. Unscoped these three are killed by that suite.
		"return-true a != b":  {covering: []string{}, outcome: report.OutcomeSurvived},
		"return-false a != b": {covering: []string{}, outcome: report.OutcomeSurvived},
		"neq-to-eq !=":        {covering: []string{}, outcome: report.OutcomeSurvived},

		// The caller package's own mutants. They are discovered, catalogued and
		// instrumented exactly as a whole-module run would — the scope is about
		// which suites measure, never about which code is mutated — and nothing
		// in the scope reaches them.
		"return-true core.Differs(a, b)":  {covering: []string{}, outcome: report.OutcomeSurvived},
		"return-false core.Differs(a, b)": {covering: []string{}, outcome: report.OutcomeSurvived},

		// Reached by nothing whatever the scope is.
		"return-true a < b":  {covering: []string{}, outcome: report.OutcomeSurvived},
		"return-false a < b": {covering: []string{}, outcome: report.OutcomeSurvived},
		"lt-to-le <":         {covering: []string{}, outcome: report.OutcomeSurvived},
	}
	if len(outcome.Report.Mutants) != len(want) {
		t.Fatalf("the catalogue holds %d mutants, want %d: %+v",
			len(outcome.Report.Mutants), len(want), outcome.Report.Mutants)
	}
	for _, m := range outcome.Report.Mutants {
		name := m.Rule + " " + m.Original
		expected, known := want[name]
		if !known {
			t.Errorf("unexpected mutant %s (%s)", m.DisplayID, name)
			continue
		}
		if !slices.Equal(m.CoveringTestPackages, expected.covering) {
			t.Errorf("%s is covered by %v, want %v", name, m.CoveringTestPackages, expected.covering)
		}
		if m.Outcome != expected.outcome {
			t.Errorf("%s is %s, want %s", name, m.Outcome, expected.outcome)
		}
		if m.Uncovered != (len(expected.covering) == 0) {
			t.Errorf("%s: uncovered = %t with covering %v", name, m.Uncovered, m.CoveringTestPackages)
		}
		killedBy := ""
		if m.KilledBy != nil {
			killedBy = *m.KilledBy
		}
		if killedBy != expected.killedBy {
			t.Errorf("%s was killed by %q, want %q", name, killedBy, expected.killedBy)
		}
		// The out-of-scope package never appears anywhere in the document, as
		// either a cover or a killer. It is the same claim as the binary count
		// above, made from the other end.
		if slices.Contains(m.CoveringTestPackages, scopeCallerPackage) || killedBy == scopeCallerPackage {
			t.Errorf("%s names the out-of-scope package %s", name, scopeCallerPackage)
		}
	}

	mapped, found := coverageMappedOf(events)
	if !found {
		t.Fatal("the run published no CoverageMapped event")
	}
	if mapped.Binaries != 1 || mapped.Covered != 3 || mapped.Uncovered != 8 {
		t.Errorf("CoverageMapped = %+v, want 1 binary, 3 covered, 8 uncovered", mapped)
	}
	// Eight mutants settled without a process, which is the saving stated as a
	// fact rather than as a stopwatch reading.
	started := 0
	for _, e := range events {
		if _, ok := e.(MutantStarted); ok {
			started++
		}
	}
	if started != 3 {
		t.Errorf("the run started %d mutants, want the 3 the scope covers", started)
	}

	document, readErr := os.ReadFile(published(t, events).RunPath)
	if readErr != nil {
		t.Fatalf("reading the filed report: %v", readErr)
	}
	validateDocument(t, document)
}

// TestScopePatternThatMatchesNothingStopsBeforeTheBaseline is the loud half of
// the feature.
//
// A pattern that names a directory with no Go packages in it is answered by the
// go command with a warning and an exit status of zero, so nothing downstream
// would notice: the baseline would pass by running nothing, the scope would
// silently shrink, and every mutant the missing suites cover would be reported
// as an uncovered survivor. There is no fail-open direction here — widening back
// to `./...` would run the suites the command excludes — so the run stops and
// names the pattern.
//
// It stops *before* the baseline, and that is asserted rather than assumed: a
// typo in a package pattern is a second's work to fix, and learning about it
// after a build, three timed test runs and an instrumentation pass would be
// several minutes spent to report it.
func TestScopePatternThatMatchesNothingStopsBeforeTheBaseline(t *testing.T) {
	privateTempDir(t)
	root := scopeWorkspace(t, "coverage")
	// A real directory that holds no Go package, which is what makes this the
	// exit-zero case rather than the missing-directory one.
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("creating the fixture's docs directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "notes.md"), []byte("notes\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture's docs file: %v", err)
	}

	opts := options(t, "coverage")
	opts.WorkspaceRoot = root
	opts.TestArgv = []string{"go", "test", "./core/...", "./docs/..."}

	outcome, _, err := collect(t, t.Context(), opts)
	if code := CodeOf(err); code != CodeTestScope {
		t.Fatalf("code = %s, want %s: %v", code, CodeTestScope, err)
	}
	if !strings.Contains(err.Error(), `"./docs/..."`) {
		t.Errorf("the refusal does not name the pattern that is wrong: %v", err)
	}
	if len(outcome.BaselineRuns) != 0 {
		t.Errorf("the run measured %d baseline runs before refusing the scope, want none",
			len(outcome.BaselineRuns))
	}
	if outcome.Status != StatusFailed {
		t.Errorf("status = %s, want %s", outcome.Status, StatusFailed)
	}
}

// TestScopeWithNoTestFilesInItIsRefused covers what patterns alone cannot.
//
// Every pattern named a real package and not one of them holds a test file, so
// the build produces no binary — and nothing downstream would say so. The
// coverage pass skips a run with no binaries in silence, the scheduler walks an
// empty list, every mutant comes back survived, and the run publishes a score of
// zero as though it had looked.
func TestScopeWithNoTestFilesInItIsRefused(t *testing.T) {
	privateTempDir(t)
	root := scopeWorkspace(t, "coverage")
	if err := os.MkdirAll(filepath.Join(root, "extra"), 0o755); err != nil {
		t.Fatalf("creating the fixture's extra package: %v", err)
	}
	source := "// SPDX-FileCopyrightText: 2026 go-mutants contributors\n" +
		"// SPDX-License-Identifier: MIT OR Apache-2.0\n\n" +
		"// Package extra is a real package with no test file in it.\n" +
		"package extra\n\n" +
		"// Sum adds two numbers.\n" +
		"func Sum(a, b int) int { return a + b }\n"
	if err := os.WriteFile(filepath.Join(root, "extra", "extra.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("writing the fixture's extra package: %v", err)
	}

	opts := options(t, "coverage")
	opts.WorkspaceRoot = root
	opts.TestArgv = []string{"go", "test", "./extra/..."}

	_, _, err := collect(t, t.Context(), opts)
	if code := CodeOf(err); code != CodeTestScope {
		t.Fatalf("code = %s, want %s: %v", code, CodeTestScope, err)
	}
	if !strings.Contains(err.Error(), "./extra/...") {
		t.Errorf("the refusal does not name the scope: %v", err)
	}
}

// scopeWorkspace copies a fixture module into a temporary directory, so that a
// test may add a file to it. The fixtures in the repository are read by every
// other integration test and are never written to.
func scopeWorkspace(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating the workspace: %v", err)
	}
	if err := os.CopyFS(root, os.DirFS(fixture(t, name))); err != nil {
		t.Fatalf("copying the %s fixture: %v", name, err)
	}
	return root
}
