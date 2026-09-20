// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const (
	oneMoreThanDiscovered = 7
	oneFewerThanSelected  = 4
	twiceTheKills         = 2
	everyMutantSelected   = 6
)

func mutantFixture() report.Report {
	value := auditedFixture()
	value.Verdict = report.VerdictInsufficient
	value.Mutants = []report.MutantDisposition{
		{ID: "m1", Status: report.MutantKilled},
		{ID: "m2", Status: report.MutantSurvived},
		{ID: "m3", Status: report.MutantInconclusive},
		{ID: "m4", Status: report.MutantCompileRejected},
		{ID: "m5", Status: report.MutantAccepted, Detail: "accepted-fixture"},
		{ID: "m6", Status: report.MutantOutOfScope},
	}
	value.Accounting.Mutants = report.MutantAccounting{
		Discovered: 6, Selected: 5, Executed: 3,
		Killed: 1, Survived: 1, Inconclusive: 1,
		CompileRejected: 1, Accepted: 1, OutOfScope: 1,
	}
	return value
}

func TestEveryMutantDispositionIsCountedInItsOwnColumn(t *testing.T) {
	t.Parallel()
	if err := report.Validate(mutantFixture()); err != nil {
		t.Fatalf("the fixture this test breaks one field of was rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*report.Report)
		want   string
	}{
		{
			name:   "a killed mutant counted as survived",
			change: func(value *report.Report) { value.Mutants[0].Status = report.MutantSurvived },
			want:   "does not match accounting",
		},
		{
			name:   "an inconclusive mutant counted as killed",
			change: func(value *report.Report) { value.Mutants[2].Status = report.MutantKilled },
			want:   "does not match accounting",
		},
		{
			name: "a compile-rejected mutant counted as accepted",
			change: func(value *report.Report) {
				value.Mutants[3].Status = report.MutantAccepted
				value.Mutants[3].Detail = "accepted-fixture"
			},
			want: "does not match accounting",
		},
		{
			name:   "an out-of-scope mutant counted as selected",
			change: func(value *report.Report) { value.Mutants[5].Status = report.MutantUnknown },
			want:   "does not match accounting",
		},
		{
			name:   "a disposition no run produces",
			change: func(value *report.Report) { value.Mutants[0].Status = "vanished" },
			want:   "unknown disposition",
		},
		{
			name:   "a mutant with no identity",
			change: func(value *report.Report) { value.Mutants[0].ID = "" }, want: "empty ID",
		},
		{
			name:   "a mutant on a line before the first",
			change: func(value *report.Report) { value.Mutants[0].Line = -1 }, want: "negative source line",
		},
		{
			name:   "two mutants of one identity",
			change: func(value *report.Report) { value.Mutants[1].ID = "m1" }, want: "duplicate ID",
		},
		{
			name: "an inventory shorter than the discovery",
			change: func(value *report.Report) {
				value.Mutants = value.Mutants[:len(value.Mutants)-1]
			},
			want: "inventory mismatch",
		},
		{
			name:   "a discovery that is not every disposition",
			change: func(value *report.Report) { value.Accounting.Mutants.Discovered = oneMoreThanDiscovered },
			want:   "disposition mismatch",
		},
		{
			name:   "a selection that is not every executed and set-aside mutant",
			change: func(value *report.Report) { value.Accounting.Mutants.Selected = oneFewerThanSelected },
			want:   "selection mismatch",
		},
		{
			name:   "an execution that is not every outcome",
			change: func(value *report.Report) { value.Accounting.Mutants.Killed = twiceTheKills },
			want:   "execution mismatch",
		},
		{
			name:   "a negative count",
			change: func(value *report.Report) { value.Accounting.Mutants.OutOfScope = -1 },
			want:   "negative count",
		},
		{
			name: "more reuse than execution",
			change: func(value *report.Report) {
				value.Accounting.Mutants.ReusedKilled = twiceTheKills
				value.Accounting.Mutants.ReusedSurvived = twiceTheKills
			},
			want: "reused 4 of 3 executions",
		},
		{
			name: "an unexplained mutant without an ERROR verdict",
			change: func(value *report.Report) {
				value.Mutants[5].Status = report.MutantUnknown
				value.Accounting.Mutants.OutOfScope = 0
				value.Accounting.Mutants.Unknown = 1
				value.Accounting.Mutants.Selected = everyMutantSelected
			},
			want: "ERROR verdict",
		},
		{
			name:   "an accepted mutant nobody accepted",
			change: func(value *report.Report) { value.Mutants[4].Detail = "unaccepted" },
			want:   "acceptance metadata",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := mutantFixture()
			test.change(&input)
			err := report.Validate(input)
			if err == nil {
				t.Fatalf("Validate accepted a report with %s", test.name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate reported %v, want it to say %q", err, test.want)
			}
		})
	}
}

func TestAReusedMutantCarriesTheProvenanceOfADispositionARunExecutes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status report.MutantStatus
		reused bool
		origin string
		want   string
	}{
		{name: "a killed mutant reused with its origin", status: report.MutantKilled, reused: true, origin: "run-a"},
		{name: "a survived mutant reused with its origin", status: report.MutantSurvived, reused: true, origin: "run-a"},
		{
			name:   "an inconclusive mutant reused with its origin",
			status: report.MutantInconclusive, reused: true, origin: "run-a",
		},
		{
			name:   "an accepted mutant reused with its origin",
			status: report.MutantAccepted, reused: true, origin: "run-a",
		},
		{
			name: "a compile-rejected mutant nothing executes", status: report.MutantCompileRejected,
			reused: true, origin: "run-a", want: "which no run executes",
		},
		{
			name: "an out-of-scope mutant nothing executes", status: report.MutantOutOfScope,
			reused: true, origin: "run-a", want: "which no run executes",
		},
		{
			name: "an unknown mutant nothing executes", status: report.MutantUnknown,
			reused: true, origin: "run-a", want: "which no run executes",
		},
		{
			name: "a mutant reused with no origin", status: report.MutantKilled,
			reused: true, want: "reuse and provenance disagree",
		},
		{
			name: "an origin without the reuse it belongs to", status: report.MutantKilled,
			origin: "run-a", want: "reuse and provenance disagree",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := mutantFixture()
			input.Mutants = []report.MutantDisposition{{
				ID: "m1", Status: test.status, Reused: test.reused, Provenance: test.origin,
				Detail: "accepted-fixture",
			}}
			input.Accounting.Mutants = mutantAccountingFor(test.status, test.reused)
			if test.status == report.MutantUnknown {
				input.Verdict = report.VerdictError
			}
			err := report.Validate(input)
			switch {
			case test.want == "" && err != nil:
				t.Fatalf("Validate rejected %s: %v", test.name, err)
			case test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)):
				t.Fatalf("Validate reported %v, want it to say %q", err, test.want)
			}
		})
	}
}

func mutantAccountingFor(status report.MutantStatus, reused bool) report.MutantAccounting {
	count := report.MutantAccounting{Discovered: 1, Selected: 1}
	switch status {
	case report.MutantKilled:
		count.Executed, count.Killed = 1, 1
		if reused {
			count.ReusedKilled = 1
		}
	case report.MutantSurvived:
		count.Executed, count.Survived = 1, 1
		if reused {
			count.ReusedSurvived = 1
		}
	case report.MutantInconclusive:
		count.Executed, count.Inconclusive = 1, 1
	case report.MutantCompileRejected:
		count.CompileRejected = 1
	case report.MutantAccepted:
		count.Accepted = 1
	case report.MutantOutOfScope:
		count.Selected, count.OutOfScope = 0, 1
	case report.MutantUnknown:
		count.Unknown = 1
	}
	return count
}
