// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/cli"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

func TestServicePropagatesRepositoryRootResolutionFailure(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("absolute root failed")
	service := Service{
		Root: "relative",
		absolute: func(string) (string, error) {
			return "", sentinel
		},
	}
	_, err := service.Execute(t.Context(), cli.CommandReport, cli.Request{}, "")
	if !errors.Is(err, sentinel) {
		t.Fatalf("root resolution error = %v, want %v", err, sentinel)
	}
}

func TestFinalizeReportMarksUnreadableConfigurationMetadata(t *testing.T) {
	t.Parallel()
	hooks := reportHooks{git: absentGit, readConfiguration: func(string) ([]byte, error) {
		return nil, errors.New("configuration read failed")
	}}
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	result := finalizeReportKind(t.Context(), t.TempDir(), cli.Request{}, report.Report{
		Verdict: report.VerdictCompleted,
	}, report.RunOperation, now, now, hooks)
	if len(result.Configuration.Digest) != len(appTestDigest("a")) {
		t.Fatalf("configuration digest = %q", result.Configuration.Digest)
	}
	found := false
	for _, limitation := range result.Limitations {
		found = found || limitation.Code == "configuration-metadata-unavailable"
	}
	if !found {
		t.Fatalf("configuration limitation missing: %+v", result.Limitations)
	}
}

func absentGit(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("not a git repository")
}

func TestGitMetadataComesFromTheServiceGitHook(t *testing.T) {
	t.Parallel()
	const commit = "0123456789abcdef0123456789abcdef01234567"
	var asked [][]string
	scripted := func(_ context.Context, _ string, arguments ...string) ([]byte, error) {
		asked = append(asked, arguments)
		switch arguments[0] {
		case "rev-parse":
			return []byte(commit + "\n"), nil
		case "merge-base":
			return []byte(commit + "\n"), nil
		case "status", "diff", "ls-files":
			return nil, nil
		}
		return nil, errors.New("unexpected git command")
	}
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	result := finalizeReportKind(t.Context(), t.TempDir(), cli.Request{}, report.Report{
		Verdict: report.VerdictCompleted,
	}, report.RunOperation, now, now, reportHooks{git: scripted})

	if !result.Repository.Git.Available || result.Repository.Git.Commit != commit {
		t.Fatalf("git metadata = %+v, want the commit the hook reported", result.Repository.Git)
	}
	if result.Repository.Git.Dirty {
		t.Error("an empty status was read as a dirty tree")
	}
	for _, limitation := range result.Limitations {
		if limitation.Code == "git-metadata-unavailable" {
			t.Error("the run reported git as unavailable while the hook was answering")
		}
	}
	if len(asked) == 0 {
		t.Fatal("the hook was never called")
	}
}

func TestGitMetadataIsUnavailableWhenTheHookRefuses(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	result := finalizeReportKind(t.Context(), t.TempDir(), cli.Request{}, report.Report{
		Verdict: report.VerdictCompleted,
	}, report.RunOperation, now, now, reportHooks{git: absentGit})

	if result.Repository.Git.Available || result.Repository.Git.Commit != "unavailable" {
		t.Fatalf("git metadata = %+v, want the unavailable placeholder", result.Repository.Git)
	}
	found := false
	for _, limitation := range result.Limitations {
		found = found || limitation.Code == "git-metadata-unavailable"
	}
	if !found {
		t.Error("no git-metadata-unavailable limitation was recorded")
	}
}
