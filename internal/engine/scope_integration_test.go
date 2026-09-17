// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testkit"
)

const (
	scopeCorePackage   = "fixture.example/coverage/core"
	scopeCallerPackage = "fixture.example/coverage/caller"
)

func TestScopedTestCommandBuildsAndRunsOnlyTheScopedPackage(t *testing.T) {
	t.Parallel()
	opts := options(t, "coverage")
	opts.TestArgv = []string{"go", "test", "./core/..."}

	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, OutputOf(err))
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	if warning, found := warningWith(events, Code(coverage.CodeCustomTestCommand)); found {
		t.Errorf("a scoped `go test` was treated as an opaque command: %s", warning.Message)
	}
	block := outcome.Report.Coverage
	if block.Mode != report.CoverageTest {
		t.Fatalf("coverage mode = %q, want %q for a command go-mutants can read", block.Mode, report.CoverageTest)
	}
	if block.Binaries == nil || *block.Binaries != 1 {
		t.Fatalf("coverage.binaries = %v, want only the scoped package's 1", block.Binaries)
	}

	want := map[string]struct {
		covering []string
		outcome  report.Outcome
		killedBy string
	}{
		"return-true v > 0":  {covering: []string{scopeCorePackage}, outcome: report.OutcomeKilled, killedBy: scopeCorePackage},
		"return-false v > 0": {covering: []string{scopeCorePackage}, outcome: report.OutcomeKilled, killedBy: scopeCorePackage},
		"gt-to-ge >":         {covering: []string{scopeCorePackage}, outcome: report.OutcomeKilled, killedBy: scopeCorePackage},

		"return-true a != b":  {covering: []string{}, outcome: report.OutcomeSurvived},
		"return-false a != b": {covering: []string{}, outcome: report.OutcomeSurvived},
		"neq-to-eq !=":        {covering: []string{}, outcome: report.OutcomeSurvived},

		"return-true core.Differs(a, b)":  {covering: []string{}, outcome: report.OutcomeSurvived},
		"return-false core.Differs(a, b)": {covering: []string{}, outcome: report.OutcomeSurvived},

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

func TestScopePatternThatMatchesNothingStopsBeforeTheBaseline(t *testing.T) {
	t.Parallel()
	opts := options(t, "coverage")
	testkit.WriteFile(t, filepath.Join(opts.WorkspaceRoot, "docs", "notes.md"), []byte("notes\n"))
	testkit.AgeTree(t, opts.WorkspaceRoot)

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

func TestScopeWithNoTestFilesInItIsRefused(t *testing.T) {
	t.Parallel()
	opts := options(t, "coverage")
	testkit.WriteSource(t, filepath.Join(opts.WorkspaceRoot, "extra"), "extra.go",
		"// Package extra is a real package with no test file in it.\n"+
			"package extra\n\n"+
			"// Sum adds two numbers.\n"+
			"func Sum(a, b int) int { return a + b }\n")
	testkit.AgeTree(t, opts.WorkspaceRoot)

	opts.TestArgv = []string{"go", "test", "./extra/..."}

	_, _, err := collect(t, t.Context(), opts)
	if code := CodeOf(err); code != CodeTestScope {
		t.Fatalf("code = %s, want %s: %v", code, CodeTestScope, err)
	}
	if !strings.Contains(err.Error(), "./extra/...") {
		t.Errorf("the refusal does not name the scope: %v", err)
	}
}
