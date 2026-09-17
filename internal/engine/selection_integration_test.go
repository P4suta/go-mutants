// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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

func gitRepo(t *testing.T, root string) string {
	t.Helper()
	testkit.Env(t)
	return testkit.GitInit(t, root)
}

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

func changedOptions(t *testing.T, root, base string) Options {
	t.Helper()
	opts := optionsAt(t, root)
	opts.Config.Execution.Jobs = 2
	opts.Changed = true
	opts.ChangedRef = base
	return opts
}

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
	if rep.Summary.Killed == 0 {
		t.Error("nothing in the edited file was killed, so the run measured nothing meaningful")
	}
	if err := schemas.Validate(schemas.RunReportV1, mustMarshalReport(t, rep)); err != nil {
		t.Errorf("the changed run's report does not satisfy the schema: %v", err)
	}
}

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

func TestChangedRunFailsWithoutARepository(t *testing.T) {
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
	if outcome.SnapshotRoot != "" {
		t.Errorf("the run snapshotted %s before finding out it could not resolve the diff", outcome.SnapshotRoot)
	}
}

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

func mustMarshalReport(t *testing.T, r *report.Report) []byte {
	t.Helper()
	data, err := r.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return data
}
