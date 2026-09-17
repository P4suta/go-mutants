// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
)

var errListing = errors.New("the directory could not be listed")

func TestListReportsADirectoryItCouldNotList(t *testing.T) {
	root := t.TempDir()
	storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	base := filepath.Join(root, report.WorkspacesDirName)

	restore := report.FailReadDir(base, errListing)
	listing, err := report.History{Root: root}.List()
	restore()

	if got := report.CodeOf(err); got != report.CodeHistoryDirectory {
		t.Fatalf("List = %v (code %q), want %s", err, got, report.CodeHistoryDirectory)
	}
	if want := "the history directory " + base + " could not be listed"; !strings.Contains(err.Error(), want) {
		t.Errorf("the failure does not say %q: %v", want, err)
	}
	if !errors.Is(err, errListing) {
		t.Error("the cause is not reachable through errors.Is")
	}
	if len(listing.Workspaces) != 0 || listing.Root != "" {
		t.Errorf("a half-read listing came back beside the failure: %+v", listing)
	}
}

func TestListReportsAWorkspaceItCouldNotRead(t *testing.T) {
	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	runs := filepath.Dir(runPath)

	restore := report.FailReadDir(runs, errListing)
	listing, err := report.History{Root: root}.List()
	restore()

	if got := report.CodeOf(err); got != report.CodeHistoryDirectory {
		t.Fatalf("List = %v (code %q), want %s", err, got, report.CodeHistoryDirectory)
	}
	if want := "the run history directory " + runs + " could not be listed"; !strings.Contains(err.Error(), want) {
		t.Errorf("the failure does not say %q: %v", want, err)
	}
	if len(listing.Workspaces) != 0 {
		t.Errorf("a half-read listing came back beside the failure: %+v", listing)
	}
}

func TestTheListingIsOrderedByThisPackageAndNotByTheFilesystem(t *testing.T) {
	root := t.TempDir()
	storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	storeRun(t, root, "20260218T091600Z-2222", otherDigest, moment(t, "2026-02-18T09:16:42Z"))
	base := filepath.Join(root, report.WorkspacesDirName)
	for _, name := range []string{"aaa-not-ours", "zzz-not-ours"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o755); err != nil {
			t.Fatalf("staging: %v", err)
		}
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("reading the store: %v", err)
	}
	reversed := make([]fs.DirEntry, len(entries))
	for i, entry := range entries {
		reversed[len(entries)-1-i] = entry
	}

	restore := report.StubReadDir(base, reversed)
	listing, err := report.History{Root: root}.List()
	restore()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	keys := make([]string, 0, len(listing.Workspaces))
	for _, workspace := range listing.Workspaces {
		keys = append(keys, workspace.Key)
	}
	if !sortedStrings(keys) {
		t.Errorf("the workspaces came back as %v, which is not in key order", keys)
	}
	names := make([]string, 0, len(listing.Skipped))
	for _, skipped := range listing.Skipped {
		names = append(names, skipped.Name)
	}
	if !sortedStrings(names) {
		t.Errorf("the skipped directories came back as %v, which is not in name order", names)
	}
	if len(keys) != 2 || len(names) != 2 {
		t.Fatalf("the listing holds %d workspaces and %d skipped directories, want 2 and 2", len(keys), len(names))
	}
}

func TestASkippedDirectoryIsGivenAReasonWithoutACode(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	base := filepath.Join(root, report.WorkspacesDirName)
	unmarked := filepath.Join(base, "somebody-elses")
	if err := os.MkdirAll(unmarked, 0o755); err != nil {
		t.Fatalf("staging: %v", err)
	}
	foreign := filepath.Join(base, "a-marker-we-cannot-read")
	writeFile(t, filepath.Join(foreign, report.MarkerFileName), "# managed by another tool\n")

	listing, err := report.History{Root: root}.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	reasons := map[string]string{}
	for _, skipped := range listing.Skipped {
		reasons[skipped.Name] = skipped.Reason
	}
	if want := "it carries no go-mutants workspace marker"; reasons["somebody-elses"] != want {
		t.Errorf("the unmarked directory's reason is %q, want %q", reasons["somebody-elses"], want)
	}
	got := reasons["a-marker-we-cannot-read"]
	if !strings.HasPrefix(got, "the marker ") || !strings.Contains(got, "is not one this build of go-mutants wrote") {
		t.Errorf("the foreign marker's reason is %q, want the refusal without its code in front", got)
	}
	if strings.Contains(got, "GOM") {
		t.Errorf("the reason still carries its diagnostic code: %q", got)
	}
}

func TestAReasonWithNoCodeInFrontOfItIsLeftAlone(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"a coded refusal": {
			err:  errors.New("GOM5133: the marker is not one this build wrote"),
			want: "the marker is not one this build wrote",
		},
		"a sentence with a colon and no code": {
			err:  errors.New("the marker: not one this build wrote"),
			want: "the marker: not one this build wrote",
		},
		"a sentence with no colon at all": {
			err:  errors.New("the marker is not ours"),
			want: "the marker is not ours",
		},
		"a code and several colons": {
			err:  errors.New("GOM5131: the marker /a/b: could not be read: is a directory"),
			want: "the marker /a/b: could not be read: is a directory",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := report.ReasonOf(tc.err); got != tc.want {
				t.Errorf("ReasonOf = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestADamagedDocumentIsARowSayingWhatIsWrongWithIt(t *testing.T) {
	t.Parallel()

	const header = `"document_type":"go-mutants/run-report","schema_version":1`
	for name, tc := range map[string]struct {
		content string
		says    string
	}{
		"not JSON at all": {
			content: "half a document",
			says:    "it is not a run report this build can read: ",
		},
		"another tool's document": {
			content: `{"document_type":"go-mutants/catalog","schema_version":1}`,
			says: `it is "go-mutants/catalog", not a go-mutants/run-report or a ` +
				`go-mutants/workspace-report document`,
		},
		"a version this build does not read": {
			content: `{"document_type":"go-mutants/run-report","schema_version":7}`,
			says:    "it is schema version 7, and this build reads version 1",
		},
		"a run id that is not one": {
			content: `{` + header + `,"run_id":"nightly"}`,
			says:    `its run id "nightly" is not a run id`,
		},
		"a status a run cannot end in": {
			content: `{` + header + `,"run_id":"20260218T091500Z-3f9c","status":"aborted"}`,
			says:    `its status "aborted" is not one a run can end in`,
		},
		"a finish time that is not a timestamp": {
			content: `{` + header + `,"run_id":"20260218T091500Z-3f9c","status":"completed","finished_at":"last tuesday"}`,
			says:    `its finish time "last tuesday" is not an RFC 3339 timestamp`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
			damaged := filepath.Join(filepath.Dir(runPath), "20260218T091600Z-2222.json")
			writeFile(t, damaged, tc.content)

			listing, err := report.History{Root: root}.List()
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			workspace := onlyWorkspace(t, listing)
			if len(workspace.Runs) != 1 {
				t.Errorf("the good run was lost: %d runs listed", len(workspace.Runs))
			}
			if len(workspace.Damaged) != 1 {
				t.Fatalf("the listing holds %d damaged rows, want 1: %+v", len(workspace.Damaged), workspace.Damaged)
			}
			if workspace.Damaged[0].Path != damaged {
				t.Errorf("the damaged row names %s, want %s", workspace.Damaged[0].Path, damaged)
			}
			if !strings.Contains(workspace.Damaged[0].Reason, tc.says) {
				t.Errorf("the damaged row's reason is %q, want it to say %q", workspace.Damaged[0].Reason, tc.says)
			}
		})
	}
}

func TestAPointerThatIsNotADocumentIsADamagedRowToo(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	dir := filepath.Dir(filepath.Dir(runPath))
	latest := filepath.Join(dir, report.LatestFileName)
	if err := os.Remove(latest); err != nil {
		t.Fatalf("removing the pointer: %v", err)
	}
	makeDir(t, latest, "in the way")
	damaged := filepath.Join(filepath.Dir(runPath), "20260218T091600Z-2222.json")
	writeFile(t, damaged, "half a document")

	listing, err := report.History{Root: root}.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	workspace := onlyWorkspace(t, listing)
	if len(workspace.Damaged) != 2 {
		t.Fatalf("the listing holds %d damaged rows, want 2: %+v", len(workspace.Damaged), workspace.Damaged)
	}
	if workspace.Damaged[0].Path != latest || workspace.Damaged[1].Path != damaged {
		t.Errorf("the damaged rows are\n %s\n %s\nwant them ordered by path:\n %s\n %s",
			workspace.Damaged[0].Path, workspace.Damaged[1].Path, latest, damaged)
	}
	if !strings.HasPrefix(workspace.Damaged[0].Reason, "it could not be read: ") {
		t.Errorf("the pointer's row is %q, want it to say the file could not be read", workspace.Damaged[0].Reason)
	}
	if workspace.Latest != "" {
		t.Errorf("a run id was taken from a pointer that could not be read: %q", workspace.Latest)
	}
}

func TestAPointerToARunNoLongerFiledStillNamesIt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	newest := storeRun(t, root, "20260218T091600Z-2222", fixtureDigest, moment(t, "2026-02-18T09:16:42Z"))
	if err := os.Remove(newest); err != nil {
		t.Fatalf("removing the newest run from runs/: %v", err)
	}

	listing, err := report.History{Root: root}.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	workspace := onlyWorkspace(t, listing)
	ids := make([]string, 0, len(workspace.Runs))
	for _, run := range workspace.Runs {
		ids = append(ids, run.RunID)
	}
	if len(ids) != 2 {
		t.Fatalf("the listing holds %v, want both runs — the pointer holds the newest in full", ids)
	}
	if ids[0] != "20260218T091600Z-2222" {
		t.Errorf("the listing is %v, want the newest first", ids)
	}
	if workspace.Latest != "20260218T091600Z-2222" {
		t.Errorf("latest = %q, want the run the pointer holds", workspace.Latest)
	}
	if workspace.Runs[0].Path != filepath.Join(workspace.Dir, report.LatestFileName) {
		t.Errorf("the run recovered from the pointer is at %s, want the pointer's own path", workspace.Runs[0].Path)
	}
}

func TestNewestFirstAnswersWithASign(t *testing.T) {
	t.Parallel()

	older := report.StoredRun{RunID: "20260218T091500Z-1111", Path: "/a", FinishedAt: moment(t, "2026-02-18T09:15:42Z")}
	newer := report.StoredRun{RunID: "20260218T091600Z-2222", Path: "/b", FinishedAt: moment(t, "2026-02-18T09:16:42Z")}
	if got := report.NewestFirst(newer, older); got >= 0 {
		t.Errorf("NewestFirst(newer, older) = %d, want a negative number", got)
	}
	if got := report.NewestFirst(older, newer); got <= 0 {
		t.Errorf("NewestFirst(older, newer) = %d, want a positive number", got)
	}

	early := report.StoredRun{RunID: "20260218T091500Z-1111", Path: "/a", FinishedAt: moment(t, "2026-02-18T09:15:42Z")}
	late := report.StoredRun{RunID: "20260218T091500Z-9999", Path: "/b", FinishedAt: moment(t, "2026-02-18T09:15:42Z")}
	if got := report.NewestFirst(late, early); got >= 0 {
		t.Errorf("NewestFirst by id = %d, want a negative number", got)
	}
	if got := report.NewestFirst(early, late); got <= 0 {
		t.Errorf("NewestFirst by id = %d, want a positive number", got)
	}
	if got := report.NewestFirst(early, early); got != 0 {
		t.Errorf("NewestFirst of one run with itself = %d, want 0", got)
	}
}

func TestOnlyRunDocumentsAreListedOrDeleted(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	runs := filepath.Dir(runPath)
	writeFile(t, filepath.Join(runs, "go-mutants-report-4242.tmp"), "an interrupted write")
	makeDir(t, filepath.Join(runs, "a-directory.json"), "inner")

	listing, err := report.History{Root: root}.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	workspace := onlyWorkspace(t, listing)
	if len(workspace.Damaged) != 0 {
		t.Errorf("the listing calls something damaged that is not a document: %+v", workspace.Damaged)
	}
	if len(workspace.Runs) != 1 {
		t.Errorf("the listing holds %d runs, want 1", len(workspace.Runs))
	}

	removed, err := report.History{Root: root}.RemoveRuns(fixtureDigest)
	if err != nil {
		t.Fatalf("RemoveRuns: %v", err)
	}
	if removed.Runs != 2 {
		t.Errorf("RemoveRuns counted %d documents, want 2", removed.Runs)
	}
}

func TestAFileThatWentAwayBetweenTheListingAndTheStat(t *testing.T) {
	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	runs := filepath.Dir(runPath)
	entries, err := os.ReadDir(runs)
	if err != nil {
		t.Fatalf("reading the run directory: %v", err)
	}

	t.Run("a file that is gone is not there to list", func(t *testing.T) {
		vanished := append(append([]fs.DirEntry{}, entries...),
			report.VanishedEntry("20260218T091600Z-2222.json", fs.ErrNotExist))

		restore := report.StubReadDir(runs, vanished)
		listing, listErr := report.History{Root: root}.List()
		restore()

		if listErr != nil {
			t.Fatalf("List over a file that went away: %v", listErr)
		}
		workspace := onlyWorkspace(t, listing)
		if len(workspace.Runs) != 1 || len(workspace.Damaged) != 0 {
			t.Errorf("the listing holds %d runs and %d damaged rows, want 1 and 0",
				len(workspace.Runs), len(workspace.Damaged))
		}
	})

	t.Run("a file that cannot be measured is a failure", func(t *testing.T) {
		refused := errors.New("the file could not be measured")
		unmeasurable := append(append([]fs.DirEntry{}, entries...),
			report.VanishedEntry("20260218T091600Z-2222.json", refused))

		restore := report.StubReadDir(runs, unmeasurable)
		_, listErr := report.History{Root: root}.List()
		restore()

		if got := report.CodeOf(listErr); got != report.CodeHistoryDirectory {
			t.Fatalf("List = %v (code %q), want %s", listErr, got, report.CodeHistoryDirectory)
		}
		want := "the stored run " + filepath.Join(runs, "20260218T091600Z-2222.json") + " could not be measured"
		if !strings.Contains(listErr.Error(), want) {
			t.Errorf("the failure does not say %q: %v", want, listErr)
		}
		if !errors.Is(listErr, refused) {
			t.Error("the cause is not reachable through errors.Is")
		}
	})
}

func TestRemoveRunsReportsAMarkerItCouldNotRead(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(fixtureDigest))
	marker := filepath.Join(dir, report.MarkerFileName)
	makeDir(t, marker, "in the way")
	writeFile(t, filepath.Join(dir, report.RunsDirName, "20260218T091500Z-1111.json"), "{}")

	removed, err := report.History{Root: root}.RemoveRuns(fixtureDigest)
	if got := report.CodeOf(err); got != report.CodeHistoryDirectory {
		t.Fatalf("RemoveRuns = %v (code %q), want %s -- an unreadable marker is not a foreign one",
			err, got, report.CodeHistoryDirectory)
	}
	if want := "the workspace marker " + marker + " could not be read"; !strings.Contains(err.Error(), want) {
		t.Errorf("the failure does not say %q: %v", want, err)
	}
	if removed.Runs != 0 {
		t.Errorf("RemoveRuns reported %d documents removed from a directory it refused", removed.Runs)
	}
	exists(t, filepath.Join(dir, report.RunsDirName, "20260218T091500Z-1111.json"), true)
}

func TestRemoveRunsRefusesAMarkerThatNamesAnotherWorkspace(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(fixtureDigest))
	writeFile(t, filepath.Join(dir, report.MarkerFileName), "go-mutants-workspace-v1\n"+otherDigest+"\n")
	writeFile(t, filepath.Join(dir, report.RunsDirName, "20260218T091500Z-1111.json"), "{}")

	removed, err := report.History{Root: root}.RemoveRuns(fixtureDigest)
	if got := report.CodeOf(err); got != report.CodeForeignWorkspace {
		t.Fatalf("RemoveRuns = %v (code %q), want %s", err, got, report.CodeForeignWorkspace)
	}
	if want := "the marker in " + dir + " names another workspace, so nothing in it was deleted"; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not say %q: %v", want, err)
	}
	if removed.Runs != 0 {
		t.Errorf("RemoveRuns reported %d documents removed from a directory it refused", removed.Runs)
	}
	exists(t, filepath.Join(dir, report.RunsDirName, "20260218T091500Z-1111.json"), true)
}

func TestRemoveRunsReportsARunDirectoryItCouldNotList(t *testing.T) {
	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	runs := filepath.Dir(runPath)

	restore := report.FailReadDir(runs, errListing)
	removed, err := report.History{Root: root}.RemoveRuns(fixtureDigest)
	restore()

	if got := report.CodeOf(err); got != report.CodeHistoryDirectory {
		t.Fatalf("RemoveRuns = %v (code %q), want %s", err, got, report.CodeHistoryDirectory)
	}
	if want := "the run history directory " + runs + " could not be listed"; !strings.Contains(err.Error(), want) {
		t.Errorf("the failure does not say %q: %v", want, err)
	}
	if removed.Dir == "" {
		t.Error("RemoveRuns did not say which directory it was working on")
	}
	exists(t, runPath, true)
}

func TestRemoveRunsRefusesAStoreItCannotProveThingsAreInside(t *testing.T) {
	t.Chdir(t.TempDir())

	const relative = "store"
	runPath := storeRun(t, relative, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))

	_, err := report.History{Root: relative}.RemoveRuns(fixtureDigest)
	if got := report.CodeOf(err); got != report.CodeHistoryNotRemoved {
		t.Fatalf("RemoveRuns = %v (code %q), want %s", err, got, report.CodeHistoryNotRemoved)
	}
	runs := filepath.Dir(runPath)
	if want := runs + " is not inside the history store at " + relative + ", so it was not deleted"; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not say %q: %v", want, err)
	}
	exists(t, runPath, true)
}

func TestRemoveRunsReportsAPointerItCouldNotMeasure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	dir := filepath.Dir(filepath.Dir(runPath))
	latest := filepath.Join(dir, report.LatestFileName)
	if err := os.Remove(latest); err != nil {
		t.Fatalf("removing the pointer: %v", err)
	}
	if err := os.Symlink(report.LatestFileName, latest); err != nil {
		t.Skipf("this platform will not let the test create a symbolic link: %v", err)
	}

	removed, err := report.History{Root: root}.RemoveRuns(fixtureDigest)
	if got := report.CodeOf(err); got != report.CodeHistoryNotRemoved {
		t.Fatalf("RemoveRuns = %v (code %q), want %s", err, got, report.CodeHistoryNotRemoved)
	}
	if want := latest + " could not be measured, so it was not deleted"; !strings.Contains(err.Error(), want) {
		t.Errorf("the failure does not say %q: %v", want, err)
	}
	if removed.Runs != 1 {
		t.Errorf("RemoveRuns reported %d documents removed, want the one it did remove", removed.Runs)
	}
}

func TestRemoveRunsReportsWhatItCouldNotDelete(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	dir := filepath.Dir(filepath.Dir(runPath))
	if err := os.RemoveAll(filepath.Join(dir, report.RunsDirName)); err != nil {
		t.Fatalf("staging: %v", err)
	}
	denyWrites(t, dir)

	removed, err := report.History{Root: root}.RemoveRuns(fixtureDigest)
	if got := report.CodeOf(err); got != report.CodeHistoryNotRemoved {
		t.Fatalf("RemoveRuns = %v (code %q), want %s", err, got, report.CodeHistoryNotRemoved)
	}
	latest := filepath.Join(dir, report.LatestFileName)
	if want := latest + " could not be deleted"; !strings.Contains(err.Error(), want) {
		t.Errorf("the failure does not say %q: %v", want, err)
	}
	if removed.Dir != dir {
		t.Errorf("RemoveRuns says it worked on %s, want %s", removed.Dir, dir)
	}
	exists(t, latest, true)
}

func TestWithinIsAboutWhereAPathResolvesRatherThanHowItIsSpelled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	inside := filepath.Join(root, "workspaces", "abcd")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("staging: %v", err)
	}
	file := filepath.Join(root, "afile")
	writeFile(t, file, "not a directory")

	for name, tc := range map[string]struct {
		path    string
		root    string
		want    bool
		wantErr report.Code
	}{
		"a path under the root":           {path: filepath.Join(inside, "runs"), root: root, want: true},
		"the root itself":                 {path: root, root: root},
		"the parent of the root":          {path: filepath.Dir(root), root: root},
		"a sibling of the root":           {path: filepath.Join(filepath.Dir(root), "elsewhere"), root: root},
		"a path that is not there at all": {path: filepath.Join(inside, "never-created", "runs"), root: root, want: true},
		"a path whose parent is a file":   {path: filepath.Join(file, "deeper", "runs"), root: root, wantErr: report.CodeHistoryNotRemoved},
		"a root that is not a directory":  {path: filepath.Join(inside, "runs"), root: filepath.Join(file, "store"), wantErr: report.CodeHistoryNotRemoved},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if tc.wantErr != "" {
				requireRefusalUnderAFile(t, file)
			}
			got, err := report.Within(tc.path, tc.root)
			if tc.wantErr != "" {
				if code := report.CodeOf(err); code != tc.wantErr {
					t.Fatalf("Within = (%v, %v) (code %q), want %s", got, err, code, tc.wantErr)
				}
				if got {
					t.Error("a path that could not be resolved was reported as inside the store")
				}
				if !strings.Contains(err.Error(), "so it was not deleted") &&
					!strings.Contains(err.Error(), "so nothing under it was deleted") {
					t.Errorf("the refusal does not say what it did about it: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Within: %v", err)
			}
			if got != tc.want {
				t.Errorf("Within = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWithinFollowsALinkOutOfTheStore(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	dir := filepath.Join(root, "workspaces", "abcd")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatalf("staging: %v", err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Skipf("this platform will not let the test create a symbolic link: %v", err)
	}

	inside, err := report.Within(filepath.Join(dir, report.RunsDirName), root)
	if err != nil {
		t.Fatalf("Within: %v", err)
	}
	if inside {
		t.Error("a path that resolves outside the store was reported as inside it")
	}

	linked := filepath.Join(root, "linked-leaf")
	if err = os.Symlink(outside, linked); err != nil {
		t.Skipf("this platform will not let the test create a symbolic link: %v", err)
	}
	if inside, err = report.Within(linked, root); err != nil || !inside {
		t.Errorf("Within(a linked leaf) = (%v, %v), want (true, nil)", inside, err)
	}
}

func TestRemoveInsideRefusesToDeleteWhatItCannotPlace(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "afile"), "not a directory")
	victim := filepath.Join(t.TempDir(), "elsewhere")
	writeFile(t, victim, "somebody else's file")

	t.Run("a path outside the store", func(t *testing.T) {
		t.Parallel()
		err := report.RemoveInside(victim, root)
		if got := report.CodeOf(err); got != report.CodeHistoryNotRemoved {
			t.Fatalf("RemoveInside = %v (code %q), want %s", err, got, report.CodeHistoryNotRemoved)
		}
		if want := victim + " is not inside the history store at " + root; !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
		exists(t, victim, true)
	})

	t.Run("a path that cannot be resolved", func(t *testing.T) {
		t.Parallel()
		requireRefusalUnderAFile(t, filepath.Join(root, "afile"))
		err := report.RemoveInside(filepath.Join(root, "afile", "deeper", "runs"), root)
		if got := report.CodeOf(err); got != report.CodeHistoryNotRemoved {
			t.Fatalf("RemoveInside = %v (code %q), want %s", err, got, report.CodeHistoryNotRemoved)
		}
		if !strings.Contains(err.Error(), "could not be resolved on disk, so it was not deleted") {
			t.Errorf("the refusal does not say what could not be resolved: %v", err)
		}
	})

	t.Run("a path that is already gone", func(t *testing.T) {
		t.Parallel()
		if err := report.RemoveInside(filepath.Join(root, "never-created"), root); err != nil {
			t.Errorf("RemoveInside of a path that is not there = %v, want nil — it is already gone", err)
		}
	})
}

func TestResolvePathAnswersForNamesThatAreNotAllThere(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("staging: %v", err)
	}
	writeFile(t, filepath.Join(root, "afile"), "not a directory")
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolving the fixture root: %v", err)
	}

	t.Run("a name that is there", func(t *testing.T) {
		t.Parallel()
		got, resolveErr := report.ResolvePath(real)
		if resolveErr != nil {
			t.Fatalf("ResolvePath: %v", resolveErr)
		}
		if want := filepath.Join(resolvedRoot, "real"); got != want {
			t.Errorf("ResolvePath = %q, want %q", got, want)
		}
	})

	t.Run("names that are not there", func(t *testing.T) {
		t.Parallel()
		got, resolveErr := report.ResolvePath(filepath.Join(real, "never", "created", "runs"))
		if resolveErr != nil {
			t.Fatalf("ResolvePath: %v", resolveErr)
		}
		if want := filepath.Join(resolvedRoot, "real", "never", "created", "runs"); got != want {
			t.Errorf("ResolvePath = %q, want %q — the part that resolves, then the rest verbatim", got, want)
		}
	})

	t.Run("names that are not there under a link this test made", func(t *testing.T) {
		t.Parallel()
		link := filepath.Join(root, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("this filesystem does not make symlinks: %v", err)
		}
		got, resolveErr := report.ResolvePath(filepath.Join(link, "never", "created", "runs"))
		if resolveErr != nil {
			t.Fatalf("ResolvePath: %v", resolveErr)
		}
		want := filepath.Join(resolvedRoot, "real", "never", "created", "runs")
		if got != want {
			t.Errorf("ResolvePath = %q, want %q — the link resolved, then the names that are not there",
				got, want)
		}
	})

	t.Run("a name under something that is not a directory", func(t *testing.T) {
		t.Parallel()
		requireRefusalUnderAFile(t, filepath.Join(root, "afile"))
		got, resolveErr := report.ResolvePath(filepath.Join(root, "afile", "runs"))
		if resolveErr == nil {
			t.Fatalf("ResolvePath of a path under a file = %q, want a failure", got)
		}
		if got != "" {
			t.Errorf("a path came back beside the failure: %q", got)
		}
	})
}

func TestResolveParentLeavesTheLastElementAlone(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("staging: %v", err)
	}
	writeFile(t, filepath.Join(root, "afile"), "not a directory")
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolving the fixture root: %v", err)
	}

	got, err := report.ResolveParent(filepath.Join(real, "runs"))
	if err != nil {
		t.Fatalf("ResolveParent: %v", err)
	}
	if want := filepath.Join(resolvedRoot, "real", "runs"); got != want {
		t.Errorf("ResolveParent = %q, want %q", got, want)
	}

	requireRefusalUnderAFile(t, filepath.Join(root, "afile"))
	if got, err = report.ResolveParent(filepath.Join(root, "afile", "deeper", "runs")); err == nil {
		t.Errorf("ResolveParent under a file = %q, want a failure", got)
	}
}

func requireRefusalUnderAFile(t *testing.T, file string) {
	t.Helper()
	_, err := os.Stat(filepath.Join(file, "deeper", "runs"))
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Skip("this platform reports a path under a regular file as merely absent, so the refusal this case stages cannot happen here")
	}
}

func TestTrimExtendedPrefixDropsWhatWindowsAdds(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		path string
		want string
	}{
		"a long local path":      {path: `\\?\C:\a\b`, want: `C:\a\b`},
		"a long UNC path":        {path: `\\?\UNC\server\share\a`, want: `\\server\share\a`},
		"an ordinary path":       {path: `C:\a\b`, want: `C:\a\b`},
		"a plain UNC path":       {path: `\\server\share\a`, want: `\\server\share\a`},
		"a Unix path":            {path: "/tmp/store/runs", want: "/tmp/store/runs"},
		"a relative path":        {path: "store/runs", want: "store/runs"},
		"the prefix and no more": {path: `\\?\`, want: ``},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := report.TrimExtendedPrefix(tc.path); got != tc.want {
				t.Errorf("TrimExtendedPrefix(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func onlyWorkspace(t *testing.T, listing report.Listing) report.StoredWorkspace {
	t.Helper()
	if len(listing.Workspaces) != 1 {
		t.Fatalf("the listing holds %d workspaces, want 1: %+v", len(listing.Workspaces), listing)
	}
	return listing.Workspaces[0]
}

func sortedStrings(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i-1] > values[i] {
			return false
		}
	}
	return true
}

func denyWrites(t *testing.T, dir string) {
	t.Helper()
	before, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("reading the mode of %s: %v", dir, err)
	}
	if err = os.Chmod(dir, 0o555); err != nil {
		t.Skipf("this platform will not make %s read-only: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, before.Mode().Perm()) })

	probe := filepath.Join(dir, "probe")
	if file, probeErr := os.Create(probe); probeErr == nil {
		_ = file.Close()
		_ = os.Remove(probe)
		t.Skip("a directory that refuses writes does not refuse this process, so the failure this test stages cannot happen here")
	}
}

func TestListReportsAPointerItCouldNotMeasure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runPath := storeRun(t, root, "20260218T091500Z-1111", fixtureDigest, moment(t, "2026-02-18T09:15:42Z"))
	dir := filepath.Dir(filepath.Dir(runPath))
	latest := filepath.Join(dir, report.LatestFileName)
	if err := os.Remove(latest); err != nil {
		t.Fatalf("removing the pointer: %v", err)
	}
	if err := os.Symlink(report.LatestFileName, latest); err != nil {
		t.Skipf("this platform will not let the test create a symbolic link: %v", err)
	}

	listing, err := report.History{Root: root}.List()
	if got := report.CodeOf(err); got != report.CodeHistoryDirectory {
		t.Fatalf("List = %v (code %q), want %s", err, got, report.CodeHistoryDirectory)
	}
	if want := "the pointer to the newest run, " + latest + ", could not be read"; !strings.Contains(err.Error(), want) {
		t.Errorf("the failure does not say %q: %v", want, err)
	}
	if len(listing.Workspaces) != 0 {
		t.Errorf("a half-read listing came back beside the failure: %+v", listing)
	}
}
