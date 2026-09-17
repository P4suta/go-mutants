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

func TestACheckpointKeepsEveryCollectionItWasGivenAndOrdersIt(t *testing.T) {
	t.Parallel()
	state := everyStructureCheckpoint(false)
	state.Baseline.Targets = append(state.Baseline.Targets, checkpoint.BaselineTarget{
		ID: "t0", Executed: true,
		Inventory: report.TargetDisposition{
			ID: "t0", Name: "TestZero", Kind: "test", Package: "example.test/fixture", Status: "passed",
		},
	})
	state.Baseline.Suites = append(state.Baseline.Suites,
		checkpoint.BaselineSuite{Package: "example.test/another"})
	state.Mutation.Results = append(state.Mutation.Results, terminalResult("m0"))
	state.Mutation.Probe.Targets = append(state.Mutation.Probe.Targets, checkpoint.TargetProbe{ID: "t0"})
	state.Mutation.Probe.Suites = append(state.Mutation.Probe.Suites,
		checkpoint.SuiteProbe{Package: "example.test/another"})
	state.Baseline.Targets[0].Target.Coverage.Files = []checkpoint.FileCoverage{
		{Path: "z.go", Blocks: []checkpoint.CoverageBlock{
			{StartLine: 9, StartColumn: 1, EndLine: 9, EndColumn: 2},
			{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 2},
		}},
		{Path: "a.go"},
	}

	decoded, err := checkpoint.Decode(checkpoint.JSON(state))
	if err != nil {
		t.Fatalf("a checkpoint this test wrote cannot be read back: %v", err)
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
	files := decoded.Baseline.Targets[1].Target.Coverage.Files
	if len(files) != 2 || files[0].Path != "a.go" || files[1].Path != "z.go" {
		t.Fatalf("coverage files read back as %+v, want them in path order", files)
	}
	if blocks := files[1].Blocks; len(blocks) != 2 || blocks[0].StartLine != 1 {
		t.Fatalf("coverage blocks read back as %+v, want them in position order", blocks)
	}
}
