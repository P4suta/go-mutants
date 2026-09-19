// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/assure"
	"github.com/P4suta/go-mutants/goatest/internal/cli"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

var errAssuranceFixture = errors.New("the assurance could not run")

func writtenRunRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = 1\ncontract = \"standard-v1\"\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	return root
}

func assuredRun(context.Context, assure.Options) (report.Report, error) {
	return report.Report{
		Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1", Snapshot: "snapshot",
	}, nil
}

func TestAVerifyRunsAndReportsEvenWhenItCannotHoldTheCacheLease(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		blocks bool
		fails  bool
	}{
		{name: "a cache it can hold"},
		{name: "a cache it cannot hold at all", blocks: true, fails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := writtenRunRoot(t)
			runs := 0
			if test.blocks {
				internal := filepath.Join(root, ".goatest")
				if err := os.MkdirAll(internal, filemode.ReadableDirectory); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(internal, "cache"),
					[]byte("this is not a directory"), filemode.PrivateFile); err != nil {
					t.Fatal(err)
				}
			}
			service := Service{
				Root: root, TempDirectory: t.TempDir(),
				Run: func(ctx context.Context, options assure.Options) (report.Report, error) {
					runs++
					return assuredRun(ctx, options)
				},
			}
			result, err := service.runAndWrite(t.Context(), root, cli.Request{})
			if failed := err != nil; failed != test.fails {
				t.Fatalf("%s reported %v, want failed=%t", test.name, err, test.fails)
			}
			if runs != 1 {
				t.Fatalf("%s started the assurance %d times, want once", test.name, runs)
			}
			if result.RunID == "" {
				t.Fatalf("%s wrote a report with no run identity: %+v", test.name, result)
			}
			if _, statErr := os.Stat(filepath.Join(root, "reports", "latest-any.json")); statErr != nil {
				t.Errorf("%s wrote no report at all: %v", test.name, statErr)
			}
		})
	}
}

func TestAVerifyThatWasInterruptedWritesHistoryRatherThanAVerdict(t *testing.T) {
	t.Parallel()
	root := writtenRunRoot(t)
	service := Service{
		Root: root, TempDirectory: t.TempDir(),
		Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{
				Schema: report.SchemaV1, Verdict: report.VerdictAssured, Contract: "standard-v1",
				Snapshot: "snapshot",
			}, context.Canceled
		},
	}
	result, err := service.runAndWrite(t.Context(), root, cli.Request{})
	if err == nil {
		t.Fatal("an interrupted run reported nothing")
	}
	if !hasLimitation(result.Limitations, "assurance-interrupted") {
		t.Fatalf("an interrupted run stated %+v, want the interruption", result.Limitations)
	}
	if _, statErr := os.Stat(filepath.Join(root, "reports", "latest-any.json")); statErr == nil {
		t.Error("an interrupted run published a verdict index; only its history belongs on disk")
	}
	entries, readErr := os.ReadDir(filepath.Join(root, "reports", "runs"))
	if readErr != nil || len(entries) == 0 {
		t.Fatalf("an interrupted run left no history: %v", readErr)
	}
}

func TestAVerifyThatFailedForAnotherReasonWritesAnInfrastructureReport(t *testing.T) {
	t.Parallel()
	root := writtenRunRoot(t)
	service := Service{
		Root: root, TempDirectory: t.TempDir(),
		Run: func(context.Context, assure.Options) (report.Report, error) {
			return report.Report{}, errAssuranceFixture
		},
	}
	result, err := service.runAndWrite(t.Context(), root, cli.Request{})
	if err == nil || !strings.Contains(err.Error(), "the assurance could not run") {
		t.Fatalf("a run that failed reported %v", err)
	}
	if result.Verdict != report.VerdictError {
		t.Fatalf("a run that failed was given the verdict %q, want %q", result.Verdict, report.VerdictError)
	}
	if _, statErr := os.Stat(filepath.Join(root, "reports", "latest-any.json")); statErr != nil {
		t.Errorf("a run that failed published no report: %v", statErr)
	}
}

func hasLimitation(limitations []report.Limitation, code string) bool {
	for _, limitation := range limitations {
		if limitation.Code == code {
			return true
		}
	}
	return false
}
