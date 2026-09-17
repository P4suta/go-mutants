// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
)

func TestRunsNeverWriteIntoTheCorpus(t *testing.T) {
	t.Parallel()

	corpus := testkit.Fixture(t, "killable")
	opts := options(t, "killable")
	if testkit.SamePath(opts.WorkspaceRoot, corpus) {
		t.Fatalf("the run was pointed at the corpus module %s itself", corpus)
	}
	if len(opts.Config.Report.Formats) == 0 {
		t.Fatal("the run was told to write no artefacts, so where it would have written them is not being tested")
	}

	reports := reportDirectory(t, opts.Config.Report.Directory)
	before := corpusReportDirectories(t, reports)
	if len(before) != 0 {
		t.Logf("%s/ was already in the corpus modules %v before this run, and is not attributed to it",
			reports, before)
	}

	outcome, events, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	artifacts := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(opts.Config.Report.Directory))
	projection := filepath.Join(artifacts, report.ProjectionFileName)
	if outcome.Artifacts.ProjectionPath != projection {
		t.Errorf("the run reports its projection at %s, want %s", outcome.Artifacts.ProjectionPath, projection)
	}
	if outcome.Artifacts.HTMLPath != filepath.Join(artifacts, report.HTMLFileName) {
		t.Errorf("the run reports its page at %s, want %s in the same directory",
			outcome.Artifacts.HTMLPath, report.HTMLFileName)
	}
	if err := report.ValidateProjection(testkit.ReadFile(t, projection)); err != nil {
		t.Errorf("the published %s does not satisfy the mutation-testing schema: %v", report.ProjectionFileName, err)
	}
	if got := testkit.Entries(t, artifacts); len(got) != 2 {
		t.Errorf("the run wrote %v into %s, want the two artefacts and nothing else", got, opts.Config.Report.Directory)
	}
	validateDocument(t, testkit.ReadFile(t, published(t, events).RunPath))

	for _, name := range corpusReportDirectories(t, reports) {
		if slices.Contains(before, name) {
			continue
		}
		t.Errorf("%s/ appeared in the corpus module %s while the suite was running: something ran inside the corpus instead of inside a copy of it",
			reports, name)
	}
	if status := testkit.Git(t, testkit.Root(t), "status", "--porcelain", "--", testkit.FixturesDir); status != "" {
		t.Errorf("the corpus is not as it was committed, so a suite wrote into it or an edit is in progress:\n%s", status)
	}
}

func reportDirectory(t *testing.T, directory string) string {
	t.Helper()
	first, _, _ := strings.Cut(filepath.ToSlash(directory), "/")
	if first == "" || first == "." {
		t.Fatalf("report.directory is %q, which names no directory in the workspace", directory)
	}
	return first
}

func corpusReportDirectories(t *testing.T, reports string) []string {
	t.Helper()
	corpus := filepath.Join(testkit.Root(t), testkit.FixturesDir)
	var found []string
	for _, name := range testkit.FixtureNames(t) {
		_, err := os.Stat(filepath.Join(corpus, name, reports))
		switch {
		case err == nil:
			found = append(found, name)
		case errors.Is(err, fs.ErrNotExist):
		default:
			t.Fatalf("looking for %s in the corpus module %s: %v", reports, name, err)
		}
	}
	return found
}

func TestRunLeavesNothingUnderItsTempDirectory(t *testing.T) {
	t.Parallel()

	opts := options(t, "simple")
	if opts.TempDirectory == "" {
		t.Fatal("the run was given no temporary directory of its own, so what it left behind is not observable here")
	}

	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Status != StatusOK {
		t.Fatalf("status = %s, want %s", outcome.Status, StatusOK)
	}

	owned := filepath.Dir(outcome.SnapshotRoot)
	if filepath.Base(outcome.SnapshotRoot) != snapshot.TreeName || !testkit.SamePath(filepath.Dir(owned), opts.TempDirectory) {
		t.Errorf("the snapshot was created at %s, want %s below a directory the run owns in %s",
			outcome.SnapshotRoot, snapshot.TreeName, opts.TempDirectory)
	}
	if _, err := os.Stat(owned); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the snapshot directory %s survived the run (stat error %v)", owned, err)
	}
	if left := testkit.Entries(t, opts.TempDirectory); len(left) != 0 {
		t.Errorf("the run left %v behind in the temporary directory it was given", left)
	}
	if len(outcome.Warnings) != 0 {
		t.Errorf("a clean run published %v", outcome.Warnings)
	}
}
