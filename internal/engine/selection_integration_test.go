// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of the selection tests: a real git repository, a
// real toolchain, and real mutant processes, because everything interesting
// about `--changed` and `--shard` is whether the narrowing survives the round
// trip through discovery, validation, execution and the report.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/engine/...
package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
)

// The identity the scripted repository commits under. It is set through the
// environment rather than through `git config`, so that the engine's own git —
// which runs with this process's environment and not with a set the test
// composed — reads exactly the same configuration the test does.
const (
	gitAuthor    = "go-mutants tests"
	gitEmail     = "tests@go-mutants.invalid"
	gitTimestamp = "2026-02-18T09:15:00+00:00"
)

// touchedFile and its test are the new work a `--changed` run should find. They
// are a new file rather than an edit to an existing one for two reasons: git
// reports every line of it as added, so the expected selection is "every mutant
// in this file" and needs no line arithmetic; and nothing in the fixture moves,
// so the rest of the catalogue is exactly what an unsharded, unchanged run
// would have found.
const (
	touchedFile = "touched.go"
	touchedTest = "touched_test.go"

	touchedSource = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

// Touched is the work a --changed run is meant to notice. It carries a
// comparison and two arithmetic mutants, and the test beside it covers them.
func Touched(a, b int) int {
	if a > b {
		return a - b
	}
	return a + b
}
`

	touchedTestSource = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

import "testing"

func TestTouched(t *testing.T) {
	if got := Touched(7, 2); got != 5 {
		t.Errorf("Touched(7, 2) = %d, want 5", got)
	}
	if got := Touched(2, 7); got != 9 {
		t.Errorf("Touched(2, 7) = %d, want 9", got)
	}
}
`
)

// gitRepo copies a fixture module into a temporary directory and makes it a
// repository with one commit, returning the root and the base commit.
//
// The whole environment is redirected rather than a private one composed for
// the test's own commands: the engine resolves the diff through its own git,
// with this process's environment, so a `~/.gitconfig` that the test neutered
// only for itself would still be able to change what the engine sees.
func gitRepo(t *testing.T, fixtureName string) (root, base string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH, so --changed cannot be exercised here: %v", err)
	}
	root = filepath.Join(t.TempDir(), fixtureName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating the workspace: %v", err)
	}
	if err := os.CopyFS(root, os.DirFS(fixture(t, fixtureName))); err != nil {
		t.Fatalf("copying the %s fixture: %v", fixtureName, err)
	}

	configDir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(configDir, "absent-global-config"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(configDir, "absent-system-config"))
	t.Setenv("GIT_AUTHOR_NAME", gitAuthor)
	t.Setenv("GIT_AUTHOR_EMAIL", gitEmail)
	t.Setenv("GIT_AUTHOR_DATE", gitTimestamp)
	t.Setenv("GIT_COMMITTER_NAME", gitAuthor)
	t.Setenv("GIT_COMMITTER_EMAIL", gitEmail)
	t.Setenv("GIT_COMMITTER_DATE", gitTimestamp)

	git(t, root, "init", "--quiet")
	git(t, root, "add", "--all")
	git(t, root, "commit", "--quiet", "--message", "the fixture as it was")
	return root, git(t, root, "rev-parse", "HEAD")
}

// git runs one command in the repository, failing the test if it does not
// succeed.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", dir, "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeWorkFile adds a file to the working tree.
func writeWorkFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// gitOptions is [options] against a workspace of the test's own rather than
// against the fixture in the repository, since a `--changed` run has to be able
// to write a commit into the tree it reads.
func gitOptions(t *testing.T, root string) Options {
	t.Helper()
	cfg := config.Defaults()
	cfg.Test.BaselineRuns = 1
	cfg.Execution.Jobs = 2
	return Options{
		Config:        cfg,
		WorkspaceRoot: root,
		ToolVersion:   testToolVersion,
		HistoryRoot:   t.TempDir(),
	}
}

// TestChangedRunExecutesOnlyTheMutantsOnEditedLines is the whole of `--changed`
// end to end.
//
// The assertion is an exact set rather than a count, and it is stated from the
// document: every mutant in the new file was measured, every mutant anywhere
// else was reported as not-run with `out-of-selection`, and the catalogue holds
// both — which is the property the feature rests on. Discovery and validation
// still cover the whole module, so the ids here are the ids a full run would
// mint and the two reports can be compared.
func TestChangedRunExecutesOnlyTheMutantsOnEditedLines(t *testing.T) {
	privateTempDir(t)
	root, base := gitRepo(t, "families")
	writeWorkFile(t, root, touchedFile, touchedSource)
	writeWorkFile(t, root, touchedTest, touchedTestSource)
	git(t, root, "add", "--all")
	git(t, root, "commit", "--quiet", "--message", "the work this run is about")

	opts := gitOptions(t, root)
	opts.Changed = true
	opts.ChangedRef = base
	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rep := outcome.Report
	if rep == nil {
		t.Fatal("the run published no report")
	}

	if rep.Selection.Mode != report.ModeChanged {
		t.Errorf("selection.mode = %q, want %q", rep.Selection.Mode, report.ModeChanged)
	}
	if rep.Selection.ChangedRef == nil || *rep.Selection.ChangedRef != base {
		t.Errorf("selection.changed_ref = %v, want %q", rep.Selection.ChangedRef, base)
	}
	if rep.Shard != nil {
		t.Errorf("an unsharded run reports shard %+v", rep.Shard)
	}

	var measured, skipped, elsewhere int
	for _, m := range rep.Mutants {
		onTheEdit := m.Path == touchedFile
		if !onTheEdit {
			elsewhere++
		}
		if m.Outcome == report.OutcomeNotRun {
			skipped++
			if onTheEdit {
				t.Errorf("mutant %s is on the edited file and was not run", m.DisplayID)
			}
			if m.NotRunReason == nil || *m.NotRunReason != string(report.NotRunOutOfSelection) {
				t.Errorf("mutant %s was not run for %v, want %q", m.DisplayID, m.NotRunReason, report.NotRunOutOfSelection)
			}
			continue
		}
		measured++
		if !onTheEdit {
			t.Errorf("mutant %s at %s:%d was measured and is not on an edited line",
				m.DisplayID, m.Path, m.Line)
		}
	}
	if measured == 0 {
		t.Fatal("the run measured nothing at all, so the assertion above proves nothing")
	}
	if elsewhere == 0 {
		t.Fatal("the catalogue holds nothing outside the edited file, so nothing was narrowed away")
	}
	if rep.Selection.Selected != measured {
		t.Errorf("selection.selected = %d and %d mutants were measured", rep.Selection.Selected, measured)
	}
	if rep.Summary.NotRun != skipped {
		t.Errorf("summary.not_run = %d and %d rows say not-run", rep.Summary.NotRun, skipped)
	}
	// The tests beside the new file catch some of what it carries, which is what
	// makes this a mutation run and not merely a selection one.
	if rep.Summary.Killed == 0 {
		t.Error("nothing in the edited file was killed, so the run measured nothing meaningful")
	}
	if err := schemas.Validate(schemas.RunReportV1, mustMarshalReport(t, rep)); err != nil {
		t.Errorf("the changed run's report does not satisfy the schema: %v", err)
	}
}

// TestChangedRunWithNothingChanged proves the honest empty case: a run whose
// diff is empty measures nothing and says so, rather than falling back to
// measuring everything.
func TestChangedRunWithNothingChanged(t *testing.T) {
	privateTempDir(t)
	root, base := gitRepo(t, "killable")

	opts := gitOptions(t, root)
	opts.Changed = true
	opts.ChangedRef = base
	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rep := outcome.Report
	if rep.Selection.Selected != 0 {
		t.Errorf("selection.selected = %d, want 0", rep.Selection.Selected)
	}
	if rep.Summary.NotRun != rep.Summary.Total {
		t.Errorf("%d of %d mutants are not-run, want all of them", rep.Summary.NotRun, rep.Summary.Total)
	}
	if rep.Summary.Total == 0 {
		t.Error("the catalogue is empty, so this proves nothing about narrowing")
	}
}

// TestChangedRunFailsWithoutARepository proves the fail-closed rule: a
// `--changed` run that cannot read a diff stops rather than quietly measuring
// everything or nothing.
func TestChangedRunFailsWithoutARepository(t *testing.T) {
	privateTempDir(t)
	opts := options(t, "killable")
	opts.Changed = true
	opts.ChangedRef = "HEAD"
	// The fixture lives inside go-mutants' own repository, so the run is pointed
	// at a copy outside one.
	root := filepath.Join(t.TempDir(), "killable")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating the workspace: %v", err)
	}
	if err := os.CopyFS(root, os.DirFS(fixture(t, "killable"))); err != nil {
		t.Fatalf("copying the fixture: %v", err)
	}
	opts.WorkspaceRoot = root

	outcome, _, err := collect(t, t.Context(), opts)
	if err == nil {
		t.Fatal("the run succeeded outside a repository")
	}
	if !strings.Contains(err.Error(), "GOM7711") {
		t.Errorf("error = %v, want the not-a-repository code", err)
	}
	if outcome.Report != nil {
		t.Error("a run that never started published a report")
	}
	// Nothing was copied or built: the diff is resolved before the workspace is.
	if outcome.SnapshotRoot != "" {
		t.Errorf("the run snapshotted %s before finding out it could not resolve the diff", outcome.SnapshotRoot)
	}
}

// TestShardedRunsMergeIntoTheUnshardedOne is the congruence property `--shard`
// and `report merge` exist to have.
//
// Two shards of one workspace, merged, have to reach the same verdict for every
// mutant as a run that was not split — otherwise a CI matrix and a laptop
// measure different things and nobody can say which to believe. The comparison
// is mutant for mutant rather than score against score: two runs can reach one
// score by disagreeing about two mutants in opposite directions.
func TestShardedRunsMergeIntoTheUnshardedOne(t *testing.T) {
	privateTempDir(t)

	whole, _, err := collect(t, t.Context(), options(t, "killable"))
	if err != nil {
		t.Fatalf("the unsharded run: %v", err)
	}

	const total = 2
	pieces := make([]*report.Report, 0, total)
	for index := 1; index <= total; index++ {
		opts := options(t, "killable")
		opts.Shard = report.Shard{Index: index, Total: total}
		outcome, _, shardErr := collect(t, t.Context(), opts)
		if shardErr != nil {
			t.Fatalf("shard %d of %d: %v", index, total, shardErr)
		}
		rep := outcome.Report
		if rep.Shard == nil || rep.Shard.Index != index || rep.Shard.Total != total {
			t.Fatalf("shard %d of %d reports %+v", index, total, rep.Shard)
		}
		if rep.Selection.Mode != report.ModeShard {
			t.Errorf("shard %d reports selection.mode %q", index, rep.Selection.Mode)
		}
		if rep.Selection.ChangedRef != nil {
			t.Errorf("shard %d reports a changed ref: %v", index, rep.Selection.ChangedRef)
		}
		pieces = append(pieces, rep)
	}

	// Each shard executed its own share and nothing else, which is what makes
	// the split worth doing at all.
	executed := make(map[string]int)
	for _, piece := range pieces {
		for _, m := range piece.Mutants {
			if m.Outcome == report.OutcomeNotRun && m.NotRunReason != nil &&
				*m.NotRunReason == string(report.NotRunOtherShard) {
				continue
			}
			executed[m.ID]++
			if !piece.Shard.Owns(m.ID) {
				t.Errorf("shard %d measured mutant %s, which belongs to shard %d",
					piece.Shard.Index, m.DisplayID, mutation.ShardIndex(m.ID, total))
			}
		}
	}
	if len(executed) != len(whole.Report.Mutants) {
		t.Errorf("the shards between them claimed %d of the %d mutants", len(executed), len(whole.Report.Mutants))
	}
	for id, count := range executed {
		if count != 1 {
			t.Errorf("mutant %s was measured by %d shards", id[:8], count)
		}
	}

	merged, err := report.MergeShards(report.MergeOptions{
		RunID:  NewRunID(whole.Started),
		Shards: pieces,
	})
	if err != nil {
		t.Fatalf("MergeShards: %v", err)
	}

	outcomes := func(r *report.Report) []string {
		rows := make([]string, 0, len(r.Mutants))
		for _, m := range r.Mutants {
			rows = append(rows, m.ID+" "+string(m.Outcome))
		}
		return rows
	}
	if got, want := outcomes(merged), outcomes(whole.Report); !slices.Equal(got, want) {
		t.Errorf("the merged run disagrees with the unsharded one:\n got %v\nwant %v", got, want)
	}
	// go-cmp rather than ==, because the summary holds a *float64: two equal
	// scores in two runs are two pointers, and comparing the structs directly
	// would compare the addresses.
	if diff := cmp.Diff(whole.Report.Summary, merged.Summary); diff != "" {
		t.Errorf("the merged summary is not the unsharded one (-whole +merged):\n%s", diff)
	}
	if merged.Shard != nil || merged.Merge == nil || merged.Merge.Shards != total {
		t.Errorf("the merged document reports shard %+v and merge %+v", merged.Shard, merged.Merge)
	}
	if merged.Selection.Mode != report.ModeAll {
		t.Errorf("the merged document reports selection.mode %q, want %q", merged.Selection.Mode, report.ModeAll)
	}
	if err := schemas.Validate(schemas.RunReportV1, mustMarshalReport(t, merged)); err != nil {
		t.Errorf("the merged report does not satisfy the schema: %v", err)
	}
}

// mustMarshalReport encodes a report the way it goes on disk.
func mustMarshalReport(t *testing.T, r *report.Report) []byte {
	t.Helper()
	data, err := r.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return data
}
