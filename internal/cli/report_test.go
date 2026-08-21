// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
)

// shardDocument is a complete, valid shard report with an empty catalogue.
//
// It is written out rather than built through internal/report because these
// tests are about the command line: what `report merge` does with the files it
// is handed, and what it says when they cannot be merged. The document has no
// mutants in it on purpose — a merge of two empty shards is still a merge, and
// everything about combining rows is proven in internal/report against the
// fixture that has one of every outcome.
const shardDocument = `{
  "document_type": "go-mutants/run-report",
  "schema_version": 1,
  "tool_version": "0.0.0-test",
  "run_id": "%s",
  "status": "completed",
  "started_at": "2026-02-18T09:15:00Z",
  "finished_at": "2026-02-18T09:15:42Z",
  "duration_ms": 42000,
  "workspace": {
    "module_path": "example.com/m",
    "go_version": "1.26",
    "workspace_digest": "%s",
    "platform": { "os": "linux", "arch": "amd64" }
  },
  "selection": {
    "mode": "shard",
    "changed_ref": null,
    "profile": "balanced",
    "operators": [],
    "include": ["**/*.go"],
    "exclude": [],
    "candidates": 0,
    "rejected": 0,
    "selected": 0
  },
  "shard": { "index": %d, "total": %d, "assignment": "id-hash-v1" },
  "test": {
    "command": ["go", "test", "./..."],
    "baseline": { "runs": 1, "durations_ms": [1200], "slowest_ms": 1200 },
    "timeout_ms": 10000,
    "timeout_source": "derived"
  },
  "coverage": { "mode": "off" },
  "cache": { "mode": "off", "hits": 0, "misses": 0, "writes": 0 },
  "summary": {
    "total": 0,
    "killed": 0,
    "survived": 0,
    "timed_out": 0,
    "inconclusive": 0,
    "errored": 0,
    "not_run": 0,
    "score_percent": null,
    "policy": { "strict": false, "minimum_score": 0, "require_mutants": false, "failure": null }
  },
  "mutants": [],
  "rejected": [],
  "skips": [],
  "expectations": [],
  "warnings": []
}
`

// testDigest is a real 64 hex characters, which the schema insists on.
var testDigest = strings.Repeat("ab", 32)

// writeShard writes one shard document into dir and returns its path.
func writeShard(t *testing.T, dir string, index, total int, digest string) string {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("shard-%d.json", index))
	runID := fmt.Sprintf("20260218T09150%dZ-3f9c", index)
	body := fmt.Sprintf(shardDocument, runID, digest, index, total)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestReportMergeWritesTheWholeRun(t *testing.T) {
	dir := t.TempDir()
	first := writeShard(t, dir, 1, 2, testDigest)
	second := writeShard(t, dir, 2, 2, testDigest)

	code, stdout, stderr := execute(t, "report", "merge", first, second)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	merged, err := report.Parse([]byte(stdout))
	if err != nil {
		t.Fatalf("the merged document does not parse: %v\n%s", err, stdout)
	}
	if merged.Shard != nil {
		t.Errorf("the merged document claims to be shard %+v", merged.Shard)
	}
	if merged.Merge == nil || merged.Merge.Shards != 2 {
		t.Errorf("merge = %+v, want 2 shards", merged.Merge)
	}
	if merged.Selection.Mode != report.ModeAll {
		t.Errorf("selection.mode = %q, want %q", merged.Selection.Mode, report.ModeAll)
	}
	// The merged document is a document in its own right, so its id is its own
	// and is not borrowed from a shard.
	if merged.RunID == "20260218T091501Z-3f9c" {
		t.Error("the merged document kept a shard's run id")
	}
}

func TestReportMergeWritesToAFile(t *testing.T) {
	dir := t.TempDir()
	first := writeShard(t, dir, 1, 2, testDigest)
	second := writeShard(t, dir, 2, 2, testDigest)
	out := filepath.Join(dir, "merged.json")

	code, stdout, stderr := execute(t, "report", "merge", "--output", out, first, second)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if stdout != "" {
		t.Errorf("standard output is not empty: %q", stdout)
	}
	if !strings.Contains(stderr, out) {
		t.Errorf("the path is not reported: %q", stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading the merged document: %v", err)
	}
	if _, err = report.Parse(data); err != nil {
		t.Fatalf("the written document does not parse: %v", err)
	}
}

func TestReportMergeRefusesShardsOfDifferentRuns(t *testing.T) {
	dir := t.TempDir()
	first := writeShard(t, dir, 1, 2, testDigest)
	second := writeShard(t, dir, 2, 2, strings.Repeat("cd", 32))

	code, _, stderr := execute(t, "report", "merge", first, second)
	if code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, stderr)
	}
	if !strings.Contains(stderr, string(report.CodeIncongruentShards)) {
		t.Errorf("the refusal does not carry %s: %s", report.CodeIncongruentShards, stderr)
	}
	if !strings.Contains(stderr, "workspace digest") {
		t.Errorf("the refusal does not name the field: %s", stderr)
	}
}

func TestReportMergeRefusesAnIncompleteSet(t *testing.T) {
	dir := t.TempDir()
	only := writeShard(t, dir, 1, 2, testDigest)

	code, _, stderr := execute(t, "report", "merge", only)
	if code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, stderr)
	}
	if !strings.Contains(stderr, string(report.CodeIncompleteShardSet)) {
		t.Errorf("the refusal does not carry %s: %s", report.CodeIncompleteShardSet, stderr)
	}
}

func TestReportMergeNamesTheFileItCannotRead(t *testing.T) {
	dir := t.TempDir()
	good := writeShard(t, dir, 1, 2, testDigest)
	bad := filepath.Join(dir, "not-a-report.json")
	if err := os.WriteFile(bad, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", bad, err)
	}

	code, _, stderr := execute(t, "report", "merge", good, bad)
	if code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, stderr)
	}
	if !strings.Contains(stderr, "not-a-report.json") {
		t.Errorf("the refusal does not name the file: %s", stderr)
	}
	if !strings.Contains(stderr, string(CodeInvalidReportDocument)) {
		t.Errorf("the refusal does not carry %s: %s", CodeInvalidReportDocument, stderr)
	}
}

func TestReportMergeWithNoFiles(t *testing.T) {
	code, _, stderr := execute(t, "report", "merge")
	if code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, stderr)
	}
	if !strings.Contains(stderr, string(CodeUsage)) {
		t.Errorf("the refusal does not carry %s: %s", CodeUsage, stderr)
	}
}

func TestReportValidateAcceptsADocumentGoMutantsWrote(t *testing.T) {
	dir := t.TempDir()
	path := writeShard(t, dir, 1, 2, testDigest)

	code, stdout, stderr := execute(t, "report", "validate", path)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{path, "valid", report.DocumentType} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the confirmation does not mention %q: %q", want, stdout)
		}
	}
}

func TestReportValidateReportsTheFirstViolation(t *testing.T) {
	dir := t.TempDir()
	path := writeShard(t, dir, 1, 2, testDigest)

	// A document that parses and is not valid: the shard index is below one,
	// which only the schema refuses.
	var doc map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if err = json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	doc["shard"].(map[string]any)["index"] = 0.0
	edited, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("re-encoding: %v", err)
	}
	if err = os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatalf("rewriting %s: %v", path, err)
	}

	code, _, stderr := execute(t, "report", "validate", path)
	if code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, stderr)
	}
	if !strings.Contains(stderr, "/shard/index") {
		t.Errorf("the failure does not point at the field: %s", stderr)
	}
}

func TestReportValidateNeedsExactlyOneFile(t *testing.T) {
	for _, args := range [][]string{{"report", "validate"}, {"report", "validate", "a.json", "b.json"}} {
		code, _, stderr := execute(t, args...)
		if code != 2 {
			t.Errorf("%v exited %d, want 2", args, code)
		}
		if !strings.Contains(stderr, string(CodeUsage)) {
			t.Errorf("%v does not carry %s: %s", args, CodeUsage, stderr)
		}
	}
}

func TestReportValidateNamesAFileItCannotRead(t *testing.T) {
	code, _, stderr := execute(t, "report", "validate", filepath.Join(t.TempDir(), "absent.json"))
	if code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, stderr)
	}
	if !strings.Contains(stderr, string(CodeUnreadableReport)) {
		t.Errorf("the refusal does not carry %s: %s", CodeUnreadableReport, stderr)
	}
}

func TestReportWithNoSubcommandPrintsHelp(t *testing.T) {
	code, stdout, stderr := execute(t, "report")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"merge", "validate"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the help does not offer %q: %s", want, stdout)
		}
	}
}

// TestRunRefusesExplainWithJSON and its `list` twin hold the one flag conflict
// this phase adds. It is a semantic check rather than cobra's mutual exclusion
// so that it carries a code and a remedy; see [checkExplain].
func TestRunRefusesExplainWithJSON(t *testing.T) {
	err := runWith(t, "--explain", "--json")
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeConflictingFlags {
		t.Fatalf("run --explain --json = %v, want %s", err, CodeConflictingFlags)
	}
	if coded.Hint == "" {
		t.Error("the conflict names no remedy")
	}
}

func TestListRefusesExplainWithJSON(t *testing.T) {
	code, _, stderr := execute(t, "list", "--explain", "--json")
	if code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, stderr)
	}
	if !strings.Contains(stderr, string(CodeConflictingFlags)) {
		t.Errorf("the refusal does not carry %s: %s", CodeConflictingFlags, stderr)
	}
}

func TestRunRefusesMutantWithANarrowingFlag(t *testing.T) {
	prefix := strings.Repeat("a", 8)
	for _, args := range [][]string{
		{"--mutant", prefix, "--changed"},
		{"--mutant", prefix, "--shard", "1/2"},
	} {
		err := runWith(t, args...)
		var coded *Error
		if !errors.As(err, &coded) || coded.Code != CodeConflictingFlags {
			t.Fatalf("run %v = %v, want %s", args, err, CodeConflictingFlags)
		}
		if coded.Hint == "" {
			t.Errorf("run %v names no remedy", args)
		}
	}
}

func TestRunRefusesAShardThatIsNotOne(t *testing.T) {
	for _, spec := range []string{"3/2", "0/4", "two/four", "4"} {
		err := runWith(t, "--shard", spec)
		if err == nil {
			t.Fatalf("run --shard %s was accepted", spec)
		}
		if code := report.CodeOf(err); code != report.CodeInvalidShardSpec {
			t.Errorf("run --shard %s code = %q, want %q (%v)", spec, code, report.CodeInvalidShardSpec, err)
		}
	}
}

// TestChangedNeedsAnEqualsSign covers pflag's rule for an optional-value flag,
// which is the one way a correct-looking `--changed` command line goes wrong.
func TestChangedNeedsAnEqualsSign(t *testing.T) {
	err := runWith(t, "--changed", "origin/main")
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeUsage {
		t.Fatalf("run --changed origin/main = %v, want %s", err, CodeUsage)
	}
	if !strings.Contains(coded.Hint, "--changed=origin/main") {
		t.Errorf("the hint does not show the right spelling: %q", coded.Hint)
	}
}

// TestChangedWithAnEqualsSignIsAccepted proves the flag parses in the form the
// hint recommends, and that the bare form is accepted too. Neither reaches the
// engine here: what happens next needs a repository, and internal/gitdiff owns
// that half.
func TestChangedIsAcceptedInBothForms(t *testing.T) {
	for _, args := range [][]string{{"--changed"}, {"--changed=origin/main"}} {
		cmd := newRunCommand()
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatalf("run %v: %v", args, err)
		}
		if !cmd.Flags().Changed("changed") {
			t.Errorf("run %v did not record --changed as given", args)
		}
	}
}
