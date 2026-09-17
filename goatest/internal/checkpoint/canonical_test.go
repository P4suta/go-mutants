// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package checkpoint_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/checkpoint"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const digestHexDigits = 64

func terminalResult(id string) checkpoint.MutationResult {
	return checkpoint.MutationResult{
		ID:       id,
		Evidence: []report.Evidence{{Kind: "mutation", ID: id, Status: "killed"}},
	}
}

func everyStructureCheckpoint(complete bool) checkpoint.State {
	state := checkpoint.State{
		Schema:      checkpoint.SchemaV1,
		InputDigest: strings.Repeat("a", digestHexDigits),
		Attempts:    1,
		Baseline: checkpoint.Baseline{
			Targets: []checkpoint.BaselineTarget{{
				ID: "t1", Executed: true,
				Inventory: report.TargetDisposition{
					ID: "t1", Name: "TestOne", Kind: "test", Package: "example.test/fixture", Status: "passed",
				},
				Target: &checkpoint.TargetEvidence{
					Target:       checkpoint.Target{ID: "t1"},
					Coverage:     &checkpoint.Coverage{Files: []checkpoint.FileCoverage{{Path: "a.go"}}},
					Instrumented: &checkpoint.Coverage{},
				},
			}},
			Suites: []checkpoint.BaselineSuite{{
				Package: "example.test/fixture", Measured: true,
				Covered:      &checkpoint.Coverage{},
				Instrumented: &checkpoint.Coverage{},
			}},
		},
		Race: &checkpoint.Race{},
		Mutation: &checkpoint.Mutation{
			CatalogFingerprint: strings.Repeat("b", digestHexDigits),
			Probe: &checkpoint.MutationProbe{
				IndexFingerprint: strings.Repeat("c", digestHexDigits),
				Targets:          []checkpoint.TargetProbe{{ID: "t1"}},
				Suites:           []checkpoint.SuiteProbe{{Package: "example.test/fixture"}},
			},
			Results: []checkpoint.MutationResult{terminalResult("m1")},
		},
	}
	if complete {
		state.Baseline.Complete = true
		state.Baseline.Suites = nil
		state.Baseline.Routing = &checkpoint.BaselineRouting{
			Suites: []checkpoint.SuiteCoverage{{Package: "example.test/fixture"}},
		}
	}
	return state
}

func nullPaths(t *testing.T, node any, path string, found *[]string) {
	t.Helper()
	switch typed := node.(type) {
	case nil:
		*found = append(*found, path)
	case map[string]any:
		for key, value := range typed {
			nullPaths(t, value, path+"."+key, found)
		}
	case []any:
		for index, value := range typed {
			nullPaths(t, value, path+"[]", found)
			_ = index
		}
	}
}

func bareCheckpoint() checkpoint.State {
	return checkpoint.State{
		Schema:      checkpoint.SchemaV1,
		InputDigest: strings.Repeat("a", digestHexDigits),
		Attempts:    1,
		Race:        &checkpoint.Race{},
		Mutation:    &checkpoint.Mutation{CatalogFingerprint: strings.Repeat("b", digestHexDigits)},
	}
}

func TestACheckpointWritesNoNullWhereACollectionBelongs(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		state checkpoint.State
	}{
		{name: "a checkpoint that has recorded nothing yet", state: bareCheckpoint()},
		{name: "a partial checkpoint", state: everyStructureCheckpoint(false)},
		{name: "a complete one", state: everyStructureCheckpoint(true)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var document any
			if err := json.Unmarshal(checkpoint.JSON(test.state), &document); err != nil {
				t.Fatal(err)
			}
			var found []string
			nullPaths(t, document, "checkpoint", &found)
			if len(found) != 0 {
				t.Fatalf("the checkpoint wrote null at %q, want an empty collection everywhere", found)
			}
		})
	}
}

func populatedCheckpoint(t *testing.T) checkpoint.State {
	t.Helper()
	state := everyStructureCheckpoint(false)
	state.Baseline.Evidence = []report.Evidence{{Kind: "baseline", ID: "e1", Status: "passed"}}
	state.Baseline.Findings = []report.Finding{{ID: "f1", Kind: "coverage", Summary: "unreached"}}
	state.Baseline.Targets = append(state.Baseline.Targets, checkpoint.BaselineTarget{
		ID: "t0", Executed: true,
		Inventory: report.TargetDisposition{
			ID: "t0", Name: "TestZero", Kind: "test", Package: "example.test/fixture", Status: "passed",
		},
	})
	first := &state.Baseline.Targets[0]
	first.Evidence = []report.Evidence{{Kind: "target", ID: "t1", Status: "passed"}}
	first.Findings = []report.Finding{{ID: "f2", Kind: "coverage", Summary: "unreached"}}
	first.Target.CoveredFiles = []string{"a.go"}
	first.Target.Environment = []string{"GOFLAGS"}
	first.Target.Probed = true
	first.Target.ProbeDurationNS = 1
	first.Target.Infected = []uint32{1, 2}
	first.Target.Target.Capabilities = []string{"postgres"}
	first.Target.Target.Dependencies = []string{"example.test/fixture/dep"}
	first.Target.Coverage.Files = []checkpoint.FileCoverage{
		{Path: "z.go", Blocks: []checkpoint.CoverageBlock{
			{StartLine: 9, StartColumn: 1, EndLine: 9, EndColumn: 2},
			{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 2},
		}},
		{Path: "a.go", Blocks: []checkpoint.CoverageBlock{{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 2}}},
	}
	first.Target.Instrumented.Files = []checkpoint.FileCoverage{
		{Path: "a.go", Blocks: []checkpoint.CoverageBlock{{StartLine: 1, StartColumn: 1, EndLine: 2, EndColumn: 1}}},
	}
	state.Baseline.Suites = append(state.Baseline.Suites,
		checkpoint.BaselineSuite{Package: "example.test/another"})
	state.Race = &checkpoint.Race{
		Packages: []string{"example.test/fixture"},
		Evidence: []report.Evidence{{Kind: "race", ID: "r1", Status: "passed"}},
		Findings: []report.Finding{{ID: "f3", Kind: "data-race", Summary: "raced"}},
	}
	state.Mutation.Results[0].Findings = []report.Finding{{ID: "f4", Kind: "survivor", MutantID: "m1"}}
	state.Mutation.Results = append(state.Mutation.Results, terminalResult("m0"))
	state.Mutation.Probe.Targets[0].Measured = true
	state.Mutation.Probe.Targets[0].Infected = []uint32{1, 3}
	state.Mutation.Probe.Targets = append(state.Mutation.Probe.Targets, checkpoint.TargetProbe{ID: "t0"})
	state.Mutation.Probe.Suites[0].Measured = true
	state.Mutation.Probe.Suites[0].Infected = []uint32{2, 4}
	state.Mutation.Probe.Suites = append(state.Mutation.Probe.Suites,
		checkpoint.SuiteProbe{Package: "example.test/another"})
	return state
}

func TestACheckpointKeepsEveryCollectionItWasGivenAndOrdersIt(t *testing.T) {
	t.Parallel()
	state := populatedCheckpoint(t)
	decoded, err := checkpoint.Decode(checkpoint.JSON(state))
	if err != nil {
		t.Fatalf("a checkpoint this test wrote cannot be read back: %v", err)
	}
	targetOf := func(state checkpoint.State) *checkpoint.TargetEvidence {
		for index := range state.Baseline.Targets {
			if state.Baseline.Targets[index].Target != nil {
				return state.Baseline.Targets[index].Target
			}
		}
		t.Fatal("the checkpoint carries no target evidence")
		return nil
	}
	for _, test := range []struct {
		name  string
		count func(checkpoint.State) int
	}{
		{name: "baseline evidence", count: func(s checkpoint.State) int { return len(s.Baseline.Evidence) }},
		{name: "baseline findings", count: func(s checkpoint.State) int { return len(s.Baseline.Findings) }},
		{name: "baseline targets", count: func(s checkpoint.State) int { return len(s.Baseline.Targets) }},
		{name: "baseline suites", count: func(s checkpoint.State) int { return len(s.Baseline.Suites) }},
		{
			name: "target evidence",
			count: func(s checkpoint.State) int {
				total := 0
				for _, unit := range s.Baseline.Targets {
					total += len(unit.Evidence)
				}
				return total
			},
		},
		{
			name: "target findings",
			count: func(s checkpoint.State) int {
				total := 0
				for _, unit := range s.Baseline.Targets {
					total += len(unit.Findings)
				}
				return total
			},
		},
		{name: "covered files", count: func(s checkpoint.State) int { return len(targetOf(s).CoveredFiles) }},
		{name: "environment names", count: func(s checkpoint.State) int { return len(targetOf(s).Environment) }},
		{name: "infections", count: func(s checkpoint.State) int { return len(targetOf(s).Infected) }},
		{
			name:  "target capabilities",
			count: func(s checkpoint.State) int { return len(targetOf(s).Target.Capabilities) },
		},
		{
			name:  "target dependencies",
			count: func(s checkpoint.State) int { return len(targetOf(s).Target.Dependencies) },
		},
		{name: "coverage files", count: func(s checkpoint.State) int { return len(targetOf(s).Coverage.Files) }},
		{
			name:  "instrumented files",
			count: func(s checkpoint.State) int { return len(targetOf(s).Instrumented.Files) },
		},
		{name: "race packages", count: func(s checkpoint.State) int { return len(s.Race.Packages) }},
		{name: "race evidence", count: func(s checkpoint.State) int { return len(s.Race.Evidence) }},
		{name: "race findings", count: func(s checkpoint.State) int { return len(s.Race.Findings) }},
		{name: "mutation results", count: func(s checkpoint.State) int { return len(s.Mutation.Results) }},
		{
			name: "mutation result evidence",
			count: func(s checkpoint.State) int {
				total := 0
				for _, unit := range s.Mutation.Results {
					total += len(unit.Evidence)
				}
				return total
			},
		},
		{
			name: "mutation result findings",
			count: func(s checkpoint.State) int {
				total := 0
				for _, unit := range s.Mutation.Results {
					total += len(unit.Findings)
				}
				return total
			},
		},
		{name: "probe targets", count: func(s checkpoint.State) int { return len(s.Mutation.Probe.Targets) }},
		{name: "probe suites", count: func(s checkpoint.State) int { return len(s.Mutation.Probe.Suites) }},
		{
			name: "probe target infections",
			count: func(s checkpoint.State) int {
				total := 0
				for _, target := range s.Mutation.Probe.Targets {
					total += len(target.Infected)
				}
				return total
			},
		},
		{
			name: "probe suite infections",
			count: func(s checkpoint.State) int {
				total := 0
				for _, suite := range s.Mutation.Probe.Suites {
					total += len(suite.Infected)
				}
				return total
			},
		},
	} {
		if got, want := test.count(decoded), test.count(state); got != want {
			t.Errorf("the checkpoint read back %d %s, want %d", got, test.name, want)
		}
	}

	if got := []string{decoded.Baseline.Targets[0].ID, decoded.Baseline.Targets[1].ID}; got[0] != "t0" || got[1] != "t1" {
		t.Errorf("baseline targets read back as %q, want them in identity order", got)
	}
	if decoded.Baseline.Suites[0].Package != "example.test/another" {
		t.Errorf("baseline suites read back starting at %q, want them in package order",
			decoded.Baseline.Suites[0].Package)
	}
	if decoded.Mutation.Results[0].ID != "m0" {
		t.Errorf("mutation results read back starting at %q, want them in identity order",
			decoded.Mutation.Results[0].ID)
	}
	if decoded.Mutation.Probe.Targets[0].ID != "t0" {
		t.Errorf("probe targets read back starting at %q, want them in identity order",
			decoded.Mutation.Probe.Targets[0].ID)
	}
	if decoded.Mutation.Probe.Suites[0].Package != "example.test/another" {
		t.Errorf("probe suites read back starting at %q, want them in package order",
			decoded.Mutation.Probe.Suites[0].Package)
	}
	files := targetOf(decoded).Coverage.Files
	if files[0].Path != "a.go" || files[1].Path != "z.go" {
		t.Fatalf("coverage files read back as %+v, want them in path order", files)
	}
	if blocks := files[1].Blocks; blocks[0].StartLine != 1 {
		t.Fatalf("coverage blocks read back as %+v, want them in position order", blocks)
	}
}
