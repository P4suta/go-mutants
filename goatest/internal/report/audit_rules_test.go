// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/report"
)

const moreTargetsThanSelected = 3

func auditedFixture() report.Report {
	value := persistedFixture()
	value.Accounting.Targets = report.CountAccounting{Discovered: 2, Selected: 2, Executed: 2}
	value.Targets = []report.TargetDisposition{
		{
			ID: "t1", Name: "TestOne", Kind: "test", Package: "example.test/fixture",
			Status: "passed", Line: 10, DurationMS: 5,
		},
		{ID: "t2", Name: "TestTwo", Kind: "test", Package: "example.test/fixture", Status: "passed"},
	}
	value.Resume = &report.Resume{Attempts: 1, ReusedTargets: 1}
	return value
}

func TestAReportIsAuditedFieldByFieldAndSaysWhichOneFailed(t *testing.T) {
	t.Parallel()
	valid := auditedFixture()
	if err := report.Validate(valid); err != nil {
		t.Fatalf("the fixture this test breaks one field of was rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*report.Report)
		want   string
	}{
		{
			name:   "a schema from another version",
			change: func(value *report.Report) { value.Schema = "goatest/report-v2" }, want: "report schema",
		},
		{
			name:   "a verdict nothing declares",
			change: func(value *report.Report) { value.Verdict = "PROBABLY" }, want: "verdict",
		},
		{
			name:   "a target inventory of the wrong size",
			change: func(value *report.Report) { value.Targets = value.Targets[:1] }, want: "entries for 2 selected",
		},
		{
			name:   "a target with no identity",
			change: func(value *report.Report) { value.Targets[0].ID = " " }, want: "incomplete identity",
		},
		{
			name:   "a target with no name",
			change: func(value *report.Report) { value.Targets[0].Name = " " }, want: "incomplete identity",
		},
		{
			name:   "a target of no kind",
			change: func(value *report.Report) { value.Targets[0].Kind = " " }, want: "incomplete identity",
		},
		{
			name:   "a target in no package",
			change: func(value *report.Report) { value.Targets[0].Package = " " }, want: "incomplete identity",
		},
		{
			name:   "a target with no status",
			change: func(value *report.Report) { value.Targets[0].Status = " " }, want: "incomplete identity",
		},
		{
			name:   "a target on a line before the first",
			change: func(value *report.Report) { value.Targets[0].Line = -1 }, want: "negative line or duration",
		},
		{
			name:   "a target that took less than no time",
			change: func(value *report.Report) { value.Targets[0].DurationMS = -1 }, want: "negative line or duration",
		},
		{
			name:   "two targets of one identity",
			change: func(value *report.Report) { value.Targets[1].ID = value.Targets[0].ID }, want: "duplicate ID",
		},
		{
			name:   "a resume of no attempt",
			change: func(value *report.Report) { value.Resume.Attempts = 0 }, want: "invalid count",
		},
		{
			name:   "a resume that reused less than no target",
			change: func(value *report.Report) { value.Resume.ReusedTargets = -1 }, want: "invalid count",
		},
		{
			name:   "a resume that reused less than no race package",
			change: func(value *report.Report) { value.Resume.ReusedRacePackages = -1 }, want: "invalid count",
		},
		{
			name:   "a resume that reused less than no mutant",
			change: func(value *report.Report) { value.Resume.ReusedMutants = -1 }, want: "invalid count",
		},
		{
			name:   "a resume that reused more targets than were selected",
			change: func(value *report.Report) { value.Resume.ReusedTargets = moreTargetsThanSelected }, want: "exceeds selected work",
		},
		{
			name:   "a resume that reused more race packages than were selected",
			change: func(value *report.Report) { value.Resume.ReusedRacePackages = 1 }, want: "exceeds selected work",
		},
		{
			name:   "a resume that reused more mutants than were selected",
			change: func(value *report.Report) { value.Resume.ReusedMutants = 1 }, want: "exceeds selected work",
		},
		{
			name: "a mutation this runner no longer calls compile-equivalent",
			change: func(value *report.Report) {
				value.Evidence = append(value.Evidence,
					report.Evidence{Kind: "mutation", ID: "e3", Status: "compile-equivalent"})
			},
			want: "compile-equivalent",
		},
		{
			name:   "an acceptance of nothing",
			change: func(value *report.Report) { value.Acceptances[0].ID = " " }, want: "requires id and reason",
		},
		{
			name:   "an acceptance for no reason",
			change: func(value *report.Report) { value.Acceptances[0].Reason = " " }, want: "requires id and reason",
		},
		{
			name:   "an acceptance that expires at no time",
			change: func(value *report.Report) { value.Acceptances[0].Expires = "next tuesday" }, want: "expiry",
		},
		{
			name: "one acceptance written twice",
			change: func(value *report.Report) {
				value.Acceptances = append(value.Acceptances, value.Acceptances[0])
			},
			want: "is duplicated",
		},
		{
			name: "accepted mutation evidence nothing accepted",
			change: func(value *report.Report) {
				value.Evidence = append(value.Evidence,
					report.Evidence{Kind: "mutation", ID: "e3", Status: "accepted", Detail: "unaccepted"})
			},
			want: "accepted mutation",
		},
		{
			name: "an accepted mutant nothing accepted",
			change: func(value *report.Report) {
				value.Mutants = append(value.Mutants,
					report.MutantDisposition{ID: "m1", Status: report.MutantAccepted, Detail: "unaccepted"})
				value.Accounting.Mutants = report.MutantAccounting{
					Discovered: 1, Selected: 1, Accepted: 1,
				}
			},
			want: "acceptance metadata",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := auditedFixture()
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

func TestAPersistedReportIsAuditedFieldByFieldAndSaysWhichOneFailed(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*report.Report)
		want   string
	}{
		{name: "no run identity", change: func(value *report.Report) { value.RunID = "" }, want: "run_id"},
		{name: "no run kind", change: func(value *report.Report) { value.RunKind = "" }, want: "run_kind"},
		{
			name:   "no requested scope kind",
			change: func(value *report.Report) { value.Scope.Requested.Kind = "" }, want: "requested or resolved scope",
		},
		{
			name:   "no resolved scope kind",
			change: func(value *report.Report) { value.Scope.Resolved.Kind = "" }, want: "requested or resolved scope",
		},
		{
			name:   "no requested project boundary",
			change: func(value *report.Report) { value.Scope.Requested.Project = "" }, want: "project boundary",
		},
		{
			name:   "no runner identity",
			change: func(value *report.Report) { value.Toolchain.Goatest = "" }, want: "toolchain or platform",
		},
		{
			name:   "no engine identity",
			change: func(value *report.Report) { value.Toolchain.GoMutants = "" }, want: "toolchain or platform",
		},
		{
			name:   "no operating system",
			change: func(value *report.Report) { value.Toolchain.OS = "" }, want: "toolchain or platform",
		},
		{
			name:   "no architecture",
			change: func(value *report.Report) { value.Toolchain.Arch = "" }, want: "toolchain or platform",
		},
		{
			name:   "no merge base",
			change: func(value *report.Report) { value.Repository.Git.MergeBase = "" }, want: "explicit Git identity",
		},
		{
			name:   "a commit that says it is unavailable while Git is",
			change: func(value *report.Report) { value.Repository.Git.Commit = "unavailable" },
			want:   "unavailable sentinel",
		},
		{
			name:   "a merge base that says it is unavailable while Git is",
			change: func(value *report.Report) { value.Repository.Git.MergeBase = "unavailable" },
			want:   "unavailable sentinel",
		},
		{
			name: "unavailable Git that still names changed files",
			change: func(value *report.Report) {
				value.Repository.Git = report.Git{Commit: "unavailable", MergeBase: "unavailable", ChangedFiles: []string{"a.go"}}
				value.Limitations = append(value.Limitations,
					report.Limitation{Code: report.LimitationGitMetadataUnavailable, Summary: "Git metadata is unavailable"})
			},
			want: "ambiguous partial identity",
		},
		{
			name: "unavailable Git that states no limitation",
			change: func(value *report.Report) {
				value.Repository.Git = report.Git{Commit: "unavailable", MergeBase: "unavailable"}
			},
			want: "missing its limitation",
		},
		{
			name:   "a command timeout below zero",
			change: func(value *report.Report) { value.Execution.CommandTimeoutNS = -1 }, want: "negative value",
		},
		{
			name:   "a target timeout below zero",
			change: func(value *report.Report) { value.Execution.TargetTimeoutNS = -1 }, want: "negative value",
		},
		{
			name:   "a start nobody can read",
			change: func(value *report.Report) { value.Timing.StartedAt = "this morning" }, want: "started_at",
		},
		{
			name:   "a finish nobody can read",
			change: func(value *report.Report) { value.Timing.FinishedAt = "later" }, want: "finished_at",
		},
		{
			name:   "a run that finished before it started",
			change: func(value *report.Report) { value.Timing.FinishedAt = "2025-01-01T00:00:00Z" }, want: "internally inconsistent",
		},
		{
			name:   "a run that took less than no time",
			change: func(value *report.Report) { value.Timing.DurationMS = -1 }, want: "internally inconsistent",
		},
		{
			name:   "a cache-derived report that names no source",
			change: func(value *report.Report) { value.Cache.Derived = true }, want: "source_run_id",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := persistedFixture()
			test.change(&input)
			err := report.ValidateForPersistence(input)
			if err == nil {
				t.Fatalf("ValidateForPersistence accepted a report with %s", test.name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateForPersistence reported %v, want it to say %q", err, test.want)
			}
		})
	}
}
