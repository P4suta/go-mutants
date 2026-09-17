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

func TestAReportAcceptsEveryValueAtTheEdgeOfARule(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*report.Report)
	}{
		{name: "one attempt", change: func(value *report.Report) { value.Resume.Attempts = 1 }},
		{
			name:   "a resume that reused every selected target",
			change: func(value *report.Report) { value.Resume.ReusedTargets = value.Accounting.Targets.Selected },
		},
		{
			name:   "a resume that reused no target at all",
			change: func(value *report.Report) { value.Resume.ReusedTargets = 0 },
		},
		{
			name:   "a target on the first line",
			change: func(value *report.Report) { value.Targets[0].Line = 0 },
		},
		{
			name:   "a target that took no time",
			change: func(value *report.Report) { value.Targets[0].DurationMS = 0 },
		},
		{name: "no resume metadata at all", change: func(value *report.Report) { value.Resume = nil }},
		{
			name:   "an acceptance that expires the moment after the run started",
			change: func(value *report.Report) { value.Acceptances[0].Expires = "2026-01-01T00:00:01Z" },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := auditedFixture()
			test.change(&input)
			if err := report.Validate(input); err != nil {
				t.Fatalf("Validate refused a report with %s: %v", test.name, err)
			}
		})
	}
}

func TestAPersistedReportAcceptsEveryValueAtTheEdgeOfARule(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*report.Report)
	}{
		{name: "no mutation job at all", change: func(value *report.Report) { value.Execution.MutationJobs = 0 }},
		{
			name:   "no command timeout at all",
			change: func(value *report.Report) { value.Execution.CommandTimeoutNS = 0 },
		},
		{
			name:   "no target timeout at all",
			change: func(value *report.Report) { value.Execution.TargetTimeoutNS = 0 },
		},
		{
			name: "a run that took no time",
			change: func(value *report.Report) {
				value.Timing.FinishedAt = value.Timing.StartedAt
				value.Timing.DurationMS = 0
			},
		},
		{
			name: "a cache-derived report that names its source",
			change: func(value *report.Report) {
				value.Cache.Derived = true
				value.Cache.SourceRunID = "run-before"
			},
		},
		{
			name: "unavailable Git metadata that says so",
			change: func(value *report.Report) {
				value.Repository.Git = report.Git{Commit: "unavailable", MergeBase: "unavailable"}
				value.Limitations = append(value.Limitations, report.Limitation{
					Code: report.LimitationGitMetadataUnavailable, Summary: "Git metadata is unavailable",
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := persistedFixture()
			test.change(&input)
			if err := report.ValidateForPersistence(input); err != nil {
				t.Fatalf("ValidateForPersistence refused a report with %s: %v", test.name, err)
			}
		})
	}
}

func TestACountThatNamesOneNumberIsHeldToItsEquations(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		count report.CountAccounting
	}{
		{name: "a discovery alone", count: report.CountAccounting{Discovered: 1}},
		{name: "a selection alone", count: report.CountAccounting{Selected: 1}},
		{name: "an execution alone", count: report.CountAccounting{Executed: 1}},
		{name: "a skip alone", count: report.CountAccounting{Skipped: 1}},
		{name: "an exclusion alone", count: report.CountAccounting{Excluded: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := auditedFixture()
			input.Accounting.Race = test.count
			err := report.Validate(input)
			if err == nil || !strings.Contains(err.Error(), "race accounting mismatch") {
				t.Fatalf("Validate reported %v, want it to refuse the race count that does not add up", err)
			}
		})
	}
}

func TestEveryVerdictNamesTheScopeItIsReservedFor(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		verdict  report.Verdict
		runKind  report.RunKind
		resolved string
		want     string
	}{
		{
			name: "an assurance of the whole project", verdict: report.VerdictAssured,
			runKind: report.RunFull, resolved: string(report.RunFull),
		},
		{
			name:    "an assurance of the whole project resolved to a package",
			verdict: report.VerdictAssured, runKind: report.RunFull, resolved: string(report.RunPackage),
			want: "reserved for resolved full scope",
		},
		{
			name: "a changeset assurance", verdict: report.VerdictChangeAssured,
			runKind: report.RunChangeset, resolved: string(report.RunChangeset),
		},
		{
			name: "a changeset assurance of another run kind", verdict: report.VerdictChangeAssured,
			runKind: report.RunFull, resolved: string(report.RunChangeset),
			want: "requires a resolved changeset run",
		},
		{
			name: "a changeset assurance resolved elsewhere", verdict: report.VerdictChangeAssured,
			runKind: report.RunChangeset, resolved: string(report.RunFull),
			want: "requires a resolved changeset run",
		},
		{
			name: "a package assurance", verdict: report.VerdictScopeAssured,
			runKind: report.RunPackage, resolved: string(report.RunPackage),
		},
		{
			name: "a package assurance of another run kind", verdict: report.VerdictScopeAssured,
			runKind: report.RunFull, resolved: string(report.RunPackage),
			want: "requires a resolved package run",
		},
		{
			name: "a package assurance resolved elsewhere", verdict: report.VerdictScopeAssured,
			runKind: report.RunPackage, resolved: string(report.RunFull),
			want: "requires a resolved package run",
		},
		{
			name: "a reproduction of a replay", verdict: report.VerdictReproduced,
			runKind: report.RunReplay, resolved: string(report.RunReplay),
		},
		{
			name: "a resolution of a replay", verdict: report.VerdictResolved,
			runKind: report.RunReplay, resolved: string(report.RunReplay),
		},
		{
			name: "a reproduction of something that is not a replay", verdict: report.VerdictReproduced,
			runKind: report.RunFull, resolved: string(report.RunFull),
			want: "replay outcomes require a replay run",
		},
		{
			name: "a resolution of something that is not a replay", verdict: report.VerdictResolved,
			runKind: report.RunPackage, resolved: string(report.RunPackage),
			want: "replay outcomes require a replay run",
		},
		{
			name: "a completed operation", verdict: report.VerdictCompleted,
			runKind: report.RunOperation, resolved: string(report.RunOperation),
		},
		{
			name: "a completed assurance", verdict: report.VerdictCompleted,
			runKind: report.RunFull, resolved: string(report.RunFull),
			want: "reserved for non-assurance operations",
		},
		{
			name: "a defect anywhere", verdict: report.VerdictDefect,
			runKind: report.RunFull, resolved: string(report.RunPackage),
		},
		{
			name: "an error anywhere", verdict: report.VerdictError,
			runKind: report.RunPackage, resolved: string(report.RunFull),
		},
		{
			name: "insufficient evidence anywhere", verdict: report.VerdictInsufficient,
			runKind: report.RunReplay, resolved: string(report.RunFull),
		},
		{
			name: "a report that names no run kind at all", verdict: report.VerdictAssured,
			resolved: string(report.RunPackage),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := auditedFixture()
			input.Verdict = test.verdict
			input.RunKind = test.runKind
			input.Scope.Resolved.Kind = test.resolved
			if test.verdict == report.VerdictError {
				input.Accounting.Mutants.Unknown = 0
			}
			err := report.Validate(input)
			switch {
			case test.want == "" && err != nil:
				t.Fatalf("Validate refused %s: %v", test.name, err)
			case test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)):
				t.Fatalf("Validate reported %v, want it to say %q", err, test.want)
			}
		})
	}
}

func TestAPersistedReportRefusesEveryAmbiguousGitIdentity(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		git  report.Git
	}{
		{
			name: "a commit that says nothing while the rest says unavailable",
			git:  report.Git{Commit: "commit-a", MergeBase: "unavailable"},
		},
		{
			name: "a merge base that says nothing while the rest says unavailable",
			git:  report.Git{Commit: "unavailable", MergeBase: "commit-a"},
		},
		{
			name: "an unavailable identity that is also dirty",
			git:  report.Git{Commit: "unavailable", MergeBase: "unavailable", Dirty: true},
		},
		{
			name: "an unavailable identity that names changed files",
			git: report.Git{
				Commit: "unavailable", MergeBase: "unavailable", ChangedFiles: []string{"a.go"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := persistedFixture()
			input.Repository.Git = test.git
			input.Limitations = append(input.Limitations, report.Limitation{
				Code: report.LimitationGitMetadataUnavailable, Summary: "Git metadata is unavailable",
			})
			err := report.ValidateForPersistence(input)
			if err == nil || !strings.Contains(err.Error(), "ambiguous partial identity") {
				t.Fatalf("ValidateForPersistence reported %v, want it to refuse the partial identity", err)
			}
		})
	}
}

func TestAReportRefusesEveryCountThatDoesNotAddUpOnEitherSide(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		apply func(*report.Report, report.CountAccounting)
		want  string
	}{
		{
			name: "the target count",
			apply: func(value *report.Report, count report.CountAccounting) {
				value.Accounting.Targets = count
				value.Targets = nil
			},
			want: "targets accounting",
		},
		{
			name:  "the race count",
			apply: func(value *report.Report, count report.CountAccounting) { value.Accounting.Race = count },
			want:  "race accounting",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := auditedFixture()
			test.apply(&input, report.CountAccounting{Discovered: 1, Selected: 1, Executed: 1, Excluded: 1})
			err := report.Validate(input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate reported %v, want it to refuse %s", err, test.name)
			}
		})
	}
}

func TestAReportWithNoRunKindIsStillHeldToItsPersistedFields(t *testing.T) {
	t.Parallel()
	input := persistedFixture()
	input.RunKind = ""
	err := report.ValidateForPersistence(input)
	if err == nil || !strings.Contains(err.Error(), "missing run_kind") {
		t.Fatalf("ValidateForPersistence reported %v, want it to say the run kind is missing", err)
	}
}
