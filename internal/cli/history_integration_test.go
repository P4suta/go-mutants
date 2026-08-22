// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The run-history commands against a history a real run wrote.
//
// The unit tests read a store this file's fabricated documents were placed in,
// which is how the damaged, the foreign and the empty cases are pinned. What
// they cannot prove is that the store a run actually writes is one these
// commands can read: the layout, the run id, the module path, and the clock all
// have to line up, and every one of them is decided in another package.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/cli/...
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
)

// measuredFixture copies the killable fixture into a temporary directory,
// points everything the run writes at temporary directories too, and returns
// the workspace root and the history store the run will file itself in.
func measuredFixture(t *testing.T) (root, store string) {
	t.Helper()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "fixtures", "killable"))
	if err != nil {
		t.Fatalf("resolving the fixture path: %v", err)
	}

	temp := t.TempDir()
	// os.TempDir reads TMPDIR on POSIX and TMP then TEMP on Windows, so all
	// three are set rather than guessing which platform is reading; the cache
	// pair is what os.UserCacheDir reads, and redirecting it is what keeps this
	// test out of the developer's own history.
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	cache := filepath.Join(temp, "cache")
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("LocalAppData", cache)

	root = filepath.Join(t.TempDir(), "killable")
	if err = os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating the workspace: %v", err)
	}
	if err = os.CopyFS(root, os.DirFS(fixture)); err != nil {
		t.Fatalf("copying the killable fixture: %v", err)
	}
	t.Chdir(root)
	return root, filepath.Join(cache, report.DirName)
}

// TestHistoryCommandsReadWhatARunWrote walks the three of them over one real
// run: an empty history before it, the run's own document after it, and an
// empty history again once it has been cleaned.
func TestHistoryCommandsReadWhatARunWrote(t *testing.T) {
	_, store := measuredFixture(t)

	// Before anything has run, the listing is empty and says so, and `latest`
	// is the one of the three that refuses.
	code, stdout, stderr := execute(t, "report", "list")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`report list` on a fresh machine exited %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "no run is recorded") {
		t.Errorf("a fresh machine has history:\n%s", stdout)
	}
	if code, _, _ = execute(t, "report", "latest"); code != int(mutation.ExitInfrastructure) {
		t.Errorf("`report latest` with no history exited %d, want 2", code)
	}

	code, stdout, stderr = execute(t, "run", "--no-tui", "--no-color", "--quiet")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`go-mutants run` exited %d\n%s%s", code, stdout, stderr)
	}

	code, stdout, stderr = execute(t, "report", "list")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`report list` exited %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, store) {
		t.Errorf("the listing does not name the store the run wrote to:\n%s", stdout)
	}
	if !strings.Contains(stdout, "fixture.example/killable") {
		t.Errorf("the listing does not name the module that was measured:\n%s", stdout)
	}
	if !strings.Contains(stdout, "1 run in 1 workspace directory") {
		t.Errorf("the listing does not count the run:\n%s", stdout)
	}
	if strings.Contains(stdout, "could not read") {
		t.Errorf("a document go-mutants had just written could not be read back:\n%s", stdout)
	}

	// The summary names the run and the file, and the file is the document the
	// run filed — schema and all.
	code, stdout, stderr = execute(t, "report", "latest")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`report latest` exited %d\n%s", code, stderr)
	}
	path := lastLine(stdout)
	if !strings.HasPrefix(path, store) {
		t.Fatalf("`report latest` did not name a file inside the store:\n%s", stdout)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file it named is not there: %v", err)
	}

	code, jsonOut, stderr := execute(t, "report", "latest", "--json")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`report latest --json` exited %d\n%s", code, stderr)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the stored document: %v", err)
	}
	if jsonOut != string(stored) {
		t.Error("--json printed something other than the stored bytes")
	}
	if err = schemas.Validate(schemas.RunReportV1, []byte(jsonOut)); err != nil {
		t.Fatalf("the stored document does not satisfy %s: %v", schemas.RunReportV1, err)
	}
	var doc struct {
		RunID   string `json:"run_id"`
		Summary struct {
			Total int `json:"total"`
		} `json:"summary"`
	}
	if err = json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		t.Fatalf("decoding the stored document: %v", err)
	}
	if !strings.Contains(stdout, doc.RunID) {
		t.Errorf("the summary does not name the run in the document it read:\n%s", stdout)
	}
	if doc.Summary.Total == 0 {
		t.Error("the run measured nothing, so this proves less than it should")
	}

	// And `clean` removes exactly what the run filed.
	code, stdout, stderr = execute(t, "report", "clean")
	if code != int(mutation.ExitOK) {
		t.Fatalf("`report clean` exited %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "removed ") {
		t.Errorf("the sweep removed nothing:\n%s", stdout)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the stored run survived the clean: %v", err)
	}
	// The marker stays, which is what lets a run already in flight keep its
	// claim on the directory.
	dir := filepath.Dir(filepath.Dir(path))
	if _, err = os.Stat(filepath.Join(dir, report.MarkerFileName)); err != nil {
		t.Errorf("the ownership marker was deleted: %v", err)
	}
	if code, stdout, _ = execute(t, "report", "list"); !strings.Contains(stdout, "no run is recorded") {
		t.Errorf("history survived the clean (exit %d):\n%s", code, stdout)
	}
}

// lastLine returns the last non-empty line of some output.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
