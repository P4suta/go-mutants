// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/report"
)

func TestTheLineRendererNamesOnlyWhatTheReportCarries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		change  func(*report.Report)
		want    []string
		without []string
	}{
		{
			name:   "a report with no run identity",
			change: func(value *report.Report) { value.RunID = "" },
			want:   []string{"INSUFFICIENT standard-v1 snapshot=abc123\n"}, without: []string{"run="},
		},
		{
			name:   "a report that names its run",
			change: func(value *report.Report) { value.RunID, value.RunKind = "run-a", report.RunFull },
			want:   []string{"run=run-a kind=full"},
		},
		{
			name: "a finding with a path and a line",
			change: func(value *report.Report) {
				value.Findings = []report.Finding{{ID: "f1", Kind: "survivor", Path: "a.go", Line: 12, Summary: "survived"}}
			},
			want: []string{"FINDING f1 survivor a.go:12: survived\n"},
		},
		{
			name: "a finding with a path and no line",
			change: func(value *report.Report) {
				value.Findings = []report.Finding{{ID: "f1", Kind: "survivor", Path: "a.go", Summary: "survived"}}
			},
			want: []string{"FINDING f1 survivor a.go: survived\n"},
		},
		{
			name: "a finding with no place at all",
			change: func(value *report.Report) {
				value.Findings = []report.Finding{{ID: "f1", Kind: "survivor", Summary: "survived"}}
			},
			want: []string{"FINDING f1 survivor survived\n"},
		},
		{
			name: "a finding that names the mutation and how to replay it",
			change: func(value *report.Report) {
				value.Findings = []report.Finding{{
					ID: "f1", Kind: "survivor", Summary: "survived",
					Mutant: "lt-to-le: < -> <=", Replay: "goatest replay f1",
				}}
			},
			want: []string{"  MUTANT lt-to-le: < -> <=\n", "  REPLAY goatest replay f1\n"},
		},
		{
			name: "an operation, which names its evidence",
			change: func(value *report.Report) {
				value.RunID, value.RunKind = "run-a", report.RunOperation
				value.Verdict = report.VerdictCompleted
				value.Scope.Resolved.Kind = string(report.RunOperation)
				value.Evidence = []report.Evidence{{Kind: "doctor", ID: "config", Status: "ready", Detail: "strict"}}
			},
			want: []string{"EVIDENCE doctor config ready strict\n"},
		},
		{
			name: "an assurance, which does not",
			change: func(value *report.Report) {
				value.Evidence = []report.Evidence{{Kind: "doctor", ID: "config", Status: "ready", Detail: "strict"}}
			},
			without: []string{"EVIDENCE "},
		},
		{
			name: "a repair that says why",
			change: func(value *report.Report) {
				value.Repairs = []report.Repair{{
					ID: "r1", Finding: "f2", Path: "z_test.go", Status: "artifact", Reason: "the preimage moved",
				}}
			},
			want: []string{"REPAIR r1 artifact z_test.go finding=f2\n", "  REASON the preimage moved\n"},
		},
		{
			name: "a repair that does not",
			change: func(value *report.Report) {
				value.Repairs = []report.Repair{{ID: "r1", Finding: "f2", Path: "z_test.go", Status: "applied"}}
			},
			want: []string{"REPAIR r1 applied z_test.go finding=f2\n"}, without: []string{"  REASON "},
		},
		{
			name: "an acceptance with an owner and a ticket",
			change: func(value *report.Report) {
				value.Acceptances = []report.Acceptance{{
					ID: "a1", Reason: "reviewed", Expires: "2026-12-01T00:00:00Z",
					Owner: "goatest", Ticket: "GOAT-1",
				}}
			},
			want: []string{
				"ACCEPTANCE a1 expires=2026-12-01T00:00:00Z reason=reviewed owner=goatest ticket=GOAT-1\n",
			},
		},
		{
			name: "an acceptance with neither",
			change: func(value *report.Report) {
				value.Acceptances = []report.Acceptance{{ID: "a1", Reason: "reviewed", Expires: "2026-12-01T00:00:00Z"}}
			},
			want:    []string{"ACCEPTANCE a1 expires=2026-12-01T00:00:00Z reason=reviewed\n"},
			without: []string{"owner=", "ticket="},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := fixture()
			input.Evidence, input.Findings, input.Repairs = nil, nil, nil
			input.Limitations, input.Acceptances = nil, nil
			test.change(&input)
			got := report.Lines(input)
			for _, want := range test.want {
				if !strings.Contains(got, want) {
					t.Errorf("the rendering does not hold %q:\n%s", want, got)
				}
			}
			for _, unwanted := range test.without {
				if strings.Contains(got, unwanted) {
					t.Errorf("the rendering holds %q, which the report does not carry:\n%s", unwanted, got)
				}
			}
		})
	}
}
