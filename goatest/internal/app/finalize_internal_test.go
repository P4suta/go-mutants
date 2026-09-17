// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/cli"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

func finalizeHooks() reportHooks {
	return reportHooks{
		git: scriptedGit(map[string]string{
			"rev-parse": "commit-a\n", "status": "", "merge-base": "commit-base\n",
			"diff": "changed.go\x00", "ls-files": "",
		}, nil),
		readConfiguration: func(string) ([]byte, error) { return []byte("version = 1\n"), nil },
	}
}

func finalizeFixture() report.Report {
	return report.Report{
		Verdict: report.VerdictInsufficient, Contract: "standard-v1", Snapshot: "snapshot-a",
		Scope: report.Scope{
			Requested: report.ScopeSpec{Kind: "full", Project: "."},
			Resolved:  report.ScopeSpec{Kind: "full", Project: "."},
		},
		Repository:    report.Repository{Module: "example.test/fixture", Packages: []string{"./..."}},
		Configuration: report.Configuration{Digest: appTestDigest("a")},
		Toolchain: report.Toolchain{
			Go: "go1.26.6", Goatest: "devel", GoMutants: "v0.1.2", OS: "linux", Arch: "amd64",
		},
	}
}

func hasLimitationCode(limitations []report.Limitation, code string) bool {
	for _, limitation := range limitations {
		if limitation.Code == code {
			return true
		}
	}
	return false
}

func TestFinalizingAReportFillsEveryGapAndSaysThatItDid(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	finished := started.Add(time.Second)
	for _, test := range []struct {
		name  string
		blank func(*report.Report)
		hooks func(*reportHooks)
		want  string
		fill  func(report.Report) string
	}{
		{
			name:  "a report that names no contract",
			blank: func(value *report.Report) { value.Contract = "" },
			want:  report.LimitationContractMetadataUnavailable,
			fill:  func(value report.Report) string { return value.Contract },
		},
		{
			name:  "a report that names no snapshot",
			blank: func(value *report.Report) { value.Snapshot = "" },
			want:  report.LimitationSnapshotMetadataUnavailable,
			fill:  func(value report.Report) string { return value.Snapshot },
		},
		{
			name:  "a report that names no toolchain",
			blank: func(value *report.Report) { value.Toolchain.Go = "" },
			want:  report.LimitationGoToolchainMetadataUnavailable,
			fill:  func(value report.Report) string { return value.Toolchain.Go },
		},
		{
			name:  "a report that names no module",
			blank: func(value *report.Report) { value.Repository.Module = "" },
			want:  report.LimitationModuleMetadataUnavailable,
			fill:  func(value report.Report) string { return value.Repository.Module },
		},
		{
			name:  "a report whose configuration cannot be read",
			blank: func(value *report.Report) { value.Configuration.Digest = "" },
			hooks: func(hooks *reportHooks) {
				hooks.readConfiguration = func(string) ([]byte, error) { return nil, errors.New("no configuration") }
			},
			want: report.LimitationConfigurationMetadataUnavailable,
		},
		{
			name: "a report whose Git identity cannot be read",
			hooks: func(hooks *reportHooks) {
				hooks.git = scriptedGit(nil, map[string]error{"rev-parse": errors.New("no HEAD")})
			},
			want: report.LimitationGitMetadataUnavailable,
			fill: func(value report.Report) string { return value.Repository.Git.Commit },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := finalizeFixture()
			if test.blank != nil {
				test.blank(&input)
			}
			hooks := finalizeHooks()
			if test.hooks != nil {
				test.hooks(&hooks)
			}
			result := finalizeReportKind(t.Context(), ".", cli.Request{}, input, report.RunFull, started, finished, hooks)
			if !hasLimitationCode(result.Limitations, test.want) {
				t.Fatalf("finalizing %s stated %+v, want the %q limitation", test.name, result.Limitations, test.want)
			}
			if test.fill != nil && test.fill(result) != "unavailable" {
				t.Errorf("the gap was filled with %q, want %q", test.fill(result), "unavailable")
			}
		})
	}
}

func TestFinalizingACompleteReportStatesNoLimitationAtAll(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	finished := started.Add(time.Second)
	result := finalizeReportKind(t.Context(), ".", cli.Request{}, finalizeFixture(),
		report.RunFull, started, finished, finalizeHooks())

	if len(result.Limitations) != 0 {
		t.Fatalf("a complete report stated %+v, want none", result.Limitations)
	}
	if result.Schema != report.SchemaV1 || result.RunKind != report.RunFull || result.RunID == "" {
		t.Fatalf("finalizing left schema %q, kind %q and run %q", result.Schema, result.RunKind, result.RunID)
	}
	if result.Timing.StartedAt != "2026-01-01T00:00:00Z" || result.Timing.DurationMS != time.Second.Milliseconds() {
		t.Fatalf("finalizing recorded %+v, want the second between the two moments", result.Timing)
	}
	if !slices.Equal(result.Scope.Requested.Modules, []string{"example.test/fixture"}) ||
		!slices.Equal(result.Scope.Resolved.Modules, []string{"example.test/fixture"}) {
		t.Fatalf("the scope names modules %+v and %+v, want the repository module",
			result.Scope.Requested.Modules, result.Scope.Resolved.Modules)
	}
	if result.Repository.Git.Commit != "commit-a" || result.Repository.Git.MergeBase != "commit-base" {
		t.Fatalf("the report carries the Git identity %+v, want the one git gave", result.Repository.Git)
	}
}

func TestFinalizingARunThatEndedBeforeItStartedRecordsNoNegativeDuration(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	result := finalizeReportKind(t.Context(), ".", cli.Request{}, finalizeFixture(),
		report.RunFull, started, started.Add(-time.Second), finalizeHooks())

	if result.Timing.DurationMS != 0 {
		t.Fatalf("a run that ended before it started lasted %dms, want none", result.Timing.DurationMS)
	}
}

func TestFinalizingAChangesetNamesTheFilesGitSawChange(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	input := finalizeFixture()
	input.Scope = report.Scope{}
	result := finalizeReportKind(t.Context(), ".", cli.Request{}, input,
		report.RunChangeset, started, started.Add(time.Second), finalizeHooks())

	if !slices.Equal(result.Scope.Requested.Files, []string{"changed.go"}) {
		t.Errorf("the requested scope names %q, want the files git saw change", result.Scope.Requested.Files)
	}
	if !slices.Equal(result.Scope.Resolved.Files, []string{"changed.go"}) {
		t.Errorf("the resolved scope names %q, want the files git saw change", result.Scope.Resolved.Files)
	}
	if result.Scope.Requested.Ref != "HEAD" {
		t.Errorf("the requested scope compares against %q, want HEAD", result.Scope.Requested.Ref)
	}
}

func TestFinalizingAReplayNamesTheReplayScopeWhateverTheReportSaid(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	input := finalizeFixture()
	input.Verdict = report.VerdictAssured
	result := finalizeReportKind(t.Context(), ".", cli.Request{}, input,
		report.RunReplay, started, started.Add(time.Second), finalizeHooks())

	if result.Scope.Requested.Kind != string(report.RunReplay) || result.Scope.Requested.Project != "." {
		t.Fatalf("the requested scope is %+v, want the replay scope", result.Scope.Requested)
	}
	if result.Verdict != report.VerdictResolved {
		t.Fatalf("a replay that found nothing is %q, want %q", result.Verdict, report.VerdictResolved)
	}
}
