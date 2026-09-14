// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// What the commands do at the root of a `go.work`, against a real toolchain.
//
// A workspace is measured as one run over one catalogue that spans its modules
// -- see ADR 0012 -- and everything downstream of that is a document with a
// different shape: `run` publishes a workspace report, `report merge` puts its
// shards back together, and the history commands treat the workspace as a
// project of its own rather than as any module inside it.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/cli/...
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testsupport"
)

// TestAWorkspaceRunIsPublishedMergedAndExplainedAsOne is the command line's
// half of ADR 0012, end to end and in one test because the three claims are one
// claim: a workspace run is a run of the workspace.
//
// It publishes a workspace report rather than a run report, because
// `workspace.module_path` is required of the latter and a workspace has no
// single answer for it. Its shards merge back into one, module by module.
// And the history commands find it standing in the workspace root, where there
// is no `go.mod` to name the project by.
func TestAWorkspaceRunIsPublishedMergedAndExplainedAsOne(t *testing.T) {
	// The run files a report, so os.UserCacheDir is redirected before anything
	// runs -- by [testsupport.CacheDir], because which variable it reads is a
	// property of the operating system and not of this test.
	testsupport.CacheDir(t)
	root := testkit.Copy(t, "workspace")
	t.Chdir(root)

	// Two shards, each writing its document outside the tree: a file written
	// inside it between the runs would change the snapshot digest, and the two
	// would stop being shards of one run.
	shards := t.TempDir()
	for _, shard := range []string{"1/2", "2/2"} {
		var out, errOut bytes.Buffer
		code := ExecuteContext(t.Context(),
			[]string{"run", "--no-tui", "--report", "none", "--shard", shard, "--json"},
			&out, &errOut)
		if code != 0 {
			t.Fatalf("shard %s exited %d\nstderr:\n%s", shard, code, errOut.String())
		}
		name := filepath.Join(shards, strings.ReplaceAll(shard, "/", "-")+".json")
		if err := os.WriteFile(name, out.Bytes(), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		if documentTypeOf(t, out.Bytes()) != report.WorkspaceDocumentType {
			t.Fatalf("shard %s published a %q, want a workspace report",
				shard, documentTypeOf(t, out.Bytes()))
		}
	}

	var merged, errOut bytes.Buffer
	if code := ExecuteContext(t.Context(), []string{
		"report", "merge",
		filepath.Join(shards, "1-2.json"), filepath.Join(shards, "2-2.json"),
	}, &merged, &errOut); code != 0 {
		t.Fatalf("report merge exited %d\nstderr:\n%s", code, errOut.String())
	}
	var document struct {
		DocumentType string `json:"document_type"`
		Summary      struct {
			Total    int `json:"total"`
			Killed   int `json:"killed"`
			Survived int `json:"survived"`
		} `json:"summary"`
		Modules []struct {
			ModulePath string `json:"module_path"`
			Report     struct {
				Merge *struct {
					Shards int `json:"shards"`
				} `json:"merge"`
			} `json:"report"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(merged.Bytes(), &document); err != nil {
		t.Fatalf("decoding the merged document: %v", err)
	}
	if document.DocumentType != report.WorkspaceDocumentType {
		t.Errorf("the merge produced a %q", document.DocumentType)
	}
	if document.Summary.Total != 11 || document.Summary.Killed != 11 {
		t.Errorf("the merged run is %d of %d killed, want every mutant of the workspace",
			document.Summary.Killed, document.Summary.Total)
	}
	if len(document.Modules) != 3 {
		t.Fatalf("the merged document holds %d modules, want three", len(document.Modules))
	}
	for _, module := range document.Modules {
		if module.Report.Merge == nil || module.Report.Merge.Shards != 2 {
			t.Errorf("%s's report does not record that it is a merge of two shards", module.ModulePath)
		}
	}

	// And a whole run, filed in the history, found from the workspace root --
	// where `moduleAt` has nothing to read, because a workspace root is not a
	// module.
	var runOut, runErr bytes.Buffer
	if code := ExecuteContext(t.Context(),
		[]string{"run", "--no-tui", "--report", "none"}, &runOut, &runErr); code != 0 {
		t.Fatalf("the whole run exited %d\nstderr:\n%s", code, runErr.String())
	}
	var latest, latestErr bytes.Buffer
	if code := ExecuteContext(t.Context(), []string{"report", "latest", "--json"},
		&latest, &latestErr); code != 0 {
		t.Fatalf("report latest exited %d\nstderr:\n%s", code, latestErr.String())
	}
	if got := documentTypeOf(t, latest.Bytes()); got != report.WorkspaceDocumentType {
		t.Errorf("the stored run is a %q, want a workspace report", got)
	}
}

// documentTypeOf is what a document says it is.
func documentTypeOf(t *testing.T, data []byte) string {
	t.Helper()

	got, err := report.DocumentTypeOf(data)
	if err != nil {
		t.Fatalf("reading the document type: %v", err)
	}
	return got
}
