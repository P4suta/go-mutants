// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The toolchain-backed half of the outcome cache tests. Everything interesting
// about a cache is whether the second run really finds what the first really
// wrote, through the whole pipeline: a real snapshot with a real digest, a real
// catalogue, real mutant processes, and the report the run publishes.
//
// The assertions are on the counts and the outcomes, never on the wall clock. A
// cache is faster, but "faster" on a shared CI runner is a flake; "the second
// run executed nothing and reached the same verdicts" is the property, and it
// is checkable.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/engine/...
package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
)

// cacheWorkspace copies a fixture module into a temporary directory, so that a
// test may edit it. The fixtures in the repository are read by every other
// integration test and are never written to.
func cacheWorkspace(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating the workspace: %v", err)
	}
	if err := os.CopyFS(root, os.DirFS(fixture(t, name))); err != nil {
		t.Fatalf("copying the %s fixture: %v", name, err)
	}
	return root
}

// cacheOptions is [options] against a workspace of the test's own, with the
// outcome cache pointed at a directory shared between the runs of one test and
// nowhere near the developer's own.
func cacheOptions(t *testing.T, root, cacheRoot string) Options {
	t.Helper()
	cfg := config.Defaults()
	cfg.Test.BaselineRuns = 1
	cfg.Execution.Jobs = 2
	cfg.Cache.Mode = config.CacheOn
	return Options{
		Config:        cfg,
		WorkspaceRoot: root,
		ToolVersion:   testToolVersion,
		HistoryRoot:   t.TempDir(),
		CacheRoot:     cacheRoot,
	}
}

// runCached runs the engine and returns the report, failing the test if the run
// did not complete.
func runCached(t *testing.T, opts Options) *report.Report {
	t.Helper()
	outcome, _, err := collect(t, t.Context(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Report == nil {
		t.Fatal("the run published no report")
	}
	if err = schemas.Validate(schemas.RunReportV1, mustMarshalReport(t, outcome.Report)); err != nil {
		t.Fatalf("the run report does not satisfy the schema: %v", err)
	}
	return outcome.Report
}

// reusableRows is the set of mutants a run measured and whose outcomes a later
// run may adopt: the ones the cache stores, and not the ones coverage settled
// without executing.
func reusableRows(r *report.Report) map[string]report.Outcome {
	out := make(map[string]report.Outcome, len(r.Mutants))
	for _, m := range r.Mutants {
		if m.Uncovered {
			continue
		}
		core, err := m.Outcome.Mutation()
		if err != nil {
			continue
		}
		switch core {
		case mutation.OutcomeKilled, mutation.OutcomeSurvived, mutation.OutcomeTimedOut:
			out[m.ID] = m.Outcome
		}
	}
	return out
}

// cachedRows is the set of mutants a run adopted rather than measured.
func cachedRows(r *report.Report) map[string]report.Outcome {
	out := make(map[string]report.Outcome, len(r.Mutants))
	for _, m := range r.Mutants {
		if m.Cached {
			out[m.ID] = m.Outcome
		}
	}
	return out
}

// TestTheSecondRunOfAnUnchangedWorkspaceExecutesNothing is the whole feature end
// to end.
//
// Two things are asserted and both matter. The second run's hits are exactly
// the outcomes the first run stored, which is what makes it fast; and every
// verdict is identical, which is what makes it trustworthy. A cache that were
// only fast would be worse than no cache at all.
func TestTheSecondRunOfAnUnchangedWorkspaceExecutesNothing(t *testing.T) {
	privateTempDir(t)
	root := cacheWorkspace(t, "killable")
	cacheRoot := t.TempDir()

	first := runCached(t, cacheOptions(t, root, cacheRoot))
	if first.Cache.Mode != report.CacheOn {
		t.Fatalf("the first run's cache mode is %q, want %q", first.Cache.Mode, report.CacheOn)
	}
	if first.Cache.Hits != 0 {
		t.Errorf("a cold cache had %d hits", first.Cache.Hits)
	}
	stored := reusableRows(first)
	if len(stored) == 0 {
		t.Fatal("the first run measured no reusable outcome, so this proves nothing")
	}
	if first.Cache.Writes != len(stored) {
		t.Errorf("the first run stored %d outcomes, want the %d it could", first.Cache.Writes, len(stored))
	}
	if first.Cache.Misses < first.Cache.Writes {
		t.Errorf("the first run stored %d outcomes from %d misses", first.Cache.Writes, first.Cache.Misses)
	}

	second := runCached(t, cacheOptions(t, root, cacheRoot))
	if second.Workspace.WorkspaceDigest != first.Workspace.WorkspaceDigest {
		t.Fatalf("the workspace digest moved between two runs of an unchanged tree: %s then %s",
			first.Workspace.WorkspaceDigest, second.Workspace.WorkspaceDigest)
	}
	// The derived bound is a wall-clock measurement and is allowed to move
	// between runs; the hits below are what proves that it moving does not empty
	// the cache. Both runs derive rather than being told, which is the case that
	// matters: it is the default, and it is the one a key over the effective
	// timeout would have broken.
	if second.Test.TimeoutSource != report.TimeoutDerived {
		t.Fatalf("the fixture stopped deriving its timeout, so this no longer tests the interesting case")
	}
	if got, want := second.Cache.Hits, first.Cache.Writes; got != want {
		t.Errorf("the second run had %d hits, want the %d outcomes the first stored", got, want)
	}
	adopted := cachedRows(second)
	if len(adopted) != second.Cache.Hits {
		t.Errorf("cache.hits is %d and %d rows are marked cached", second.Cache.Hits, len(adopted))
	}
	for id, outcome := range stored {
		got, hit := adopted[id]
		if !hit {
			t.Errorf("mutant %s was measured again, though its %s outcome was stored", id[:8], outcome)
			continue
		}
		if got != outcome {
			t.Errorf("mutant %s came back as %s, want the stored %s", id[:8], got, outcome)
		}
	}

	// The verdicts are identical, row for row: a cached run and a measured run
	// are the same measurement.
	if len(first.Mutants) != len(second.Mutants) {
		t.Fatalf("the two runs catalogued %d and %d mutants", len(first.Mutants), len(second.Mutants))
	}
	for i, want := range first.Mutants {
		if got := second.Mutants[i]; got.ID != want.ID || got.Outcome != want.Outcome {
			t.Errorf("mutant %d is %s %s in the second run and %s %s in the first",
				i, got.DisplayID, got.Outcome, want.DisplayID, want.Outcome)
		}
	}
	if first.Summary.Killed != second.Summary.Killed || first.Summary.Survived != second.Summary.Survived {
		t.Errorf("the summaries disagree: %+v against %+v", second.Summary, first.Summary)
	}
	// And the second run really did keep the cache warm rather than emptying
	// it: everything it adopted, it left in place.
	third := runCached(t, cacheOptions(t, root, cacheRoot))
	if third.Cache.Hits != second.Cache.Hits {
		t.Errorf("the third run had %d hits and the second had %d", third.Cache.Hits, second.Cache.Hits)
	}
}

// TestAnEditedSourceFileIsAllMisses is the key doing its job through the whole
// pipeline: the snapshot digest is over every byte of the workspace, so one
// added comment moves it, and nothing the previous run proved is reachable.
//
// A comment is the edit on purpose. It changes no behaviour and no outcome, and
// a cache that keyed on anything less than the whole tree would be tempted to
// reuse across it — which is exactly the temptation that produces a wrong
// answer the first time somebody edits a line the key was not watching.
func TestAnEditedSourceFileIsAllMisses(t *testing.T) {
	privateTempDir(t)
	root := cacheWorkspace(t, "killable")
	cacheRoot := t.TempDir()

	first := runCached(t, cacheOptions(t, root, cacheRoot))
	if first.Cache.Writes == 0 {
		t.Fatal("the first run stored nothing, so this proves nothing")
	}

	path := filepath.Join(root, "untested.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if err = os.WriteFile(path, append(source, "\n// One comment, and every key has moved.\n"...), 0o644); err != nil {
		t.Fatalf("editing %s: %v", path, err)
	}

	second := runCached(t, cacheOptions(t, root, cacheRoot))
	if second.Workspace.WorkspaceDigest == first.Workspace.WorkspaceDigest {
		t.Fatal("editing a source file did not change the workspace digest")
	}
	if second.Cache.Hits != 0 {
		t.Errorf("an edited workspace had %d hits", second.Cache.Hits)
	}
	if len(cachedRows(second)) != 0 {
		t.Error("an edited workspace adopted an outcome")
	}
	if second.Cache.Misses != first.Cache.Misses {
		t.Errorf("the edited run looked up %d mutants and the first looked up %d",
			second.Cache.Misses, first.Cache.Misses)
	}
	// The old entries are not wrong, only unreachable — which is what makes the
	// cache need no invalidation pass at all. Coming back to the original
	// content finds them again.
	if err = os.WriteFile(path, source, 0o644); err != nil {
		t.Fatalf("restoring %s: %v", path, err)
	}
	restored := runCached(t, cacheOptions(t, root, cacheRoot))
	if restored.Cache.Hits != first.Cache.Writes {
		t.Errorf("the restored workspace had %d hits, want the %d the first run stored",
			restored.Cache.Hits, first.Cache.Writes)
	}
}

// TestAShardReusesWhatTheWholeRunProved is the property a CI matrix needs.
//
// Every shard of one workspace computes the same context — the shard index is
// not in the key, and must not be, because a mutant's outcome does not depend
// on which runner measured it — so a shard run after a whole run finds exactly
// its own share already answered.
func TestAShardReusesWhatTheWholeRunProved(t *testing.T) {
	privateTempDir(t)
	root := cacheWorkspace(t, "killable")
	cacheRoot := t.TempDir()

	whole := runCached(t, cacheOptions(t, root, cacheRoot))
	stored := reusableRows(whole)
	if len(stored) == 0 {
		t.Fatal("the whole run stored nothing, so this proves nothing")
	}

	const total = 2
	seen := 0
	for index := 1; index <= total; index++ {
		opts := cacheOptions(t, root, cacheRoot)
		opts.Shard = report.Shard{Index: index, Total: total}
		shard := runCached(t, opts)

		want := make(map[string]report.Outcome)
		for id, outcome := range stored {
			if mutation.ShardIndex(id, total) == index {
				want[id] = outcome
			}
		}
		got := cachedRows(shard)
		if len(got) != len(want) {
			t.Errorf("shard %d of %d adopted %d outcomes, want the %d it owns",
				index, total, len(got), len(want))
		}
		for id, outcome := range want {
			if got[id] != outcome {
				t.Errorf("shard %d of %d has mutant %s as %q, want the whole run's %s",
					index, total, id[:8], got[id], outcome)
			}
		}
		if shard.Cache.Hits != len(got) {
			t.Errorf("shard %d reports %d hits and %d cached rows", index, shard.Cache.Hits, len(got))
		}
		// Every mutant it owns was already answered, so it measured nothing at
		// all — which is the whole point of a warm cache in a matrix.
		if shard.Cache.Misses != 0 {
			t.Errorf("shard %d of %d looked up %d mutants it did not already have",
				index, total, shard.Cache.Misses)
		}
		seen += len(got)
	}
	if seen != len(stored) {
		t.Errorf("the shards between them adopted %d outcomes, want the %d the whole run stored", seen, len(stored))
	}
}

// TestTheCacheIsOffForACustomTestCommandUnderAuto is the mode matrix where it
// meets a real toolchain: `auto` stands down, says so with GOM7901, and the run
// reaches the same verdicts by measuring everything.
func TestTheCacheIsOffForACustomTestCommandUnderAuto(t *testing.T) {
	privateTempDir(t)
	root := cacheWorkspace(t, "killable")
	cacheRoot := t.TempDir()

	opts := cacheOptions(t, root, cacheRoot)
	opts.Config.Cache.Mode = config.CacheAuto
	opts.TestArgv = []string{"go", "test", "-count=1", "./..."}
	rep := runCached(t, opts)

	if rep.Cache.Mode != report.CacheOff {
		t.Errorf("cache.mode = %q, want %q", rep.Cache.Mode, report.CacheOff)
	}
	if rep.Cache.Hits != 0 || rep.Cache.Misses != 0 || rep.Cache.Writes != 0 {
		t.Errorf("a stood-down cache reported %+v", rep.Cache)
	}
	found := false
	for _, warning := range rep.Warnings {
		if warning.Code == "GOM7901" {
			found = true
		}
	}
	if !found {
		t.Errorf("the run did not say why the cache was off: %+v", rep.Warnings)
	}
	// Nothing was written either, so a later `auto` run over the same workspace
	// still has nothing to adopt.
	survey, err := cache.Status(cacheRoot)
	if err != nil {
		t.Fatalf("surveying the cache: %v", err)
	}
	if survey.Entries() != 0 {
		t.Errorf("a stood-down cache left %d outcomes behind", survey.Entries())
	}
}
