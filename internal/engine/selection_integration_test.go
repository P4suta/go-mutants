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
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/testkit"
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

// gitRepo makes the workspace a repository holding the fixture in one commit,
// under an environment the run's own git reads the same way, and returns that
// commit.
//
// The redirection is of the whole process rather than of a set composed for the
// test's own commands, and it is the reason the three `--changed` tests do not
// run in parallel where the rest of this file does. internal/engine resolves
// the diff through internal/gitdiff without naming an environment, so the git
// the run drives is this process's git with this process's variables: a
// developer's ~/.gitconfig that a test neutralised only for its own commands
// would still be read by the code under test, and `diff.noprefix` alone changes
// what the engine has to parse.
//
// It is [testkit.Env]'s whole policy rather than the two configuration
// variables strictly needed here, because a second, narrower spelling of "a
// hermetic environment" in this package is how the copies came to disagree in
// the first place.
func gitRepo(t *testing.T, root string) string {
	t.Helper()
	testkit.Env(t)
	return testkit.GitInit(t, root)
}

// ageWrites puts files just written into a tree, and the directory holding
// them, an hour into the past.
//
// It is [testkit.AgeTree] narrowed to what was written, and the narrowing is the
// whole reason it exists here: this tree is a git repository, and ageing all of
// it would reach into `.git`, where the index git decides what is dirty from is
// a table of the stat data of every file. Rewriting timestamps underneath that
// to answer a question the go command asks is no way to ask git a question.
//
// The directory is aged with the files because cmd/go indexes a package
// directory only when everything it reads there is at least two seconds old, and
// writing a file into a directory stamps the directory too.
func ageWrites(t *testing.T, dir string, names ...string) {
	t.Helper()
	when := time.Now().Add(-testkit.TreeAge)
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatalf("ageing %s: %v", path, err)
		}
	}
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatalf("ageing %s: %v", dir, err)
	}
}

// changedOptions is [optionsAt] for a `--changed` run: two workers, because
// these tests assert on the document rather than on the order of the events,
// and a diff against the commit the workspace started from.
func changedOptions(t *testing.T, root, base string) Options {
	t.Helper()
	opts := optionsAt(t, root)
	opts.Config.Execution.Jobs = 2
	opts.Changed = true
	opts.ChangedRef = base
	return opts
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
	root := testkit.Copy(t, "families")
	base := gitRepo(t, root)

	testkit.WriteFile(t, filepath.Join(root, touchedFile), []byte(touchedSource))
	testkit.WriteFile(t, filepath.Join(root, touchedTest), []byte(touchedTestSource))
	ageWrites(t, root, touchedFile, touchedTest)
	testkit.GitCommit(t, root, "the work this run is about")

	outcome, _, err := collect(t, t.Context(), changedOptions(t, root, base))
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
	root := testkit.Copy(t, "killable")
	base := gitRepo(t, root)

	outcome, _, err := collect(t, t.Context(), changedOptions(t, root, base))
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
	// The same hermetic environment the two repositories above are scripted in,
	// for the same reason: the engine's git is this process's git. The workspace
	// is a copy under the test's own directory and no repository is made in it,
	// which is the whole of the arrangement — the corpus module itself lives
	// inside go-mutants' repository and would resolve a diff.
	testkit.Env(t)
	opts := options(t, "killable")
	opts.Changed = true
	opts.ChangedRef = "HEAD"

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
	t.Parallel()

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
