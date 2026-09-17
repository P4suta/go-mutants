// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package checkpoint_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/checkpoint"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const (
	firstInfection  = 1
	secondInfection = 2
	thirdInfection  = 3
	fourthInfection = 4

	laterLine     = 2
	laterColumn   = 4
	earlierColumn = 2
)

func validCheckpoint() checkpoint.State {
	state := everyStructureCheckpoint(false)
	target := state.Baseline.Targets[0].Target
	target.Probed = true
	target.ProbeDurationNS = 1
	target.Infected = []uint32{firstInfection, secondInfection}
	target.Coverage.Files = []checkpoint.FileCoverage{{
		Path:   "a.go",
		Blocks: []checkpoint.CoverageBlock{{StartLine: 1, StartColumn: 1, EndLine: 2, EndColumn: 1}},
	}}
	state.Mutation.Probe.Targets[0].Measured = true
	state.Mutation.Probe.Targets[0].Infected = []uint32{firstInfection, thirdInfection}
	state.Mutation.Probe.Suites[0].Measured = true
	state.Mutation.Probe.Suites[0].Infected = []uint32{secondInfection, fourthInfection}
	return state
}

func TestACheckpointIsValidatedFieldByFieldAndSaysWhichOneFailed(t *testing.T) {
	t.Parallel()
	if err := checkpoint.Validate(validCheckpoint()); err != nil {
		t.Fatalf("the fixture this test breaks one field of was rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*checkpoint.State)
		want   string
	}{
		{
			name:   "a schema from another version",
			change: func(s *checkpoint.State) { s.Schema = "assurance-checkpoint-v2" }, want: "checkpoint schema",
		},
		{
			name:   "an input digest that is not one",
			change: func(s *checkpoint.State) { s.InputDigest = "abc" }, want: "lowercase SHA-256",
		},
		{
			name:   "a checkpoint of no attempt",
			change: func(s *checkpoint.State) { s.Attempts = 0 }, want: "attempts must be positive",
		},
		{
			name:   "a baseline target with no identity",
			change: func(s *checkpoint.State) { s.Baseline.Targets[0].ID = "" }, want: "invalid identity",
		},
		{
			name:   "a baseline target whose inventory names another",
			change: func(s *checkpoint.State) { s.Baseline.Targets[0].Inventory.ID = "other" },
			want:   "invalid identity",
		},
		{
			name:   "a baseline target with no name",
			change: func(s *checkpoint.State) { s.Baseline.Targets[0].Inventory.Name = "" }, want: "invalid identity",
		},
		{
			name:   "a baseline target with no status",
			change: func(s *checkpoint.State) { s.Baseline.Targets[0].Inventory.Status = "" }, want: "invalid identity",
		},
		{
			name:   "a baseline target that took less than no time",
			change: func(s *checkpoint.State) { s.Baseline.Targets[0].Inventory.DurationMS = -1 },
			want:   "invalid identity",
		},
		{
			name:   "a baseline target that neither ran nor stepped aside",
			change: func(s *checkpoint.State) { s.Baseline.Targets[0].Executed = false },
			want:   "not completely classified",
		},
		{
			name:   "a baseline target that both ran and stepped aside",
			change: func(s *checkpoint.State) { s.Baseline.Targets[0].Skipped = true },
			want:   "not completely classified",
		},
		{
			name: "two baseline targets of one identity",
			change: func(s *checkpoint.State) {
				s.Baseline.Targets = append(s.Baseline.Targets, s.Baseline.Targets[0])
				s.Baseline.Targets[1].Target = nil
			},
			want: "duplicate baseline target",
		},
		{
			name:   "target evidence that names another target",
			change: func(s *checkpoint.State) { s.Baseline.Targets[0].Target.Target.ID = "other" },
			want:   "invalid identity",
		},
		{
			name:   "target evidence that took less than no time",
			change: func(s *checkpoint.State) { s.Baseline.Targets[0].Target.DurationNS = -1 },
			want:   "invalid identity",
		},
		{
			name:   "target evidence probed for less than no time",
			change: func(s *checkpoint.State) { s.Baseline.Targets[0].Target.ProbeDurationNS = -1 },
			want:   "invalid identity",
		},
		{
			name: "target evidence nothing probed that names a probe duration",
			change: func(s *checkpoint.State) {
				s.Baseline.Targets[0].Target.Probed = false
				s.Baseline.Targets[0].Target.Infected = nil
			},
			want: "invalid identity",
		},
		{
			name: "target evidence nothing probed that names an infection",
			change: func(s *checkpoint.State) {
				s.Baseline.Targets[0].Target.Probed = false
				s.Baseline.Targets[0].Target.ProbeDurationNS = 0
			},
			want: "invalid identity",
		},
		{
			name: "target infections that repeat",
			change: func(s *checkpoint.State) {
				s.Baseline.Targets[0].Target.Infected = []uint32{firstInfection, firstInfection}
			},
			want: "invalid identity",
		},
		{
			name: "target infections out of order",
			change: func(s *checkpoint.State) {
				s.Baseline.Targets[0].Target.Infected = []uint32{secondInfection, firstInfection}
			},
			want: "invalid identity",
		},
		{
			name:   "target evidence with no exact coverage",
			change: func(s *checkpoint.State) { s.Baseline.Targets[0].Target.Coverage = nil },
			want:   "no exact coverage",
		},
		{
			name: "coverage of a file with no path",
			change: func(s *checkpoint.State) {
				s.Baseline.Targets[0].Target.Coverage.Files[0].Path = ""
			},
			want: "empty file path",
		},
		{
			name: "a coverage block that starts on line zero",
			change: func(s *checkpoint.State) {
				s.Baseline.Targets[0].Target.Coverage.Files[0].Blocks[0].StartLine = 0
			},
			want: "positions must be positive",
		},
		{
			name: "a coverage block that starts in column zero",
			change: func(s *checkpoint.State) {
				s.Baseline.Targets[0].Target.Coverage.Files[0].Blocks[0].StartColumn = 0
			},
			want: "positions must be positive",
		},
		{
			name: "a coverage block that ends on line zero",
			change: func(s *checkpoint.State) {
				s.Baseline.Targets[0].Target.Coverage.Files[0].Blocks[0].EndLine = 0
			},
			want: "positions must be positive",
		},
		{
			name: "a coverage block that ends in column zero",
			change: func(s *checkpoint.State) {
				s.Baseline.Targets[0].Target.Coverage.Files[0].Blocks[0].EndColumn = 0
			},
			want: "positions must be positive",
		},
		{
			name: "a coverage block that ends on an earlier line",
			change: func(s *checkpoint.State) {
				s.Baseline.Targets[0].Target.Coverage.Files[0].Blocks[0].EndLine = 1
				s.Baseline.Targets[0].Target.Coverage.Files[0].Blocks[0].StartLine = laterLine
			},
			want: "ends before it starts",
		},
		{
			name: "a coverage block that ends in an earlier column of its line",
			change: func(s *checkpoint.State) {
				s.Baseline.Targets[0].Target.Coverage.Files[0].Blocks[0].StartLine = 1
				s.Baseline.Targets[0].Target.Coverage.Files[0].Blocks[0].StartColumn = laterColumn
				s.Baseline.Targets[0].Target.Coverage.Files[0].Blocks[0].EndLine = 1
				s.Baseline.Targets[0].Target.Coverage.Files[0].Blocks[0].EndColumn = earlierColumn
			},
			want: "ends before it starts",
		},
		{
			name: "two instrumentation anchors for one package",
			change: func(s *checkpoint.State) {
				second := s.Baseline.Targets[0]
				second.ID = "t2"
				second.Inventory.ID = "t2"
				evidence := *s.Baseline.Targets[0].Target
				evidence.Target.ID = "t2"
				second.Target = &evidence
				s.Baseline.Targets = append(s.Baseline.Targets, second)
			},
			want: "instrumentation anchors",
		},
		{
			name:   "a partial baseline suite for no package",
			change: func(s *checkpoint.State) { s.Baseline.Suites[0].Package = "" }, want: "invalid measurement",
		},
		{
			name:   "a partial baseline suite that took less than no time",
			change: func(s *checkpoint.State) { s.Baseline.Suites[0].DurationNS = -1 }, want: "invalid measurement",
		},
		{
			name: "a partial baseline suite nothing measured that carries coverage",
			change: func(s *checkpoint.State) {
				s.Baseline.Suites[0].Measured = false
				s.Baseline.Suites[0].Instrumented = nil
			},
			want: "invalid measurement",
		},
		{
			name: "a partial baseline suite nothing measured that carries instrumentation",
			change: func(s *checkpoint.State) {
				s.Baseline.Suites[0].Measured = false
				s.Baseline.Suites[0].Covered = nil
			},
			want: "invalid measurement",
		},
		{
			name:   "a measured partial baseline suite with no coverage",
			change: func(s *checkpoint.State) { s.Baseline.Suites[0].Covered = nil },
			want:   "invalid measurement",
		},
		{
			name:   "a measured partial baseline suite with no instrumentation",
			change: func(s *checkpoint.State) { s.Baseline.Suites[0].Instrumented = nil },
			want:   "invalid measurement",
		},
		{
			name: "two partial baseline suites for one package",
			change: func(s *checkpoint.State) {
				s.Baseline.Suites = append(s.Baseline.Suites, s.Baseline.Suites[0])
			},
			want: "duplicate partial baseline suite",
		},
		{
			name:   "a race package that names nothing",
			change: func(s *checkpoint.State) { s.Race.Packages = []string{""} }, want: "race package is empty",
		},
		{
			name: "one race package named twice",
			change: func(s *checkpoint.State) {
				s.Race.Packages = []string{"example.test/fixture", "example.test/fixture"}
			},
			want: "duplicate race package",
		},
		{
			name:   "a catalog fingerprint that is not one",
			change: func(s *checkpoint.State) { s.Mutation.CatalogFingerprint = "abc" },
			want:   "catalog fingerprint is invalid",
		},
		{
			name:   "an index fingerprint that is not one",
			change: func(s *checkpoint.State) { s.Mutation.Probe.IndexFingerprint = "abc" },
			want:   "index fingerprint is invalid",
		},
		{
			name:   "a probed target with no identity",
			change: func(s *checkpoint.State) { s.Mutation.Probe.Targets[0].ID = "" },
			want:   "target has invalid measurement",
		},
		{
			name:   "a probed target that took less than no time",
			change: func(s *checkpoint.State) { s.Mutation.Probe.Targets[0].DurationNS = -1 },
			want:   "target has invalid measurement",
		},
		{
			name: "an unmeasured probed target that names a duration",
			change: func(s *checkpoint.State) {
				s.Mutation.Probe.Targets[0].Measured = false
				s.Mutation.Probe.Targets[0].DurationNS = 1
				s.Mutation.Probe.Targets[0].Infected = nil
			},
			want: "target has invalid measurement",
		},
		{
			name:   "an unmeasured probed target that names an infection",
			change: func(s *checkpoint.State) { s.Mutation.Probe.Targets[0].Measured = false },
			want:   "target has invalid measurement",
		},
		{
			name: "two probed targets of one identity",
			change: func(s *checkpoint.State) {
				s.Mutation.Probe.Targets = append(s.Mutation.Probe.Targets, s.Mutation.Probe.Targets[0])
			},
			want: "duplicate target",
		},
		{
			name: "probed target infections out of order",
			change: func(s *checkpoint.State) {
				s.Mutation.Probe.Targets[0].Infected = []uint32{thirdInfection, firstInfection}
			},
			want: "infections are not ascending",
		},
		{
			name:   "a probed suite for no package",
			change: func(s *checkpoint.State) { s.Mutation.Probe.Suites[0].Package = "" },
			want:   "suite has invalid measurement",
		},
		{
			name:   "a probed suite that took less than no time",
			change: func(s *checkpoint.State) { s.Mutation.Probe.Suites[0].DurationNS = -1 },
			want:   "suite has invalid measurement",
		},
		{
			name: "an unmeasured probed suite that names a duration",
			change: func(s *checkpoint.State) {
				s.Mutation.Probe.Suites[0].Measured = false
				s.Mutation.Probe.Suites[0].DurationNS = 1
				s.Mutation.Probe.Suites[0].Infected = nil
			},
			want: "suite has invalid measurement",
		},
		{
			name:   "an unmeasured probed suite that names an infection",
			change: func(s *checkpoint.State) { s.Mutation.Probe.Suites[0].Measured = false },
			want:   "suite has invalid measurement",
		},
		{
			name: "an unmeasured probed suite that claims the whole tree",
			change: func(s *checkpoint.State) {
				s.Mutation.Probe.Suites[0].Measured = false
				s.Mutation.Probe.Suites[0].Infected = nil
				s.Mutation.Probe.Suites[0].WholeTree = true
			},
			want: "suite has invalid measurement",
		},
		{
			name: "two probed suites for one package",
			change: func(s *checkpoint.State) {
				s.Mutation.Probe.Suites = append(s.Mutation.Probe.Suites, s.Mutation.Probe.Suites[0])
			},
			want: "duplicate suite",
		},
		{
			name: "probed suite infections that repeat",
			change: func(s *checkpoint.State) {
				s.Mutation.Probe.Suites[0].Infected = []uint32{secondInfection, secondInfection}
			},
			want: "infections are not ascending",
		},
		{
			name:   "a mutation result with no identity",
			change: func(s *checkpoint.State) { s.Mutation.Results[0].ID = "" }, want: "not terminal",
		},
		{
			name: "a mutation result nothing concluded",
			change: func(s *checkpoint.State) {
				s.Mutation.Results[0].Evidence = []report.Evidence{{Kind: "mutation", ID: "m1", Status: "survived"}}
			},
			want: "not terminal",
		},
		{
			name: "two mutation results of one identity",
			change: func(s *checkpoint.State) {
				s.Mutation.Results = append(s.Mutation.Results, s.Mutation.Results[0])
			},
			want: "duplicate mutant",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := validCheckpoint()
			test.change(&state)
			err := checkpoint.Validate(state)
			if err == nil {
				t.Fatalf("Validate accepted a checkpoint with %s", test.name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate reported %v, want it to say %q", err, test.want)
			}
		})
	}
}

func TestAMutationResultIsTerminalWhenItsEvidenceOrAFindingSaysSo(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		evidence []report.Evidence
		findings []report.Finding
		want     bool
	}{
		{
			name:     "a mutant that was killed",
			evidence: []report.Evidence{{Kind: "mutation", ID: "m1", Status: "killed"}}, want: true,
		},
		{
			name:     "a mutant the compiler rejected",
			evidence: []report.Evidence{{Kind: "mutation", ID: "m1", Status: "compile-rejected"}}, want: true,
		},
		{
			name:     "a mutant somebody accepted",
			evidence: []report.Evidence{{Kind: "mutation", ID: "m1", Status: "accepted"}}, want: true,
		},
		{
			name:     "a mutant that survived",
			evidence: []report.Evidence{{Kind: "mutation", ID: "m1", Status: "survived"}},
		},
		{
			name:     "evidence of another kind entirely",
			evidence: []report.Evidence{{Kind: "baseline", ID: "m1", Status: "killed"}},
		},
		{
			name:     "evidence about another mutant",
			evidence: []report.Evidence{{Kind: "mutation", ID: "m2", Status: "killed"}},
		},
		{
			name:     "a finding about this mutant",
			findings: []report.Finding{{ID: "f1", MutantID: "m1"}}, want: true,
		},
		{
			name:     "a finding about another mutant",
			findings: []report.Finding{{ID: "f1", MutantID: "m2"}},
		},
		{name: "nothing at all"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := validCheckpoint()
			state.Mutation.Results = []checkpoint.MutationResult{
				{ID: "m1", Evidence: test.evidence, Findings: test.findings},
			}
			err := checkpoint.Validate(state)
			if (err == nil) != test.want {
				t.Fatalf("Validate = %v, want the result to be terminal: %t", err, test.want)
			}
		})
	}
}
