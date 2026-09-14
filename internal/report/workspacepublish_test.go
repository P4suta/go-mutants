// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

// TestPublishingAWorkspaceWritesOnePageForTheWholeTree is what the artefacts of
// a workspace run are, and why they are one pair rather than N.
//
// The viewer's question is about the project: a reader opening the HTML report
// wants the tree, and a directory of N pages would make them open N of them to
// find out whether anything survived. Every path in the document is relative to
// the workspace root, which is what a mutation-testing-report's paths mean --
// and what keeps two modules' `app.go` from being one entry.
func TestPublishingAWorkspaceWritesOnePageForTheWholeTree(t *testing.T) {
	t.Parallel()

	root := workspaceTree(t)
	doc := workspaceDocumentAt(t, root)
	artifacts, err := report.WriteArtifacts(report.ArtifactOptions{
		Workspace:     doc,
		WorkspaceRoot: root,
		Directory:     "reports/mutation",
		Formats:       []config.ReportFormat{config.FormatJSON, config.FormatHTML},
	})
	if err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}
	if artifacts.ProjectionPath == "" || artifacts.HTMLPath == "" {
		t.Fatalf("published %+v, want both a projection and a page", artifacts)
	}
	written, err := os.ReadFile(artifacts.ProjectionPath)
	if err != nil {
		t.Fatalf("reading the projection: %v", err)
	}
	for _, want := range []string{`"first/app.go"`, `"second/app.go"`} {
		if !strings.Contains(string(written), want) {
			t.Errorf("the projection does not key a file at %s:\n%s", want, written)
		}
	}
	if strings.Contains(string(written), `"app.go"`) {
		t.Errorf("the projection holds a key relative to a module rather than to the workspace:\n%s", written)
	}
}

// TestProjectingAWorkspaceRefusesWhatItHasNoDocumentFor is the fail-closed half
// of both entry points.
//
// A projection with no report at all is a document about nothing, and one whose
// module carries no report is a document that lost one — and a viewer handed
// either would show a tree with a module silently missing from it.
func TestProjectingAWorkspaceRefusesWhatItHasNoDocumentFor(t *testing.T) {
	t.Parallel()

	root := workspaceTree(t)
	doc := workspaceDocumentAt(t, root)

	if _, err := report.ProjectWorkspace(report.WorkspaceProjectionOptions{
		WorkspaceRoot: root,
	}); err == nil {
		t.Error("ProjectWorkspace accepted a call with no workspace report")
	} else if !strings.Contains(err.Error(), "no workspace report") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}

	missing := *doc
	missing.Modules = append([]report.ModuleReport(nil), doc.Modules...)
	missing.Modules[0].Report = nil
	if _, err := report.ProjectWorkspace(report.WorkspaceProjectionOptions{
		Workspace:     &missing,
		WorkspaceRoot: root,
	}); err == nil {
		t.Error("ProjectWorkspace projected a module with no report")
	}

	// And the artefacts, which is the same refusal one level up: neither kind
	// of document given is a run that published nothing, and the message names
	// the directory the caller asked to publish into.
	_, err := report.WriteArtifacts(report.ArtifactOptions{
		WorkspaceRoot: root,
		Directory:     "reports/mutation",
		Formats:       []config.ReportFormat{config.FormatJSON},
	})
	if err == nil {
		t.Fatal("WriteArtifacts published a run with no document at all")
	}
	if !strings.Contains(err.Error(), "no report to publish into reports/mutation") {
		t.Errorf("the refusal does not name the directory: %v", err)
	}
}

// TestFilingAWorkspaceRunRefusesAnIdentityItCannotName is the history store's
// half, and it is the run report's refusals over the other document type.
//
// Both values name a file: the run id names the document under `runs/`, and the
// workspace digest names the directory that holds it. A store that accepted
// either unchecked would write outside the directory it owns, or into a file
// named after something that is not a run.
func TestFilingAWorkspaceRunRefusesAnIdentityItCannotName(t *testing.T) {
	t.Parallel()

	root := workspaceTree(t)
	good := workspaceDocumentAt(t, root)
	history := report.History{Root: t.TempDir()}

	runPath, latestPath, err := history.WriteWorkspace(good)
	if err != nil {
		t.Fatalf("WriteWorkspace: %v", err)
	}
	for _, path := range []string{runPath, latestPath} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("reading %s: %v", path, readErr)
		}
		if !strings.Contains(string(data), report.WorkspaceDocumentType) {
			t.Errorf("%s does not hold a workspace report", path)
		}
	}
	if filepath.Base(runPath) != good.RunID+".json" {
		t.Errorf("the run document is at %s, want it named after the run", runPath)
	}

	for _, tc := range []struct {
		name string
		doc  *report.WorkspaceReport
		says string
	}{
		{name: "no document at all", says: "no workspace report to write"},
		{
			name: "a run id that cannot name a file",
			doc:  withRunID(*good, "../escape"),
			says: "cannot be used as a file name",
		},
		{
			name: "a digest that cannot name a directory",
			doc:  withDigest(*good, "not-a-digest"),
			says: "cannot name a history directory",
		},
		{
			// A document that cannot be encoded: a run report's
			// `score_percent` is a `*float64`, and a non-finite float is the
			// one value encoding/json has no representation for.
			name: "a document that cannot be encoded",
			doc:  withUnencodableScore(*good),
			says: "could not be encoded as JSON",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runPath, latestPath, err := report.History{Root: t.TempDir()}.WriteWorkspace(tc.doc)
			if err == nil {
				t.Fatal("WriteWorkspace filed it")
			}
			if runPath != "" || latestPath != "" {
				t.Errorf("a refused write reported %q and %q", runPath, latestPath)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

// withRunID and withDigest are one field of a workspace report changed, for the
// refusals above.
func withRunID(doc report.WorkspaceReport, runID string) *report.WorkspaceReport {
	doc.RunID = runID
	return &doc
}

func withDigest(doc report.WorkspaceReport, digest string) *report.WorkspaceReport {
	doc.Workspace.WorkspaceDigest = digest
	return &doc
}

// withUnencodableScore puts a NaN where a module's score goes, which is the one
// value a workspace report can hold that encoding/json refuses.
func withUnencodableScore(doc report.WorkspaceReport) *report.WorkspaceReport {
	doc.Modules = append([]report.ModuleReport(nil), doc.Modules...)
	rep := *doc.Modules[0].Report
	unencodable := math.NaN()
	rep.Summary.ScorePercent = &unencodable
	doc.Modules[0].Report = &rep
	return &doc
}

// workspaceTree writes the two modules' sources where the projection reads
// them, and returns the workspace root.
func workspaceTree(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	for dir, module := range map[string]string{"first": firstModule, "second": secondModule} {
		source := sourceOfModule(module)
		path := filepath.Join(root, dir, "app.go")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("making %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	return root
}

// workspaceDocumentAt is a built workspace report whose spans match the tree.
func workspaceDocumentAt(t *testing.T, root string) *report.WorkspaceReport {
	t.Helper()

	doc, err := report.BuildWorkspace(workspaceRunOptions(t))
	if err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	return doc
}

// TestAWorkspaceRunIsListedAsARunOfItsOwn is what the history store says about
// the other document type, and it is two claims rather than one.
//
// A workspace run is a run: a listing that named only run reports would make a
// workspace's history look empty, and a `report clean` would leave it behind.
// And it is a run of the *workspace* rather than of any module in it: it names
// its modules and no module path, so a command asking "is this run mine" gets a
// different answer standing in a module from the one it gets standing in the
// workspace. That is the point — a mutant measured in a workspace and the same
// mutant measured alone have different identities, and offering one run's
// documents for the other's question would be offering ids that do not mean
// what the reader thinks.
func TestAWorkspaceRunIsListedAsARunOfItsOwn(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	doc := workspaceDocumentAt(t, workspaceTree(t))
	if _, _, err := (report.History{Root: root}).WriteWorkspace(doc); err != nil {
		t.Fatalf("WriteWorkspace: %v", err)
	}
	listing, err := report.History{Root: root}.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing.Workspaces) != 1 {
		t.Fatalf("the store holds %d workspace directories, want the one just written",
			len(listing.Workspaces))
	}
	stored := listing.Workspaces[0]
	if len(stored.Damaged) != 0 {
		t.Errorf("the workspace document was read as damaged: %+v", stored.Damaged)
	}
	if len(stored.Runs) != 1 {
		t.Fatalf("the directory lists %d runs, want the one written: %+v", len(stored.Runs), stored.Runs)
	}
	run := stored.Runs[0]
	if run.RunID != doc.RunID {
		t.Errorf("the listed run is %s, want %s", run.RunID, doc.RunID)
	}
	if run.ModulePath != "" {
		t.Errorf("the listed run names the module path %q; a workspace run has none", run.ModulePath)
	}
	want := []string{firstModule, secondModule}
	if !slices.Equal(run.Modules, want) {
		t.Errorf("the listed run names %v, want %v", run.Modules, want)
	}
	if run.Summary.Total != doc.Summary.Total {
		t.Errorf("the listed summary counts %d mutants, want %d",
			run.Summary.Total, doc.Summary.Total)
	}
}

// TestMergingAWorkspaceRunsShardsReassemblesIt is the round trip: a workspace
// run split N ways and put back together is the run it was split from.
//
// The module-level merge is [report.MergeShards] itself rather than a second
// implementation of it, so what this adds is the check a per-module merge
// cannot make -- that every shard holds the same modules, in the same order.
func TestMergingAWorkspaceRunsShardsReassemblesIt(t *testing.T) {
	t.Parallel()

	whole, err := report.BuildWorkspace(workspaceRunOptions(t))
	if err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	shards := workspaceShards(t, 2)

	merged, err := report.MergeWorkspaces(report.MergeWorkspaceOptions{
		RunID:  "20260218T091599Z-9999",
		Shards: shards,
	})
	if err != nil {
		t.Fatalf("MergeWorkspaces: %v", err)
	}
	if merged.RunID != "20260218T091599Z-9999" {
		t.Errorf("the merged document is run %s, want the id it was given", merged.RunID)
	}
	if merged.Summary.Total != whole.Summary.Total || merged.Summary.Killed != whole.Summary.Killed {
		t.Errorf("the merged summary counts %d of %d, want the whole run's %d of %d",
			merged.Summary.Killed, merged.Summary.Total,
			whole.Summary.Killed, whole.Summary.Total)
	}
	// The merge is not an infrastructure failure, whatever a shard said: the
	// merged document's verdict is the policy's, over the counts above.
	if merged.Summary.Policy.Failure != nil {
		t.Errorf("the merged run failed with %q; the shards were green and complete",
			*merged.Summary.Policy.Failure)
	}
	if len(merged.Modules) != len(whole.Modules) {
		t.Fatalf("the merged document holds %d modules, want %d",
			len(merged.Modules), len(whole.Modules))
	}
	for i, module := range merged.Modules {
		if module.ModulePath != whole.Modules[i].ModulePath {
			t.Errorf("module %d is %s, want %s", i, module.ModulePath, whole.Modules[i].ModulePath)
		}
		if module.Report.Merge == nil || module.Report.Merge.Shards != len(shards) {
			t.Errorf("%s's report does not record the merge it is: %+v",
				module.ModulePath, module.Report.Merge)
		}
	}
}

// TestMergingWorkspacesRefusesWhatIsNotOneRun is the fail-closed half, and
// every row is a set of documents that would merge into a smaller run than
// anybody measured.
func TestMergingWorkspacesRefusesWhatIsNotOneRun(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		shards func(*testing.T) []*report.WorkspaceReport
		says   string
	}{
		{
			name:   "nothing to merge",
			shards: func(*testing.T) []*report.WorkspaceReport { return nil },
			says:   "no shard reports to merge",
		},
		{
			name: "a document that is not there",
			shards: func(t *testing.T) []*report.WorkspaceReport {
				return []*report.WorkspaceReport{workspaceShards(t, 2)[0], nil}
			},
			says: "second report to merge is missing",
		},
		{
			name: "a shard of a workspace with fewer modules",
			shards: func(t *testing.T) []*report.WorkspaceReport {
				shards := workspaceShards(t, 2)
				short := *shards[1]
				short.Modules = short.Modules[:1]
				shards[1] = &short
				return shards
			},
			says: "the second report holds 1 modules and the first holds 2",
		},
		{
			name: "a module whose reports are not shards",
			shards: func(t *testing.T) []*report.WorkspaceReport {
				shards := workspaceShards(t, 2)
				whole := *shards[1]
				whole.Modules = append([]report.ModuleReport(nil), whole.Modules...)
				unsharded := *whole.Modules[0].Report
				unsharded.Shard = nil
				whole.Modules[0].Report = &unsharded
				shards[1] = &whole
				return shards
			},
			says: "was not produced by a --shard run",
		},
		{
			name: "a shard of a different workspace",
			shards: func(t *testing.T) []*report.WorkspaceReport {
				shards := workspaceShards(t, 2)
				other := *shards[1]
				other.Modules = append([]report.ModuleReport(nil), other.Modules...)
				other.Modules[0].ModulePath = "example.com/elsewhere"
				shards[1] = &other
				return shards
			},
			says: `the second report's first module is "example.com/elsewhere"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			merged, err := report.MergeWorkspaces(report.MergeWorkspaceOptions{
				RunID:  "20260218T091599Z-9999",
				Shards: tc.shards(t),
			})
			if err == nil {
				t.Fatalf("MergeWorkspaces accepted it and returned %+v", merged)
			}
			if merged != nil {
				t.Errorf("a refusal also returned a document: %+v", merged)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

// workspaceShards is one workspace run split into a complete set of shards.
func workspaceShards(t *testing.T, total int) []*report.WorkspaceReport {
	t.Helper()

	shards := make([]*report.WorkspaceReport, 0, total)
	for shard := 1; shard <= total; shard++ {
		index := shard
		opts := workspaceRunOptions(t)
		opts.RunID = fmt.Sprintf("20260218T09150%dZ-3f9c", index)
		opts.Mode = report.ModeShard
		opts.Shard = &report.Shard{Index: index, Total: total}
		selected := 0
		for i, result := range opts.Results {
			if mutation.ShardIndex(result.ID, total) == index {
				selected++
				continue
			}
			opts.Results[i] = report.MutantResult{
				ID:           result.ID,
				Outcome:      mutation.OutcomeNotRun,
				NotRunReason: report.NotRunOtherShard,
			}
		}
		for i := range opts.Modules {
			opts.Modules[i].Selected = 0
			for _, m := range opts.Catalog.Mutants() {
				if m.ModulePath == opts.Modules[i].Path &&
					mutation.ShardIndex(m.ID, total) == index {
					opts.Modules[i].Selected++
				}
			}
		}
		doc, err := report.BuildWorkspace(opts)
		if err != nil {
			t.Fatalf("BuildWorkspace for shard %d: %v", index, err)
		}
		shards = append(shards, doc)
	}
	return shards
}

// TestTheTwoRunDocumentsShareASchemaVersion is a one-line assumption the
// history store's reader depends on.
//
// It checks one version for both kinds of document a run publishes, and that is
// correct only while the two are at the same version. Stated here rather than
// branched on there: a branch nothing could distinguish would be a branch no
// test could tell from its absence, and this is the assertion that turns "they
// happen to agree" into "they are required to".
func TestTheTwoRunDocumentsShareASchemaVersion(t *testing.T) {
	t.Parallel()

	if report.SchemaVersion != report.WorkspaceSchemaVersion {
		t.Errorf("a run report is v%d and a workspace report is v%d; internal/report's store "+
			"reader checks one version for both and now needs a branch",
			report.SchemaVersion, report.WorkspaceSchemaVersion)
	}
}
