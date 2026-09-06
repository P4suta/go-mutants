// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// Where a run is allowed to write, proven against a real one.
//
// The rest of the suite is about what a run measures; this file is about the
// two directories it touches on the way — the workspace it was pointed at, and
// the temporary parent it was given — and it exists because both promises used
// to be untestable here. The engine's own tests ran *inside* `fixtures/`, so
// the only way to keep the corpus clean was to turn the project artefacts off,
// which meant the one code path that writes into a user's tree was exercised
// nowhere in this package; and the snapshot lived under `os.TempDir()`, so
// "nothing was left behind" could only be asserted by redirecting a
// process-wide global.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/engine/...
//
// The comment above is deliberately not a package doc — integration_test.go
// carries this package's — which is what the blank line below is for.

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

// TestRunsNeverWriteIntoTheCorpus is the guard the whole migration exists for:
// a run writes its artefacts into the workspace it was given, and the corpus
// module that workspace was copied from is not touched.
//
// Both halves have to be asserted together, and that is the point. "The corpus
// is clean" is trivially true of a run that writes no artefacts at all — which
// is exactly what this package used to arrange, and what made the artefact path
// untested here. So the same run has to produce `reports/mutation/` in its own
// copy: the files are proof the default really was in force, and the corpus
// being untouched is then a fact about where the run was pointed rather than
// about what it was told not to do.
//
// The corpus is checked twice over, because neither check alone is enough.
// `git status` would notice a fixture edited, added to or deleted, and would
// *not* notice the artefacts: `**/reports/mutation/` is in .gitignore precisely
// so that a manual run inside a fixture cannot be committed. The report
// directories are what the other half looks for, and it looks in every corpus
// module rather than only in the one this run copied — a test that names its own
// fixture would pass over a run that wrote into somebody else's.
//
// That second half is a difference rather than a state, which the first half
// can afford not to be: a fixture edited in the working tree is somebody's
// deliberate work in progress and shows up as one, where a `reports/` a
// developer left in a fixture a fortnight ago is indistinguishable from one this
// run just made. Blaming a run for that would make this a guard people learn to
// delete, so the corpus is listed before the run as well as after and only what
// appeared in between fails.
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

	// The directory a run creates in the workspace it is given, taken from the
	// setting rather than spelled out: a `report.directory` that moves to
	// `build/mutation` moves what this looks for with it.
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

	// The artefacts, in the copy, where a user would find them.
	artifacts := filepath.Join(opts.WorkspaceRoot, filepath.FromSlash(opts.Config.Report.Directory))
	projection := filepath.Join(artifacts, report.ProjectionFileName)
	if outcome.Artifacts.ProjectionPath != projection {
		t.Errorf("the run reports its projection at %s, want %s", outcome.Artifacts.ProjectionPath, projection)
	}
	if outcome.Artifacts.HTMLPath != filepath.Join(artifacts, report.HTMLFileName) {
		t.Errorf("the run reports its page at %s, want %s in the same directory",
			outcome.Artifacts.HTMLPath, report.HTMLFileName)
	}
	// Held against the shipped schema rather than merely opened: a file of the
	// right name holding a document nobody can read is the failure this catches.
	if err := report.ValidateProjection(testkit.ReadFile(t, projection)); err != nil {
		t.Errorf("the published %s does not satisfy the mutation-testing schema: %v", report.ProjectionFileName, err)
	}
	if got := testkit.Entries(t, artifacts); len(got) != 2 {
		t.Errorf("the run wrote %v into %s, want the two artefacts and nothing else", got, opts.Config.Report.Directory)
	}
	// And the run's own record, which is filed under the history root rather
	// than in the workspace, is still the document the schema describes.
	validateDocument(t, testkit.ReadFile(t, published(t, events).RunPath))

	// No corpus module grew one while this test ran.
	for _, name := range corpusReportDirectories(t, reports) {
		if slices.Contains(before, name) {
			continue
		}
		t.Errorf("%s/ appeared in the corpus module %s while the suite was running: something ran inside the corpus instead of inside a copy of it",
			reports, name)
	}
	// Last, because it is the one assertion something other than this suite can
	// answer — an uncommitted fixture edit of the developer's own reads exactly
	// like a suite that wrote where it reads, and the listing is printed so that
	// the two can be told apart. Tracked files only: the ignored half is the
	// report directories above, where "it was already there" is a legitimate
	// answer and `git status --ignored` cannot tell that from "it just appeared".
	if status := testkit.Git(t, testkit.Root(t), "status", "--porcelain", "--", testkit.FixturesDir); status != "" {
		t.Errorf("the corpus is not as it was committed, so a suite wrote into it or an edit is in progress:\n%s", status)
	}
}

// reportDirectory is the first segment of a `report.directory`: the directory a
// run creates in the workspace it is pointed at, which for the default
// `reports/mutation` is `reports`.
//
// The whole setting would be the narrower thing to look for and the wrong one.
// A run that created `reports/` and then failed before writing `mutation/` still
// wrote into the corpus, and the empty directory it left is exactly the evidence.
func reportDirectory(t *testing.T, directory string) string {
	t.Helper()
	first, _, _ := strings.Cut(filepath.ToSlash(directory), "/")
	if first == "" || first == "." {
		t.Fatalf("report.directory is %q, which names no directory in the workspace", directory)
	}
	return first
}

// corpusReportDirectories names every corpus module holding one, sorted as
// [testkit.FixtureNames] sorts.
//
// Every module, not merely the one a test copied: the run under test is one of
// several in a parallel suite, and a guard that looked only where its own test
// had been would leave the interesting case — a run pointed at the wrong tree
// entirely — to whoever eventually noticed the untracked files.
func corpusReportDirectories(t *testing.T, reports string) []string {
	t.Helper()
	// The corpus directory is joined here rather than asking [testkit.Fixture]
	// for each name, which would re-check every go.mod that [testkit.FixtureNames]
	// has already checked and log a line for each on the way past.
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

// TestRunLeavesNothingUnderItsTempDirectory is the other promise: the run
// snapshots into the parent it was given and takes the whole of it away again.
//
// It is deliberately not the same test as
// [TestTempDirectoryIsWhereTheRunSnapshotsAndSweeps], which is about the
// *difference* between the named parent and the operating system's own and
// therefore has to redirect a process-wide global to observe it. This one is
// the plain promise as every other test in the suite now relies on it — no
// redirection, no global, and so free to run beside the rest.
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

	// The copy is the tree below the directory the run owns, and that directory
	// is directly under the temporary directory the run was told to use.
	owned := filepath.Dir(outcome.SnapshotRoot)
	if filepath.Base(outcome.SnapshotRoot) != snapshot.TreeName || !testkit.SamePath(filepath.Dir(owned), opts.TempDirectory) {
		t.Errorf("the snapshot was created at %s, want %s below a directory the run owns in %s",
			outcome.SnapshotRoot, snapshot.TreeName, opts.TempDirectory)
	}
	if _, err := os.Stat(owned); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the snapshot directory %s survived the run (stat error %v)", owned, err)
	}
	// The scratch directory beside it is gone too — the compiled test binaries,
	// the per-worker temporary directories and the coverage data underneath.
	if left := testkit.Entries(t, opts.TempDirectory); len(left) != 0 {
		t.Errorf("the run left %v behind in the temporary directory it was given", left)
	}
	if len(outcome.Warnings) != 0 {
		t.Errorf("a clean run published %v", outcome.Warnings)
	}
}
