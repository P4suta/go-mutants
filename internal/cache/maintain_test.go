// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cache_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/report"
)

// populated builds a cache root holding one workspace with three stored
// outcomes, and returns the root and the directory they are filed in.
func populated(t *testing.T) (root, dir string) {
	t.Helper()
	root = t.TempDir()
	store := open(t, root, baseContext())
	for _, id := range mutantIDs {
		if err := store.Put(id, killedEntry()); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	return root, store.Dir()
}

// age backdates a file's modification time, which is what `cache gc` reads.
func age(t *testing.T, path string, d time.Duration) {
	t.Helper()
	when := time.Now().Add(-d)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("backdating %s: %v", path, err)
	}
}

// TestStatusCountsWhatIsStored is `cache status` over a cache with something in
// it.
func TestStatusCountsWhatIsStored(t *testing.T) {
	t.Parallel()

	root, _ := populated(t)
	survey, err := cache.Status(root)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got, want := len(survey.Workspaces), 1; got != want {
		t.Fatalf("Status found %d workspaces, want %d", got, want)
	}
	workspace := survey.Workspaces[0]
	if got, want := workspace.Entries, len(mutantIDs); got != want {
		t.Errorf("entries = %d, want %d", got, want)
	}
	if workspace.Contexts != 1 {
		t.Errorf("contexts = %d, want 1", workspace.Contexts)
	}
	if workspace.Bytes <= 0 {
		t.Errorf("bytes = %d, want the size of three files", workspace.Bytes)
	}
	if workspace.Newest.IsZero() {
		t.Error("the newest entry has no time")
	}
	if workspace.Digest != baseContext().WorkspaceDigest {
		t.Errorf("the workspace digest is %s, want the one the marker names", workspace.Digest)
	}
	if survey.Entries() != workspace.Entries || survey.Bytes() != workspace.Bytes {
		t.Errorf("the totals (%d, %d) do not match the one workspace (%d, %d)",
			survey.Entries(), survey.Bytes(), workspace.Entries, workspace.Bytes)
	}
}

// TestStatusOfAnEmptyMachineIsNotAFailure: nothing has been cached here yet,
// which is an answer rather than an error.
func TestStatusOfAnEmptyMachineIsNotAFailure(t *testing.T) {
	t.Parallel()

	survey, err := cache.Status(filepath.Join(t.TempDir(), "never-used"))
	if err != nil {
		t.Fatalf("Status of an empty root: %v", err)
	}
	if len(survey.Workspaces) != 0 || survey.Entries() != 0 {
		t.Errorf("an empty root reported %+v", survey)
	}
}

// TestGCRemovesOnlyWhatIsOldEnough is the mtime window, checked on both sides
// of the cutoff and on it.
func TestGCRemovesOnlyWhatIsOldEnough(t *testing.T) {
	t.Parallel()

	root, dir := populated(t)
	age(t, filepath.Join(dir, mutantIDs[0]+".json"), 40*24*time.Hour)
	age(t, filepath.Join(dir, mutantIDs[1]+".json"), 31*24*time.Hour)
	// The third keeps today's time and must survive.

	cutoff := time.Now().AddDate(0, 0, -cache.DefaultGCDays)
	sweep, err := cache.GC(root, cutoff)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if got, want := sweep.Entries, 2; got != want {
		t.Errorf("GC removed %d entries, want %d", got, want)
	}
	if sweep.Workspaces != 1 {
		t.Errorf("GC touched %d workspaces, want 1", sweep.Workspaces)
	}
	if sweep.Bytes <= 0 {
		t.Errorf("GC freed %d bytes", sweep.Bytes)
	}
	store := open(t, root, baseContext())
	for i, id := range mutantIDs {
		_, found, lookupErr := store.Lookup(id)
		if lookupErr != nil {
			t.Fatalf("Lookup: %v", lookupErr)
		}
		if want := i == 2; found != want {
			t.Errorf("entry %d found = %t, want %t", i, found, want)
		}
	}
	// The context directory still holds the survivor, so it stays.
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("the context directory was removed with a live entry in it: %v", statErr)
	}
}

// TestGCPrunesAContextNothingIsLeftIn keeps a cache from accumulating thousands
// of empty directories, one per tool version anybody ever ran.
func TestGCPrunesAContextNothingIsLeftIn(t *testing.T) {
	t.Parallel()

	root, dir := populated(t)
	for _, id := range mutantIDs {
		age(t, filepath.Join(dir, id+".json"), 90*24*time.Hour)
	}
	sweep, err := cache.GC(root, time.Now().AddDate(0, 0, -cache.DefaultGCDays))
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if got, want := sweep.Entries, len(mutantIDs); got != want {
		t.Errorf("GC removed %d entries, want %d", got, want)
	}
	if sweep.Contexts != 1 {
		t.Errorf("GC removed %d emptied directories, want 1", sweep.Contexts)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("the emptied context directory is still there: %v", statErr)
	}
	// Never above it: `outcomes/` and the workspace directory are still the
	// store, and the run history lives in the same place.
	if _, statErr := os.Stat(filepath.Dir(dir)); statErr != nil {
		t.Errorf("gc removed the outcomes directory itself: %v", statErr)
	}
}

// TestGCLeavesATemporaryFileAlone: a concurrent run in the middle of an atomic
// write has a temporary file in the directory, and deleting it out from under
// that run would be the one thing a garbage collector must not do.
func TestGCLeavesATemporaryFileAlone(t *testing.T) {
	t.Parallel()

	root, dir := populated(t)
	temp := filepath.Join(dir, "go-mutants-cache-123.tmp")
	write(t, temp, "{}")
	for _, id := range mutantIDs {
		age(t, filepath.Join(dir, id+".json"), 90*24*time.Hour)
	}
	age(t, temp, 90*24*time.Hour)

	sweep, err := cache.GC(root, time.Now().AddDate(0, 0, -cache.DefaultGCDays))
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if sweep.Entries != len(mutantIDs) {
		t.Errorf("GC removed %d entries, want %d", sweep.Entries, len(mutantIDs))
	}
	if _, statErr := os.Stat(temp); statErr != nil {
		t.Errorf("gc deleted a temporary file another run was writing: %v", statErr)
	}
	if sweep.Contexts != 0 {
		t.Error("gc removed a directory that still had a file in it")
	}
}

// TestCleanRemovesTheOutcomesAndNothingElse is the boundary between this
// command and `report clean`: the run history filed in the same workspace
// directory is a record of what happened and is not the cache's to delete.
func TestCleanRemovesTheOutcomesAndNothingElse(t *testing.T) {
	t.Parallel()

	root, dir := populated(t)
	workspace := filepath.Dir(filepath.Dir(dir))
	runs := filepath.Join(workspace, report.RunsDirName, "20260218T091500Z-3f9c.json")
	write(t, runs, `{"document_type":"go-mutants/run-report"}`)
	latest := filepath.Join(workspace, report.LatestFileName)
	write(t, latest, `{"document_type":"go-mutants/run-report"}`)

	sweep, err := cache.Clean(root)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if got, want := sweep.Entries, len(mutantIDs); got != want {
		t.Errorf("Clean removed %d entries, want %d", got, want)
	}
	if sweep.Workspaces != 1 {
		t.Errorf("Clean touched %d workspaces, want 1", sweep.Workspaces)
	}
	outcomes := filepath.Join(workspace, cache.OutcomesDirName)
	if _, statErr := os.Stat(outcomes); !os.IsNotExist(statErr) {
		t.Errorf("the outcomes directory survived: %v", statErr)
	}
	for _, kept := range []string{runs, latest, filepath.Join(workspace, report.MarkerFileName)} {
		if _, statErr := os.Stat(kept); statErr != nil {
			t.Errorf("clean removed %s, which is not the cache's: %v", kept, statErr)
		}
	}
	// And a second clean is not a failure: there is simply nothing left.
	again, err := cache.Clean(root)
	if err != nil {
		t.Fatalf("Clean of an already clean cache: %v", err)
	}
	if again.Entries != 0 || again.Workspaces != 0 {
		t.Errorf("the second clean removed %+v", again)
	}
}

// TestADirectoryWithoutOurMarkerIsRefused is the safety property of every
// command in this file. The cache root is a directory in the operating system's
// cache that other programs also keep things in, and the marker is the whole of
// what makes deleting anything there defensible.
func TestADirectoryWithoutOurMarkerIsRefused(t *testing.T) {
	t.Parallel()

	root, _ := populated(t)
	base := filepath.Join(root, report.WorkspacesDirName)

	// Somebody else's directory, with something in it that would be deleted if
	// the marker were not checked.
	stranger := filepath.Join(base, "somebody-elses-tool")
	write(t, filepath.Join(stranger, cache.OutcomesDirName, "ctx", "a.json"), "{}")
	// And one carrying a marker this build did not write.
	impostor := filepath.Join(base, "0123456789abcdef")
	write(t, filepath.Join(impostor, report.MarkerFileName), "go-mutants-workspace-v9\nnot a digest\n")
	write(t, filepath.Join(impostor, cache.OutcomesDirName, "ctx", "b.json"), "{}")

	for _, sweep := range []func() (cache.Sweep, error){
		func() (cache.Sweep, error) { return cache.GC(root, time.Now()) },
		func() (cache.Sweep, error) { return cache.Clean(root) },
	} {
		result, err := sweep()
		if err != nil {
			t.Fatalf("the sweep failed: %v", err)
		}
		if got, want := len(result.Skipped), 2; got != want {
			t.Errorf("the sweep skipped %d directories, want %d: %+v", got, want, result.Skipped)
		}
	}
	for _, kept := range []string{
		filepath.Join(stranger, cache.OutcomesDirName, "ctx", "a.json"),
		filepath.Join(impostor, cache.OutcomesDirName, "ctx", "b.json"),
	} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("a sweep deleted %s, which is not go-mutants': %v", kept, err)
		}
	}

	survey, err := cache.Status(root)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(survey.Skipped) != 2 {
		t.Errorf("status skipped %+v, want two directories", survey.Skipped)
	}
	for _, row := range survey.Skipped {
		if row.Reason == "" {
			t.Errorf("the skipped directory %s carries no reason", row.Name)
		}
		if strings.HasPrefix(row.Reason, "GOM") {
			t.Errorf("the reason repeats a code in a list of things not touched: %q", row.Reason)
		}
	}
}

// TestASweepReportsWhatItRemovedBeforeItFailed. Deleting is the whole of what
// these commands do, so a failure is an error rather than a warning — and the
// entries already gone are still gone, which the caller has to be able to say.
func TestASweepReportsWhatItRemovedBeforeItFailed(t *testing.T) {
	t.Parallel()

	if _, err := cache.GC("", time.Now()); err == nil {
		t.Error("a sweep of no root at all succeeded")
	} else if code := cache.CodeOf(err); code != cache.CodeScanFailed {
		t.Errorf("code = %q, want %q (%v)", code, cache.CodeScanFailed, err)
	}
}

// TestGCOfANeverUsedRootIsNotAFailure, for the same reason status is not: a
// machine that has never run go-mutants has nothing to collect.
func TestGCOfANeverUsedRootIsNotAFailure(t *testing.T) {
	t.Parallel()

	sweep, err := cache.GC(filepath.Join(t.TempDir(), "never-used"), time.Now())
	if err != nil {
		t.Fatalf("GC of an empty root: %v", err)
	}
	if sweep.Entries != 0 {
		t.Errorf("an empty root reported %+v", sweep)
	}
}
