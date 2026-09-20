// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/cli"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

var acceptMoment = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

func acceptRoot(t *testing.T, finding string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = 1\ncontract = \"standard-v1\"\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	latest := report.Report{
		Schema: report.SchemaV1, RunID: "acceptance-fixture", RunKind: report.RunFull,
		Verdict: report.VerdictInsufficient, Contract: "standard-v1", Snapshot: "snapshot",
		Scope: report.Scope{
			Requested: report.ScopeSpec{Kind: "full", Project: "."},
			Resolved:  report.ScopeSpec{Kind: "full", Project: "."},
		},
		Repository: report.Repository{
			Module: "example.test/fixture",
			Git:    report.Git{Available: true, Commit: "commit", MergeBase: "commit"},
		},
		Configuration: report.Configuration{Digest: appTestDigest("a")},
		Execution: report.Execution{
			MutationJobs: 1, CommandTimeoutNS: int64(time.Minute), TargetTimeoutNS: int64(time.Minute),
		},
		Toolchain: report.Toolchain{Go: "go1.26.6", Goatest: "devel", GoMutants: "v0.1.2", OS: "darwin", Arch: "arm64"},
		Timing: report.Timing{
			StartedAt: "2026-01-01T00:00:00Z", FinishedAt: "2026-01-01T00:00:01Z", DurationMS: 1000,
		},
		Findings: []report.Finding{{ID: finding, Kind: "surviving-mutant", Summary: "a mutant survived"}},
	}
	if err := WriteReports(root, latest); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestAnAcceptanceRefusesEveryReasonAndExpiryItCannotUse(t *testing.T) {
	t.Parallel()
	const finding = "abcd1234abcd1234"
	sound := acceptMoment.Add(time.Hour).Format(time.RFC3339)
	for _, test := range []struct {
		name    string
		id      string
		request cli.Request
		want    string
	}{
		{
			name: "an acceptance of everything it asks for", id: finding,
			request: cli.Request{Reason: "the branch decides nothing", Expires: sound},
		},
		{
			name: "a finding the latest report does not hold", id: "0000000000000000",
			request: cli.Request{Reason: "a reason", Expires: sound}, want: "is absent from the latest report",
		},
		{
			name: "an acceptance with no reason at all", id: finding,
			request: cli.Request{Expires: sound}, want: "requires a reason and expiry",
		},
		{
			name: "an acceptance whose reason is only spaces", id: finding,
			request: cli.Request{Reason: "   ", Expires: sound}, want: "requires a reason and expiry",
		},
		{
			name: "an acceptance with no expiry at all", id: finding,
			request: cli.Request{Reason: "a reason"}, want: "requires a reason and expiry",
		},
		{
			name: "an acceptance whose expiry is only spaces", id: finding,
			request: cli.Request{Reason: "a reason", Expires: "   "}, want: "requires a reason and expiry",
		},
		{
			name: "an expiry that is no moment at all", id: finding,
			request: cli.Request{Reason: "a reason", Expires: "whenever"}, want: "acceptance expiry",
		},
		{
			name: "an expiry that has already passed", id: finding,
			request: cli.Request{Reason: "a reason", Expires: acceptMoment.Add(-time.Hour).Format(time.RFC3339)},
			want:    "must be in the future",
		},
		{
			name: "an expiry at this very moment", id: finding,
			request: cli.Request{Reason: "a reason", Expires: acceptMoment.Format(time.RFC3339)},
			want:    "must be in the future",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := acceptRoot(t, finding)
			service := Service{Root: root, TempDirectory: t.TempDir(), Now: func() time.Time { return acceptMoment }}
			result, err := service.Execute(t.Context(), cli.CommandAccept, test.request, test.id)
			if test.want == "" {
				if err != nil {
					t.Fatalf("%s was refused: %v", test.name, err)
				}
				if result.Verdict != report.VerdictCompleted {
					t.Fatalf("%s was given the verdict %q", test.name, result.Verdict)
				}
				written, readErr := os.ReadFile(filepath.Join(root, ".goatest.toml"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !strings.Contains(string(written), finding) {
					t.Errorf("the configuration reads %q, want the acceptance it recorded", written)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s was accepted: %+v", test.name, result)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s was refused with %v, want %q", test.name, err, test.want)
			}
		})
	}
}

func TestAnOperationTakesItsContractFromTheConfigurationWhereItHasNone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	service := Service{Root: root, TempDirectory: t.TempDir()}
	result, err := service.Execute(t.Context(), cli.CommandInit, cli.Request{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Contract == "" {
		t.Fatalf("an operation that names no contract was finished with none: %+v", result)
	}
}
