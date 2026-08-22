// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/report"
)

// otherDigest is a second workspace: the same module measured after an edit,
// which is what a content-addressed history directory means. See
// [report.StoredWorkspace].
var otherDigest = strings.Repeat("cd", 32)

// storeRun files one run under root, with the identity and clock a listing test
// needs, and returns the path it was written to.
func storeRun(t *testing.T, root, runID, digest string, finished time.Time) string {
	t.Helper()
	opts := fixtureOptions(t)
	opts.RunID = runID
	opts.WorkspaceDigest = digest
	opts.Started = finished.Add(-fixtureDuration)
	opts.Finished = finished

	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	runPath, _, err := report.History{Root: root}.Write(r)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	return runPath
}

// moment parses one of the fixed clocks these tests state.
func moment(t *testing.T, stamp string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("parsing %q: %v", stamp, err)
	}
	return at
}

// TestListReadsEveryStoredRunNewestFirst is the shape `report list` prints, and
// the one ordering promise it makes.
func TestListReadsEveryStoredRunNewestFirst(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	storeRun(t, root, "20260219T101500Z-2222", fixtureDigest, moment(t, "2026-02-19T10:15:42Z"))

	listing, err := report.History{Root: root}.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing.Workspaces) != 1 {
		t.Fatalf("List found %d workspaces, want 1", len(listing.Workspaces))
	}
	workspace := listing.Workspaces[0]
	if workspace.Digest != fixtureDigest {
		t.Errorf("workspace digest = %q, want the marker's", workspace.Digest)
	}
	if len(workspace.Runs) != 2 {
		t.Fatalf("the workspace holds %d runs, want 2 — latest.json must not be counted twice", len(workspace.Runs))
	}
	if workspace.Runs[0].RunID != "20260219T101500Z-2222" {
		t.Errorf("the newest run is %q", workspace.Runs[0].RunID)
	}
	if workspace.Latest != "20260219T101500Z-2222" {
		t.Errorf("latest.json names %q", workspace.Latest)
	}
	run := workspace.Runs[0]
	if run.ModulePath != fixtureModulePath {
		t.Errorf("module path = %q, want %q", run.ModulePath, fixtureModulePath)
	}
	if run.Status != report.StatusCompleted {
		t.Errorf("status = %q", run.Status)
	}
	if !run.FinishedAt.Equal(moment(t, "2026-02-19T10:15:42Z")) {
		t.Errorf("finished at %s", run.FinishedAt)
	}
	if _, ok := run.Score(); !ok {
		t.Error("the fixture run measured mutants and reports no score")
	}
	if run.Bytes == 0 {
		t.Error("the run's size on disk was not measured")
	}
	if len(workspace.Damaged) != 0 || len(listing.Skipped) != 0 {
		t.Errorf("a clean store reported %d damaged and %d skipped", len(workspace.Damaged), len(listing.Skipped))
	}
}

// TestListSurvivesADamagedDocument is the property that makes this a listing
// rather than a parser: one unreadable file must not cost a user the runs
// beside it, and it must be named rather than dropped.
func TestListSurvivesADamagedDocument(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	good := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	broken := filepath.Join(filepath.Dir(good), "20260220T110000Z-3333.json")
	if err := os.WriteFile(broken, []byte("{\"document_type\": \"go-mutants/run-report\","), 0o600); err != nil {
		t.Fatalf("writing the damaged document: %v", err)
	}
	foreign := filepath.Join(filepath.Dir(good), "20260221T110000Z-4444.json")
	if err := os.WriteFile(foreign, []byte(`{"document_type":"something/else","schema_version":1}`), 0o600); err != nil {
		t.Fatalf("writing the foreign document: %v", err)
	}

	listing, err := report.History{Root: root}.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	workspace := listing.Workspaces[0]
	if len(workspace.Runs) != 1 || workspace.Runs[0].RunID != "20260218T091500Z-1111" {
		t.Fatalf("the readable run was lost: %+v", workspace.Runs)
	}
	if len(workspace.Damaged) != 2 {
		t.Fatalf("%d documents were reported as damaged, want 2: %+v", len(workspace.Damaged), workspace.Damaged)
	}
	for _, row := range workspace.Damaged {
		if row.Reason == "" {
			t.Errorf("%s is damaged for no stated reason", row.Path)
		}
		if strings.HasPrefix(row.Reason, "GOM") {
			t.Errorf("a damaged row carries a code, which the listing already implies: %q", row.Reason)
		}
	}
	if !strings.Contains(workspace.Damaged[1].Reason, "run-report") {
		t.Errorf("the wrong document type is not named: %q", workspace.Damaged[1].Reason)
	}
}

// TestListSkipsWhatIsNotGoMutants. The store lives in a directory shared with
// every other tool on the machine, so a directory with no marker is reported
// and never read.
func TestListSkipsWhatIsNotGoMutants(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))

	base := filepath.Join(root, report.WorkspacesDirName)
	unmarked := filepath.Join(base, "0000000000000000")
	if err := os.MkdirAll(filepath.Join(unmarked, report.RunsDirName), 0o700); err != nil {
		t.Fatalf("creating the unmarked directory: %v", err)
	}
	garbled := filepath.Join(base, "1111111111111111")
	if err := os.MkdirAll(garbled, 0o700); err != nil {
		t.Fatalf("creating the garbled directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(garbled, report.MarkerFileName), []byte("not a marker\n"), 0o600); err != nil {
		t.Fatalf("writing the garbled marker: %v", err)
	}

	listing, err := report.History{Root: root}.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing.Workspaces) != 1 {
		t.Errorf("List read %d workspaces, want only the marked one", len(listing.Workspaces))
	}
	if len(listing.Skipped) != 2 {
		t.Fatalf("%d directories were skipped, want 2: %+v", len(listing.Skipped), listing.Skipped)
	}
	if !strings.Contains(listing.Skipped[0].Reason, "no go-mutants workspace marker") {
		t.Errorf("the unmarked directory's reason = %q", listing.Skipped[0].Reason)
	}
	if strings.HasPrefix(listing.Skipped[1].Reason, "GOM") {
		t.Errorf("the skipped row repeats the code: %q", listing.Skipped[1].Reason)
	}
}

// copyTree copies a directory and everything under it, which is what a restored
// CI cache, an `xcopy`, or a `cp -r` of somebody's cache directory leaves
// behind: the same documents and the same marker, under a name this build would
// never have chosen.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(to, 0o700); err != nil {
		t.Fatalf("creating %s: %v", to, err)
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatalf("reading %s: %v", from, err)
	}
	for _, entry := range entries {
		source, destination := filepath.Join(from, entry.Name()), filepath.Join(to, entry.Name())
		if entry.IsDir() {
			copyTree(t, source, destination)
			continue
		}
		data, readErr := os.ReadFile(source)
		if readErr != nil {
			t.Fatalf("reading %s: %v", source, readErr)
		}
		if err = os.WriteFile(destination, data, 0o600); err != nil {
			t.Fatalf("writing %s: %v", destination, err)
		}
	}
}

// TestListSkipsAWorkspaceDirectoryThatIsACopy is the seam between what a
// listing says and what a clean can do.
//
// A workspace directory copied under another name — a CI cache restored to a
// different key, a backup taken by hand — carries a marker naming the original.
// [History.RemoveRuns] is asked for a digest and rebuilds the original's path
// from it, so a copy listed as a workspace would be a directory `report clean`
// reports as swept and cannot reach: the run documents in it survive the clean
// that claimed them. Listing it as skipped is what keeps the two commands
// telling one story.
func TestListSkipsAWorkspaceDirectoryThatIsACopy(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	original := filepath.Dir(filepath.Dir(runPath))
	copied := filepath.Join(filepath.Dir(original), "0123456789abcdef")
	copyTree(t, original, copied)

	store := report.History{Root: root}
	listing, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing.Workspaces) != 1 {
		t.Fatalf("List read %d workspaces, want only the one this store named: %+v",
			len(listing.Workspaces), listing.Workspaces)
	}
	if got := listing.Workspaces[0].Key; got != report.WorkspaceKey(fixtureDigest) {
		t.Errorf("the listed workspace is %q, want the key its digest names", got)
	}
	if len(listing.Skipped) != 1 {
		t.Fatalf("%d directories were skipped, want the copy: %+v", len(listing.Skipped), listing.Skipped)
	}
	if listing.Skipped[0].Name != filepath.Base(copied) {
		t.Errorf("the skipped directory is %q, want the copy %q", listing.Skipped[0].Name, filepath.Base(copied))
	}
	if !strings.Contains(listing.Skipped[0].Reason, report.WorkspaceKey(fixtureDigest)) {
		t.Errorf("the reason does not name the directory the marker belongs to: %q", listing.Skipped[0].Reason)
	}
	if strings.HasPrefix(listing.Skipped[0].Reason, "GOM") {
		t.Errorf("the skipped row repeats a code: %q", listing.Skipped[0].Reason)
	}

	// The promise the listing makes to the sweep that follows it: every
	// directory it named as a workspace is one a clean of that workspace's
	// digest actually empties.
	for _, workspace := range listing.Workspaces {
		removed, removeErr := store.RemoveRuns(workspace.Digest)
		if removeErr != nil {
			t.Fatalf("RemoveRuns(%s): %v", workspace.Key, removeErr)
		}
		if _, statErr := os.Stat(filepath.Join(workspace.Dir, report.RunsDirName)); !os.IsNotExist(statErr) {
			t.Errorf("the listing named %s, the sweep reported %d documents removed from %s, "+
				"and that directory still holds runs: %v",
				workspace.Dir, removed.Runs, removed.Dir, statErr)
		}
	}
	// And the copy is left exactly as it was found, which is what a skipped row
	// promises.
	if _, err = os.Stat(filepath.Join(copied, report.RunsDirName, "20260218T091500Z-1111.json")); err != nil {
		t.Errorf("a directory the listing would not touch was cleaned anyway: %v", err)
	}
}

// TestNewestFirstOrdersTwoCopiesOfOneRun. The comparator claims a total order,
// and the claim has to survive the case its first two keys cannot separate: one
// run reachable under two paths, where the finish time and the run id are equal
// because it is the same document. Answering 0 there would leave an unstable
// sort free to return either, which is a `report latest` that names a different
// file on two runs of the same command.
func TestNewestFirstOrdersTwoCopiesOfOneRun(t *testing.T) {
	t.Parallel()

	finished := moment(t, "2026-02-18T09:15:42Z")
	first := report.StoredRun{RunID: "20260218T091500Z-1111", Path: "/a/runs/x.json", FinishedAt: finished}
	second := report.StoredRun{RunID: "20260218T091500Z-1111", Path: "/b/runs/x.json", FinishedAt: finished}
	if report.NewestFirst(first, second) == 0 {
		t.Error("two documents at two paths compare equal, so their order is whatever the sort felt like")
	}
	if report.NewestFirst(first, second) != -report.NewestFirst(second, first) {
		t.Error("the comparator does not answer the two orders of one pair consistently")
	}
}

// TestListOfAMachineThatHasNeverRunIsEmptyAndNotAFailure. "Nothing here yet" is
// an answer, and the difference between it and "I could not look" is exactly
// what the error return is for.
func TestListOfAMachineThatHasNeverRunIsEmpty(t *testing.T) {
	t.Parallel()

	listing, err := report.History{Root: filepath.Join(t.TempDir(), "never-used")}.List()
	if err != nil {
		t.Fatalf("List over an absent root: %v", err)
	}
	if len(listing.Workspaces) != 0 || len(listing.Skipped) != 0 {
		t.Errorf("an absent store is not empty: %+v", listing)
	}
}

// TestRemoveRunsDeletesTheHistoryAndNothingElse walks what `report clean` is
// allowed to touch. The marker stays, because the directory's identity outlives
// its contents, and `outcomes/` stays, because it is the outcome cache's.
func TestRemoveRunsDeletesTheHistoryAndNothingElse(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	storeRun(t, root, "20260219T101500Z-2222", fixtureDigest, moment(t, "2026-02-19T10:15:42Z"))
	dir := filepath.Dir(filepath.Dir(runPath))

	outcomes := filepath.Join(dir, "outcomes", "abcdef")
	if err := os.MkdirAll(outcomes, 0o700); err != nil {
		t.Fatalf("creating the outcome directory: %v", err)
	}
	entry := filepath.Join(outcomes, "kept.json")
	if err := os.WriteFile(entry, []byte("{}"), 0o600); err != nil {
		t.Fatalf("writing the outcome: %v", err)
	}

	removed, err := report.History{Root: root}.RemoveRuns(fixtureDigest)
	if err != nil {
		t.Fatalf("RemoveRuns: %v", err)
	}
	// Two runs and the pointer to the newest.
	if removed.Runs != 3 {
		t.Errorf("removed %d documents, want 3 (two runs and latest.json)", removed.Runs)
	}
	if removed.Bytes == 0 {
		t.Error("the removed documents are reported as taking up nothing")
	}
	if removed.Dir != dir {
		t.Errorf("removed.Dir = %q, want %q", removed.Dir, dir)
	}

	if _, err = os.Stat(filepath.Join(dir, report.RunsDirName)); !os.IsNotExist(err) {
		t.Errorf("the runs directory survived: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, report.LatestFileName)); !os.IsNotExist(err) {
		t.Errorf("the pointer to the newest run survived: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, report.MarkerFileName)); err != nil {
		t.Errorf("the ownership marker was deleted: %v", err)
	}
	if _, err = os.Stat(entry); err != nil {
		t.Errorf("a cached outcome was deleted by a history clean: %v", err)
	}

	// And a second clean is not a failure: there is nothing left, which is what
	// was asked for.
	again, err := report.History{Root: root}.RemoveRuns(fixtureDigest)
	if err != nil {
		t.Fatalf("RemoveRuns over an emptied history: %v", err)
	}
	if again.Runs != 0 {
		t.Errorf("a second clean removed %d documents", again.Runs)
	}
}

// TestRemoveRunsRefusesADirectoryThatIsNotOurs is the whole safety argument for
// a command that deletes files in the operating system's cache directory.
func TestRemoveRunsRefusesADirectoryThatIsNotOurs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(otherDigest))
	runs := filepath.Join(dir, report.RunsDirName)
	if err := os.MkdirAll(runs, 0o700); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}
	somebodyElses := filepath.Join(runs, "20260218T091500Z-1111.json")
	if err := os.WriteFile(somebodyElses, []byte("{}"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	_, err := report.History{Root: root}.RemoveRuns(otherDigest)
	if err == nil {
		t.Fatal("an unmarked directory was cleaned")
	}
	if got := report.CodeOf(err); got != report.CodeForeignWorkspace {
		t.Errorf("code = %q, want %q", got, report.CodeForeignWorkspace)
	}
	if _, err = os.Stat(somebodyElses); err != nil {
		t.Errorf("the refusal deleted the file anyway: %v", err)
	}

	// A marker this build did not write is refused for the same reason, and
	// says so in its own words rather than being mistaken for an absent one.
	if err = os.WriteFile(filepath.Join(dir, report.MarkerFileName), []byte("go-mutants-workspace-v9\n"), 0o600); err != nil {
		t.Fatalf("writing the marker: %v", err)
	}
	_, err = report.History{Root: root}.RemoveRuns(otherDigest)
	if report.CodeOf(err) != report.CodeForeignWorkspace {
		t.Errorf("a marker from another format was answered with %v", err)
	}
	if _, err = os.Stat(somebodyElses); err != nil {
		t.Errorf("the refusal deleted the file anyway: %v", err)
	}
}

// TestRemoveRunsRefusesAWorkspaceDirectoryThatLeavesTheStore. The containment
// check is what makes deleting files in the operating system's cache directory
// acceptable, and a check that compares two strings does not make it: the store
// is a directory anything on the machine can write to, so a workspace directory
// replaced by a link to somewhere else is lexically inside the store and
// physically wherever it points.
func TestRemoveRunsRefusesAWorkspaceDirectoryThatLeavesTheStore(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	dir := filepath.Dir(filepath.Dir(runPath))

	// The history — marker, runs and all — moved out of the store, with a link
	// left behind under the name the store would rebuild.
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(dir, outside); err != nil {
		t.Fatalf("moving the workspace directory out of the store: %v", err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Skipf("this platform will not let the test create a symbolic link: %v", err)
	}

	_, err := report.History{Root: root}.RemoveRuns(fixtureDigest)
	if err == nil {
		t.Fatal("a workspace directory linked out of the store was cleaned as if it were in it")
	}
	if got := report.CodeOf(err); got != report.CodeHistoryNotRemoved {
		t.Errorf("code = %q, want %q (%v)", got, report.CodeHistoryNotRemoved, err)
	}
	victim := filepath.Join(outside, report.RunsDirName, "20260218T091500Z-1111.json")
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Errorf("history outside the store was deleted through the link: %v", statErr)
	}
}

// TestRemoveRunsUnlinksALinkedRunsDirectory is the other half of the
// containment argument, and the reason the last element of a path is compared
// as it is spelled rather than as it resolves.
//
// [os.RemoveAll] unlinks a symbolic link instead of walking through it, so a
// linked leaf deletes the link and leaves whatever it pointed at alone: it is
// the directories *leading* to a path that can carry a deletion out of the
// store, because walking them is what follows them. Resolving the leaf too
// would refuse this — and would refuse it by treating "delete this link" as
// "delete the target", which is the one reading of it that is not true.
func TestRemoveRunsUnlinksALinkedRunsDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	dir := filepath.Dir(filepath.Dir(runPath))

	runs := filepath.Join(dir, report.RunsDirName)
	outside := filepath.Join(t.TempDir(), "runs")
	if err := os.Rename(runs, outside); err != nil {
		t.Fatalf("moving the run documents out of the store: %v", err)
	}
	if err := os.Symlink(outside, runs); err != nil {
		t.Skipf("this platform will not let the test create a symbolic link: %v", err)
	}

	store := report.History{Root: root}
	if _, err := store.RemoveRuns(fixtureDigest); err != nil {
		t.Fatalf("RemoveRuns over a linked runs directory: %v", err)
	}
	if _, err := os.Lstat(runs); !os.IsNotExist(err) {
		t.Errorf("the link inside the store survived the clean: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "20260218T091500Z-1111.json")); err != nil {
		t.Errorf("deleting the link followed it out of the store: %v", err)
	}
}

// TestRemoveRunsRefusesADigestThatIsNotOne. The directory is named from the
// digest, so a value that is not a digest is the one input that could point the
// deletion somewhere unintended.
func TestRemoveRunsRefusesADigestThatIsNotOne(t *testing.T) {
	t.Parallel()

	for _, digest := range []string{"", "..", "../../etc", strings.Repeat("z", 64)} {
		_, err := report.History{Root: t.TempDir()}.RemoveRuns(digest)
		if report.CodeOf(err) != report.CodeInvalidWorkspaceDigest {
			t.Errorf("RemoveRuns(%q) = %v, want the invalid-digest refusal", digest, err)
		}
	}
}

// TestReadStoredIsVerbatim. `report latest --json` prints an archive, and an
// archive that is re-encoded on the way out is not an archive.
func TestReadStoredIsVerbatim(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	want, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("reading the stored run: %v", err)
	}
	got, err := report.ReadStored(runPath)
	if err != nil {
		t.Fatalf("ReadStored: %v", err)
	}
	if string(got) != string(want) {
		t.Error("ReadStored did not return the bytes on disk")
	}

	_, err = report.ReadStored(filepath.Join(root, "not-there.json"))
	if report.CodeOf(err) != report.CodeHistoryUnreadable {
		t.Errorf("ReadStored of a missing file = %v, want %q", err, report.CodeHistoryUnreadable)
	}
}
