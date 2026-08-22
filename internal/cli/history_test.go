// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

// The fabricated history these tests read. The documents are written by hand
// rather than built, because what is under test is the reading: a listing must
// hold up against whatever is on disk, including a file that a run being killed
// half way through a write would have left.
const (
	historyModule = "example.com/history"
	otherModule   = "example.com/elsewhere"
	firstRun      = "20260218T091500Z-1111"
	secondRun     = "20260219T101500Z-2222"
	brokenRun     = "20260220T110000Z-3333"
)

// historyDigest and elsewhereDigest name two workspace directories. Two runs of
// one module land in different directories whenever the tree changed between
// them, so the listing has to gather them by module path.
var (
	historyDigest   = strings.Repeat("ab", 32)
	editedDigest    = strings.Repeat("a1", 32)
	elsewhereDigest = strings.Repeat("cd", 32)
	unmarkedDigest  = strings.Repeat("ef", 32)
)

// runDocument is a run report with the fields a listing reads, and nothing
// else. It is deliberately not a whole document: the enumeration decodes only
// what it prints, and a test that had to build a full report to prove that
// would not be proving it.
func runDocument(runID, module, finished string, score float64) string {
	return `{
  "document_type": "go-mutants/run-report",
  "schema_version": 1,
  "tool_version": "0.1.0-dev",
  "run_id": "` + runID + `",
  "status": "completed",
  "started_at": "` + finished + `",
  "finished_at": "` + finished + `",
  "workspace": { "module_path": "` + module + `" },
  "summary": {
    "total": 10, "killed": 7, "survived": 3, "timed_out": 0,
    "inconclusive": 0, "errored": 0, "not_run": 0,
    "score_percent": ` + strconv.FormatFloat(score, 'f', -1, 64) + `,
    "policy": { "strict": false, "minimum_score": 0, "require_mutants": true, "failure": null }
  }
}
`
}

// isolatedHistory points os.UserCacheDir at a temporary directory and puts the
// working directory in a module of its own, returning the store root.
//
// Both environment variables are set because os.UserCacheDir reads a different
// one on each platform, and a test that redirected only the POSIX spelling
// would operate on the developer's own history on Windows.
func isolatedHistory(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", base)
	t.Setenv("LocalAppData", base)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module "+historyModule+"\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	t.Chdir(dir)
	return filepath.Join(base, report.DirName)
}

// seedWorkspace claims a workspace directory and files documents in it, keyed
// by file name. A name of "latest.json" is written beside `runs/` rather than
// in it, which is where the store keeps the pointer to the newest run.
func seedWorkspace(t *testing.T, root, digest string, documents map[string]string) string {
	t.Helper()
	dir, err := report.History{Root: root}.Claim(digest)
	if err != nil {
		t.Fatalf("claiming the workspace directory: %v", err)
	}
	return writeDocuments(t, dir, documents)
}

// writeDocuments files documents under one workspace directory, creating
// `runs/` as needed.
func writeDocuments(t *testing.T, dir string, documents map[string]string) string {
	t.Helper()
	runs := filepath.Join(dir, report.RunsDirName)
	if err := os.MkdirAll(runs, 0o700); err != nil {
		t.Fatalf("creating the runs directory: %v", err)
	}
	for name, document := range documents {
		path := filepath.Join(runs, name)
		if name == report.LatestFileName {
			path = filepath.Join(dir, name)
		}
		if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	return dir
}

// seedHistory is the fabricated store every test below reads: two runs of this
// module in two workspace directories, one truncated document beside the
// newest, a pointer to the newest, and one run of a different module that must
// never be listed or deleted.
func seedHistory(t *testing.T, root string) {
	t.Helper()
	newest := runDocument(secondRun, historyModule, "2026-02-19T10:15:42Z", 91.5)
	seedWorkspace(t, root, historyDigest, map[string]string{
		firstRun + ".json":    runDocument(firstRun, historyModule, "2026-02-18T09:15:42Z", 70),
		report.LatestFileName: runDocument(firstRun, historyModule, "2026-02-18T09:15:42Z", 70),
	})
	seedWorkspace(t, root, editedDigest, map[string]string{
		secondRun + ".json":   newest,
		brokenRun + ".json":   `{"document_type": "go-mutants/run-report", "schema_ver`,
		report.LatestFileName: newest,
	})
	seedWorkspace(t, root, elsewhereDigest, map[string]string{
		"20260101T000000Z-9999.json": runDocument("20260101T000000Z-9999", otherModule, "2026-01-01T00:00:00Z", 12.5),
	})
}

// TestReportListGathersThisModulesRunsNewestFirst is the whole of `report
// list`: the two runs of this module, in order, from the two directories an
// edit between them filed them in — and nothing belonging to anybody else.
func TestReportListGathersThisModulesRunsNewestFirst(t *testing.T) {
	root := isolatedHistory(t)
	seedHistory(t, root)

	code, stdout, stderr := execute(t, "report", "list")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, historyModule) || !strings.Contains(stdout, root) {
		t.Errorf("the listing does not say whose history it is, or where:\n%s", stdout)
	}
	first := strings.Index(stdout, secondRun)
	second := strings.Index(stdout, firstRun)
	if first < 0 || second < 0 {
		t.Fatalf("both runs of this module are not listed:\n%s", stdout)
	}
	if first > second {
		t.Errorf("the newest run is not first:\n%s", stdout)
	}
	if strings.Contains(stdout, otherModule) || strings.Contains(stdout, "20260101T000000Z-9999") {
		t.Errorf("another module's run is in this module's history:\n%s", stdout)
	}
	if !strings.Contains(stdout, "91.5%") || !strings.Contains(stdout, "completed") {
		t.Errorf("the table does not carry the score and the status:\n%s", stdout)
	}
	if !strings.Contains(stdout, "2 runs in 2 workspace directories") {
		t.Errorf("the listing does not count what it found:\n%s", stdout)
	}
	// The truncated document is named rather than dropped, and it did not stop
	// the run beside it from being listed.
	if !strings.Contains(stdout, brokenRun) {
		t.Errorf("the damaged document is not reported:\n%s", stdout)
	}
	if !strings.Contains(stdout, "1 stored document go-mutants could not read") {
		t.Errorf("the damaged document is not counted:\n%s", stdout)
	}
}

// TestReportListAlignsItsColumns. The table is meant to be read down the page
// and diffed between two days, so every row's columns start where the header's
// do.
func TestReportListAlignsItsColumns(t *testing.T) {
	root := isolatedHistory(t)
	seedHistory(t, root)

	_, stdout, _ := execute(t, "report", "list")
	var header string
	var rows []string
	for _, line := range strings.Split(stdout, "\n") {
		switch {
		case strings.HasPrefix(line, "RUN "):
			header = line
		case strings.HasPrefix(line, "2026"):
			rows = append(rows, line)
		}
	}
	if header == "" || len(rows) != 2 {
		t.Fatalf("the table is not a header and two rows:\n%s", stdout)
	}
	want := strings.Index(header, "STATUS")
	for _, row := range rows {
		if got := strings.Index(row, "completed"); got != want {
			t.Errorf("the status column starts at %d and the header's at %d:\n%s", got, want, stdout)
		}
	}
}

// TestReportListOfAModuleWithNoRunsIsNotAFailure. An empty listing is a true
// answer to the question, and a non-zero status for it would break a script
// that checks the history before deciding to run.
func TestReportListOfAModuleWithNoRunsIsNotAFailure(t *testing.T) {
	root := isolatedHistory(t)
	seedWorkspace(t, root, elsewhereDigest, map[string]string{
		"20260101T000000Z-9999.json": runDocument("20260101T000000Z-9999", otherModule, "2026-01-01T00:00:00Z", 12.5),
	})

	code, stdout, stderr := execute(t, "report", "list")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "no run is recorded for this module yet") {
		t.Errorf("stdout = %q, want the empty answer", stdout)
	}
}

// TestHistoryCommandsNeedAModuleRoot. A run is filed under the module it
// measured, so outside one there is nothing to say which history is being
// asked about — and for `clean`, which deletes, guessing would be the worst
// possible answer.
func TestHistoryCommandsNeedAModuleRoot(t *testing.T) {
	for _, args := range [][]string{{"report", "list"}, {"report", "latest"}, {"report", "clean"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", base)
			t.Setenv("LocalAppData", base)
			t.Chdir(t.TempDir())

			code, _, stderr := execute(t, args...)
			if code != int(mutation.ExitInfrastructure) {
				t.Errorf("exit = %d, want 2", code)
			}
			if !strings.Contains(stderr, "error "+string(CodeNotAModuleRoot)) {
				t.Errorf("stderr = %q, want the module-root refusal", stderr)
			}
		})
	}
}

// TestReportLatestSummarisesTheNewestRunAndNamesItsFile.
func TestReportLatestSummarisesTheNewestRun(t *testing.T) {
	root := isolatedHistory(t)
	seedHistory(t, root)

	code, stdout, stderr := execute(t, "report", "latest")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if !strings.Contains(stdout, secondRun) {
		t.Errorf("the newest run is not the one summarised:\n%s", stdout)
	}
	if strings.Contains(stdout, firstRun) {
		t.Errorf("an older run is summarised too:\n%s", stdout)
	}
	for _, needle := range []string{"score 91.5%", "killed 7", "survived 3"} {
		if !strings.Contains(stdout, needle) {
			t.Errorf("the summary does not carry %q:\n%s", needle, stdout)
		}
	}
	path := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(editedDigest),
		report.RunsDirName, secondRun+".json")
	if !strings.Contains(stdout, path) {
		t.Errorf("the summary does not name the file it read:\n%s", stdout)
	}
}

// TestReportLatestJSONIsTheStoredDocument. The command prints an archive, and
// an archive re-encoded on its way out is not an archive: a document written by
// an earlier release must come back exactly as that release wrote it.
func TestReportLatestJSONIsTheStoredDocument(t *testing.T) {
	root := isolatedHistory(t)
	seedHistory(t, root)

	code, stdout, stderr := execute(t, "report", "latest", "--json")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	path := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(editedDigest),
		report.RunsDirName, secondRun+".json")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the stored document: %v", err)
	}
	if stdout != string(want) {
		t.Errorf("--json printed something other than the stored bytes:\n%s", stdout)
	}
}

// TestReportLatestWithNoHistoryIsAnError, unlike an empty listing: this
// command's whole output is one document, and there is none.
func TestReportLatestWithNoHistoryIsAnError(t *testing.T) {
	isolatedHistory(t)

	code, _, stderr := execute(t, "report", "latest")
	if code != int(mutation.ExitInfrastructure) {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "error "+string(CodeNoStoredRun)) {
		t.Errorf("stderr = %q, want the no-run code", stderr)
	}
	if !strings.Contains(stderr, "hint: ") {
		t.Errorf("stderr = %q, want a hint", stderr)
	}
}

// TestReportCleanRemovesThisModulesHistoryAndNothingElse.
func TestReportCleanRemovesThisModulesHistoryAndNothingElse(t *testing.T) {
	root := isolatedHistory(t)
	seedHistory(t, root)

	code, stdout, stderr := execute(t, "report", "clean")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s%s", code, stdout, stderr)
	}
	// Five documents: two runs, two pointers to the newest, and the truncated
	// file beside one of them, which is this module's leftover too.
	if !strings.Contains(stdout, "removed 5 stored documents") {
		t.Errorf("the sweep does not say what it removed:\n%s", stdout)
	}
	if !strings.Contains(stdout, "2 workspace directories") {
		t.Errorf("the sweep does not say where it removed them from:\n%s", stdout)
	}

	base := filepath.Join(root, report.WorkspacesDirName)
	for _, digest := range []string{historyDigest, editedDigest} {
		dir := filepath.Join(base, report.WorkspaceKey(digest))
		if _, err := os.Stat(filepath.Join(dir, report.RunsDirName)); !os.IsNotExist(err) {
			t.Errorf("%s still holds runs: %v", dir, err)
		}
		if _, err := os.Stat(filepath.Join(dir, report.LatestFileName)); !os.IsNotExist(err) {
			t.Errorf("%s still holds a pointer to the newest run: %v", dir, err)
		}
		// The marker stays: the directory's identity outlives its contents, and
		// a claim that vanished would let the next run adopt a directory
		// something else may still be writing to.
		if _, err := os.Stat(filepath.Join(dir, report.MarkerFileName)); err != nil {
			t.Errorf("the ownership marker in %s was deleted: %v", dir, err)
		}
	}
	elsewhere := filepath.Join(base, report.WorkspaceKey(elsewhereDigest), report.RunsDirName)
	if _, err := os.Stat(elsewhere); err != nil {
		t.Errorf("another module's history was deleted: %v", err)
	}

	// And the listing agrees with the sweep.
	if _, stdout, _ = execute(t, "report", "list"); !strings.Contains(stdout, "no run is recorded") {
		t.Errorf("runs survived the clean:\n%s", stdout)
	}
}

// TestReportCleanNamesWhatItCouldNotAttribute. "Nothing to remove" while
// history is still on the disk is the worst answer this command can give, so a
// directory whose documents cannot be read is named rather than passed over in
// silence — and left exactly as it was, since nothing in it can prove whose it
// is.
func TestReportCleanNamesWhatItCouldNotAttribute(t *testing.T) {
	root := isolatedHistory(t)
	seedWorkspace(t, root, historyDigest, map[string]string{
		brokenRun + ".json": `{"document_type": "go-mutants/run-report", "schema_ver`,
	})

	code, stdout, stderr := execute(t, "report", "clean")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "nothing to remove") {
		t.Errorf("an unattributable document was cleaned:\n%s", stdout)
	}
	if !strings.Contains(stdout, "could not read, so the directories holding them were left alone") {
		t.Errorf("the sweep does not say what it left behind:\n%s", stdout)
	}
	if !strings.Contains(stdout, brokenRun) {
		t.Errorf("the sweep does not name the file it left behind:\n%s", stdout)
	}
	survivor := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(historyDigest),
		report.RunsDirName, brokenRun+".json")
	if _, err := os.Stat(survivor); err != nil {
		t.Errorf("a document nothing could attribute was deleted: %v", err)
	}
}

// TestReportCleanRefusesADirectoryWithNoMarker is the safety property that
// makes deleting files in the operating system's cache directory acceptable at
// all: whatever the documents inside say, a directory that cannot prove it is
// go-mutants' own is reported and left alone.
func TestReportCleanRefusesADirectoryWithNoMarker(t *testing.T) {
	root := isolatedHistory(t)
	seedHistory(t, root)

	// A directory holding what looks exactly like this module's history, with
	// no marker on it.
	unmarked := filepath.Join(root, report.WorkspacesDirName, report.WorkspaceKey(unmarkedDigest))
	writeDocuments(t, unmarked, map[string]string{
		"20260222T120000Z-5555.json": runDocument("20260222T120000Z-5555", historyModule, "2026-02-22T12:00:00Z", 50),
	})

	code, stdout, stderr := execute(t, "report", "clean")
	if code != int(mutation.ExitOK) {
		t.Fatalf("exit = %d, want 0\n%s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "skipped 1 directory go-mutants will not touch") {
		t.Errorf("the unmarked directory is not reported:\n%s", stdout)
	}
	survivor := filepath.Join(unmarked, report.RunsDirName, "20260222T120000Z-5555.json")
	if _, err := os.Stat(survivor); err != nil {
		t.Errorf("a directory with no marker was cleaned: %v", err)
	}
	// It is not listed either: a document nothing can prove the provenance of
	// is not this module's history just because it says so.
	_, stdout, _ = execute(t, "report", "list")
	if strings.Contains(stdout, "20260222T120000Z-5555") {
		t.Errorf("an unmarked directory's run is listed as this module's:\n%s", stdout)
	}
}
