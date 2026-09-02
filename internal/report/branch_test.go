// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
)

// branchRunID is this fixture's own run identity. It is a run of its own rather
// than a variation of the golden ones, because a branch proof belongs only on
// an edit that really does narrow a condition, and none of the candidates those
// fixtures were written around does.
const branchRunID = "20260218T101500Z-9c3e"

// branchCandidates are two mutants in one file: an edit that narrows the
// condition of an `if`, which discovery proved a body span for, and one beside
// it that does not.
var branchCandidates = []candidate{
	{
		path: coreFile, pkg: corePackage, rule: "le-to-lt",
		start: 64, original: "<=", replacement: "<", line: 4, column: 5,
		outcome: mutation.OutcomeSurvived, attempts: 1, duration: 90 * time.Millisecond,
		branch: &discover.BranchProof{
			Direction:       discover.BranchDecreasing,
			BodyStartLine:   4,
			BodyStartColumn: 12,
			BodyEndLine:     6,
			BodyEndColumn:   2,
		},
	},
	{
		path: coreFile, pkg: corePackage, rule: "lt-to-le",
		start: 120, original: "<", replacement: "<=", line: 9, column: 5,
		outcome: mutation.OutcomeKilled, killedBy: corePackage, attempts: 1,
		duration: 140 * time.Millisecond, tail: "--- FAIL: TestClamp (0.00s)",
	},
}

// TestBranchProofSurvivesIntoTheRunReport carries discovery's proof through
// [report.Build] and into the published document.
//
// The absence is asserted on the encoded bytes rather than on the struct,
// because `branch` is an optional property: a mutant nothing was proved about
// has to carry no key at all, and a `null` would be a different document to
// everybody's decoder.
func TestBranchProofSurvivesIntoTheRunReport(t *testing.T) {
	t.Parallel()

	doc, err := buildBranchFixture(t).Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := schemas.Validate(schemas.RunReportV1, doc); err != nil {
		t.Fatalf("the document does not satisfy %s: %v\n%s", schemas.RunReportV1, err, doc)
	}

	var decoded struct {
		Mutants []struct {
			Rule   string         `json:"rule"`
			Branch *report.Branch `json:"branch"`
		} `json:"mutants"`
	}
	if err := json.Unmarshal(doc, &decoded); err != nil {
		t.Fatalf("decoding the document: %v", err)
	}
	if len(decoded.Mutants) != len(branchCandidates) {
		t.Fatalf("document holds %d mutants, want %d", len(decoded.Mutants), len(branchCandidates))
	}
	proved, plain := decoded.Mutants[0], decoded.Mutants[1]
	if proved.Rule != "le-to-lt" || plain.Rule != "lt-to-le" {
		t.Fatalf("the fixture no longer lists le-to-lt before lt-to-le: %s then %s", proved.Rule, plain.Rule)
	}
	want := report.Branch{
		Direction:       discover.BranchDecreasing,
		BodyStartLine:   4,
		BodyStartColumn: 12,
		BodyEndLine:     6,
		BodyEndColumn:   2,
	}
	if proved.Branch == nil || *proved.Branch != want {
		t.Errorf("branch = %+v, want %+v", proved.Branch, want)
	}
	if plain.Branch != nil {
		t.Errorf("the unproved mutant carries a branch: %+v", *plain.Branch)
	}

	var keys struct {
		Mutants []map[string]any `json:"mutants"`
	}
	if err := json.Unmarshal(doc, &keys); err != nil {
		t.Fatalf("decoding the document: %v", err)
	}
	if _, ok := keys.Mutants[1]["branch"]; ok {
		t.Error("the unproved mutant has a branch key, want the property omitted")
	}
}

// buildBranchFixture builds the two-mutant run above.
func buildBranchFixture(t *testing.T) *report.Report {
	t.Helper()
	located, catalog := located(t, branchCandidates)
	mutants := catalog.Mutants()

	results := make([]report.MutantResult, 0, len(mutants))
	for i, m := range mutants {
		c := branchCandidates[i]
		results = append(results, report.MutantResult{
			ID:         m.ID,
			Outcome:    c.outcome,
			Duration:   c.duration,
			KilledBy:   c.killedBy,
			Attempts:   c.attempts,
			OutputTail: c.tail,
		})
	}
	started, err := time.Parse(time.RFC3339, coverageStartedAt)
	if err != nil {
		t.Fatalf("parsing the fixture clock: %v", err)
	}
	built, err := report.Build(report.Options{
		ToolVersion:     fixtureToolVersion,
		RunID:           branchRunID,
		Status:          report.StatusCompleted,
		Started:         started,
		Finished:        started.Add(fixtureDuration),
		Config:          config.Defaults(),
		Mode:            report.ModeAll,
		Selected:        len(results),
		ModulePath:      fixtureModulePath,
		GoVersion:       fixtureGoVersion,
		WorkspaceDigest: fixtureDigest,
		Platform:        report.Platform{OS: "linux", Arch: "amd64"},
		Catalog:         catalog,
		Located:         located,
		Results:         results,
		TestCommand:     []string{"go", "test", "./..."},
		Baseline:        []time.Duration{900 * time.Millisecond},
		Timeout:         10 * time.Second,
		TimeoutSource:   report.TimeoutDerived,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return built
}
