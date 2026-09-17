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

func age(t *testing.T, path string, d time.Duration) {
	t.Helper()
	when := time.Now().Add(-d)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("backdating %s: %v", path, err)
	}
}

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

func TestGCRemovesOnlyWhatIsOldEnough(t *testing.T) {
	t.Parallel()

	root, dir := populated(t)
	age(t, filepath.Join(dir, mutantIDs[0]+".json"), 40*24*time.Hour)
	age(t, filepath.Join(dir, mutantIDs[1]+".json"), 31*24*time.Hour)

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
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("the context directory was removed with a live entry in it: %v", statErr)
	}
}

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
	if _, statErr := os.Stat(filepath.Dir(dir)); statErr != nil {
		t.Errorf("gc removed the outcomes directory itself: %v", statErr)
	}
}

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

func TestGCRefusesEntriesThatLeaveTheCache(t *testing.T) {
	t.Parallel()

	root, dir := populated(t)
	outcomes := filepath.Dir(dir)

	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(outcomes, outside); err != nil {
		t.Fatalf("moving the outcomes out of the cache: %v", err)
	}
	if err := os.Symlink(outside, outcomes); err != nil {
		t.Skipf("this platform will not let the test create a symbolic link: %v", err)
	}
	victims := make([]string, 0, len(mutantIDs))
	for _, id := range mutantIDs {
		victim := filepath.Join(outside, filepath.Base(dir), id+".json")
		age(t, victim, 90*24*time.Hour)
		victims = append(victims, victim)
	}

	_, err := cache.GC(root, time.Now().AddDate(0, 0, -cache.DefaultGCDays))
	if err == nil {
		t.Fatal("a sweep followed a link out of the cache and reported success")
	}
	if got := cache.CodeOf(err); got != cache.CodeNotRemoved {
		t.Errorf("code = %q, want %q (%v)", got, cache.CodeNotRemoved, err)
	}
	for _, victim := range victims {
		if _, statErr := os.Stat(victim); statErr != nil {
			t.Errorf("a file outside the cache was deleted through the link: %v", statErr)
		}
	}
}

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
	again, err := cache.Clean(root)
	if err != nil {
		t.Fatalf("Clean of an already clean cache: %v", err)
	}
	if again.Entries != 0 || again.Workspaces != 0 {
		t.Errorf("the second clean removed %+v", again)
	}
}

func TestADirectoryWithoutOurMarkerIsRefused(t *testing.T) {
	t.Parallel()

	root, _ := populated(t)
	base := filepath.Join(root, report.WorkspacesDirName)

	stranger := filepath.Join(base, "somebody-elses-tool")
	write(t, filepath.Join(stranger, cache.OutcomesDirName, "ctx", "a.json"), "{}")
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

const copiedKey = "0123456789abcdef" //gitleaks:allow

func withACopiedWorkspace(t *testing.T) (root, entry string) {
	t.Helper()
	root, dir := populated(t)
	original := filepath.Dir(filepath.Dir(dir))
	copied := filepath.Join(filepath.Dir(original), copiedKey)
	copyTree(t, original, copied)
	return root, filepath.Join(copied, cache.OutcomesDirName, filepath.Base(dir), mutantIDs[0]+".json")
}

func checkTheCopyWasSkipped(t *testing.T, skipped []cache.Skipped) {
	t.Helper()
	if len(skipped) != 1 {
		t.Fatalf("%d directories were skipped, want the copy: %+v", len(skipped), skipped)
	}
	if skipped[0].Name != copiedKey {
		t.Errorf("the skipped directory is %q, want the copy %q", skipped[0].Name, copiedKey)
	}
	if key := report.WorkspaceKey(baseContext().WorkspaceDigest); !strings.Contains(skipped[0].Reason, key) {
		t.Errorf("the reason does not name the directory the marker belongs to: %q", skipped[0].Reason)
	}
	if strings.HasPrefix(skipped[0].Reason, "GOM") {
		t.Errorf("the skipped row repeats a code: %q", skipped[0].Reason)
	}
}

func TestASweepSkipsAWorkspaceDirectoryThatIsACopy(t *testing.T) {
	t.Parallel()

	root, entry := withACopiedWorkspace(t)
	survey, err := cache.Status(root)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(survey.Workspaces) != 1 {
		t.Fatalf("Status found %d workspaces, want only the one this cache named: %+v",
			len(survey.Workspaces), survey.Workspaces)
	}
	if got, want := survey.Workspaces[0].Key, report.WorkspaceKey(baseContext().WorkspaceDigest); got != want {
		t.Errorf("the surveyed workspace is %q, want the key its digest names %q", got, want)
	}
	if got, want := survey.Entries(), len(mutantIDs); got != want {
		t.Errorf("the survey counted %d entries, want the %d the one workspace holds", got, want)
	}
	checkTheCopyWasSkipped(t, survey.Skipped)
	if _, err = os.Stat(entry); err != nil {
		t.Errorf("a survey that changes nothing lost an entry: %v", err)
	}

	for _, sweep := range []struct {
		name string
		run  func(root string) (cache.Sweep, error)
	}{
		{"gc", func(root string) (cache.Sweep, error) { return cache.GC(root, time.Now().Add(time.Hour)) }},
		{"clean", cache.Clean},
	} {
		t.Run(sweep.name, func(t *testing.T) {
			t.Parallel()

			root, entry := withACopiedWorkspace(t)
			result, err := sweep.run(root)
			if err != nil {
				t.Fatalf("%s: %v", sweep.name, err)
			}
			if result.Workspaces != 1 {
				t.Errorf("the sweep touched %d workspaces, want only the one this cache named", result.Workspaces)
			}
			if got, want := result.Entries, len(mutantIDs); got != want {
				t.Errorf("the sweep removed %d entries, want the %d the one workspace holds", got, want)
			}
			checkTheCopyWasSkipped(t, result.Skipped)
			if _, err := os.Stat(entry); err != nil {
				t.Errorf("a directory the walk would not touch was swept anyway: %v", err)
			}
		})
	}
}

func TestASweepReportsWhatItRemovedBeforeItFailed(t *testing.T) {
	t.Parallel()

	if _, err := cache.GC("", time.Now()); err == nil {
		t.Error("a sweep of no root at all succeeded")
	} else if code := cache.CodeOf(err); code != cache.CodeScanFailed {
		t.Errorf("code = %q, want %q (%v)", code, cache.CodeScanFailed, err)
	}
}

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
