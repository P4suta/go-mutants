// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
	"github.com/P4suta/go-mutants/goatest/internal/report"
)

type stubReportFile struct {
	path     string
	writeErr error
	syncErr  error
	chmodErr error
	closeErr error
}

func (file *stubReportFile) Name() string { return file.path }
func (file *stubReportFile) Write(data []byte) (int, error) {
	if file.writeErr != nil {
		return 0, file.writeErr
	}
	return len(data), nil
}
func (file *stubReportFile) Sync() error             { return file.syncErr }
func (file *stubReportFile) Chmod(os.FileMode) error { return file.chmodErr }
func (file *stubReportFile) Close() error            { return file.closeErr }

func TestAtomicWritePropagatesEveryFilesystemStageAndRenameFallback(t *testing.T) {
	t.Parallel()
	stageErr := errors.New("stage failed")
	fallbackErr := errors.New("fallback failed")
	for _, testCase := range []struct {
		name       string
		configure  func(*atomicWriteOperations, *stubReportFile)
		want       error
		wantRename int
	}{
		{name: "success", wantRename: 1},
		{name: "mkdir", configure: func(ops *atomicWriteOperations, _ *stubReportFile) {
			ops.mkdirAll = func(string, os.FileMode) error { return stageErr }
		}, want: stageErr},
		{name: "create", configure: func(ops *atomicWriteOperations, _ *stubReportFile) {
			ops.createTemp = func(string, string) (atomicReportFile, error) { return nil, stageErr }
		}, want: stageErr},
		{name: "write", configure: func(_ *atomicWriteOperations, file *stubReportFile) { file.writeErr = stageErr }, want: stageErr},
		{name: "sync", configure: func(_ *atomicWriteOperations, file *stubReportFile) { file.syncErr = stageErr }, want: stageErr},
		{name: "chmod", configure: func(_ *atomicWriteOperations, file *stubReportFile) { file.chmodErr = stageErr }, want: stageErr},
		{name: "close", configure: func(_ *atomicWriteOperations, file *stubReportFile) { file.closeErr = stageErr }, want: stageErr},
		{name: "remove-fails", configure: func(ops *atomicWriteOperations, _ *stubReportFile) {
			ops.rename = sequenceErrors(stageErr)
			ops.remove = func(string) error { return fallbackErr }
		}, want: stageErr, wantRename: 1},
		{name: "missing-destination", configure: func(ops *atomicWriteOperations, _ *stubReportFile) {
			ops.rename = sequenceErrors(stageErr, nil)
			ops.remove = func(string) error { return os.ErrNotExist }
		}, wantRename: 2},
		{name: "second-rename-fails", configure: func(ops *atomicWriteOperations, _ *stubReportFile) {
			ops.rename = sequenceErrors(stageErr, fallbackErr)
		}, want: fallbackErr, wantRename: 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			file := &stubReportFile{path: filepath.Join(t.TempDir(), "temporary")}
			renameCalls := 0
			ops := atomicWriteOperations{
				mkdirAll:   func(string, os.FileMode) error { return nil },
				createTemp: func(string, string) (atomicReportFile, error) { return file, nil },
				remove:     func(string) error { return nil },
				rename:     func(string, string) error { renameCalls++; return nil },
			}
			if testCase.configure != nil {
				testCase.configure(&ops, file)
				wrappedRename := ops.rename
				renameCalls = 0
				ops.rename = func(oldPath, newPath string) error {
					renameCalls++
					return wrappedRename(oldPath, newPath)
				}
			}
			err := atomicWriteWith(filepath.Join(t.TempDir(), "report.json"), []byte("report"), ops)
			if testCase.want == nil && err != nil {
				t.Fatalf("atomic write error = %v", err)
			}
			if testCase.want != nil && !errors.Is(err, testCase.want) {
				t.Fatalf("atomic write error = %v, want %v", err, testCase.want)
			}
			if renameCalls != testCase.wantRename {
				t.Fatalf("rename calls = %d, want %d", renameCalls, testCase.wantRename)
			}
		})
	}
}

func TestWriteReportsNamesTheArtifactWhoseAtomicWriteFailed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest"), []byte("blocks directory"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	err := WriteReports(root, validReportFixture())
	if err == nil || !strings.Contains(err.Error(), "write report") || !strings.Contains(err.Error(), ".goatest") {
		t.Fatalf("WriteReports error = %v", err)
	}
	artifacts, readErr := os.ReadDir(filepath.Join(root, "reports", "runs", "fixture-run"))
	if readErr != nil || len(artifacts) != 5 {
		t.Fatalf("complete immutable history was not published before index failure: entries=%d error=%v", len(artifacts), readErr)
	}
	runs, readErr := os.ReadDir(filepath.Join(root, "reports", "runs"))
	if readErr != nil || len(runs) != 1 || runs[0].Name() != "fixture-run" {
		t.Fatalf("history contains staging residue: entries=%v error=%v", runs, readErr)
	}
}

func sequenceErrors(errors ...error) func(string, string) error {
	index := 0
	return func(string, string) error {
		if index >= len(errors) {
			return nil
		}
		err := errors[index]
		index++
		return err
	}
}

func validReportFixture() report.Report {
	return report.Report{
		Schema: report.SchemaV1, RunID: "fixture-run", RunKind: report.RunFull, Verdict: report.VerdictAssured,
		Contract: "standard-v1", Snapshot: "fixture-snapshot",
		Scope: report.Scope{
			Requested: report.ScopeSpec{Kind: "full", Project: "."},
			Resolved:  report.ScopeSpec{Kind: "full", Project: "."},
		},
		Repository:    report.Repository{Module: "example.test/fixture", Git: report.Git{Available: true, Commit: "commit", MergeBase: "commit"}},
		Configuration: report.Configuration{Digest: appTestDigest("a")},
		Toolchain:     report.Toolchain{Go: "go1.26.6", Goatest: "devel", GoMutants: "v0.1.2", OS: "windows", Arch: "amd64"},
		Timing:        report.Timing{StartedAt: "2026-01-01T00:00:00Z", FinishedAt: "2026-01-01T00:00:01Z", DurationMS: 1000},
	}
}

const twoProtectedRuns = 2

func reportWithRunID(t *testing.T, runID string) []byte {
	t.Helper()
	encoded, err := json.Marshal(report.Report{
		Schema: report.SchemaV1, RunID: runID, RunKind: report.RunFull,
		Verdict: report.VerdictAssured, Contract: "standard-v1", Snapshot: "snapshot",
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
		Toolchain: report.Toolchain{
			Go: "go1.26.6", Goatest: "devel", GoMutants: "v0.1.2", OS: "darwin", Arch: "arm64",
		},
		Timing: report.Timing{
			StartedAt: "2026-01-01T00:00:00Z", FinishedAt: "2026-01-01T00:00:01Z", DurationMS: 1000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestProtectedRunIdsNameOnlyTheIndexesThatCarryOne(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		any       string
		full      string
		protected int
	}{
		{name: "two indexes naming two runs", any: "run-a", full: "run-b", protected: twoProtectedRuns},
		{name: "two indexes naming one run", any: "run-a", full: "run-a", protected: 1},
		{name: "one index alone", any: "run-a", protected: 1},
		{name: "an index that names no run", any: "", protected: 0},
		{name: "no index at all", protected: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			internal := filepath.Join(root, ".goatest")
			if err := os.MkdirAll(internal, filemode.ReadableDirectory); err != nil {
				t.Fatal(err)
			}
			written := map[string]string{}
			if test.any != "" || test.name == "an index that names no run" {
				written["latest-any.json"] = test.any
			}
			if test.full != "" {
				written["latest-full.json"] = test.full
			}
			for name, runID := range written {
				if err := os.WriteFile(filepath.Join(internal, name),
					reportWithRunID(t, runID), filemode.ReadableFile); err != nil {
					t.Fatal(err)
				}
			}
			if got := len(protectedRunIDs(root)); got != test.protected {
				t.Fatalf("%s protected %d runs, want %d", test.name, got, test.protected)
			}
		})
	}
}

func TestADurableSweepThatCouldNotReadTheConfigurationSaysSo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = \"one\"\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	service := Service{Root: root, Progress: &progress}
	service.collectDurableArtifacts(root, filepath.Join(root, ".goatest", "cache"))
	for _, want := range []string{"reports-gc-unavailable", "repair-gc-unavailable"} {
		if !strings.Contains(progress.String(), want) {
			t.Errorf("the sweep said %q, want it to say %q", progress.String(), want)
		}
	}
}

func TestADurableSweepThatRanSaysNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".goatest.toml"),
		[]byte("version = 1\ncontract = \"standard-v1\"\n"), filemode.PrivateFile); err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	service := Service{Root: root, Progress: &progress}
	service.collectDurableArtifacts(root, filepath.Join(root, ".goatest", "cache"))
	if progress.Len() != 0 {
		t.Fatalf("a sweep over a repository it can read said %q, want nothing", progress.String())
	}
}
