// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package engine

import (
	"path/filepath"
	"testing"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
	"github.com/P4suta/go-mutants/internal/testkit"
)

func cacheOptions(t *testing.T, root, cacheRoot string) Options {
	t.Helper()
	opts := optionsAt(t, root)
	opts.Config.Execution.Jobs = 2
	opts.Config.Cache.Mode = config.CacheOn
	opts.CacheRoot = cacheRoot
	return opts
}

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

func cachedRows(r *report.Report) map[string]report.Outcome {
	out := make(map[string]report.Outcome, len(r.Mutants))
	for _, m := range r.Mutants {
		if m.Cached {
			out[m.ID] = m.Outcome
		}
	}
	return out
}

func TestTheSecondRunOfAnUnchangedWorkspaceExecutesNothing(t *testing.T) {
	t.Parallel()
	root := testkit.Copy(t, "killable")
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
	third := runCached(t, cacheOptions(t, root, cacheRoot))
	if third.Cache.Hits != second.Cache.Hits {
		t.Errorf("the third run had %d hits and the second had %d", third.Cache.Hits, second.Cache.Hits)
	}
}

func TestAnEditedSourceFileIsAllMisses(t *testing.T) {
	t.Parallel()
	root := testkit.Copy(t, "killable")
	cacheRoot := t.TempDir()

	first := runCached(t, cacheOptions(t, root, cacheRoot))
	if first.Cache.Writes == 0 {
		t.Fatal("the first run stored nothing, so this proves nothing")
	}

	path := filepath.Join(root, "untested.go")
	source := testkit.ReadFile(t, path)
	testkit.WriteFile(t, path, append(source, "\n// One comment, and every key has moved.\n"...))
	testkit.AgeTree(t, root)

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
	testkit.WriteFile(t, path, source)
	testkit.AgeTree(t, root)
	restored := runCached(t, cacheOptions(t, root, cacheRoot))
	if restored.Cache.Hits != first.Cache.Writes {
		t.Errorf("the restored workspace had %d hits, want the %d the first run stored",
			restored.Cache.Hits, first.Cache.Writes)
	}
}

func TestAShardReusesWhatTheWholeRunProved(t *testing.T) {
	t.Parallel()
	root := testkit.Copy(t, "killable")
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

func TestTheCacheIsOffForACustomTestCommandUnderAuto(t *testing.T) {
	t.Parallel()
	root := testkit.Copy(t, "killable")
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
	survey, err := cache.Status(cacheRoot)
	if err != nil {
		t.Fatalf("surveying the cache: %v", err)
	}
	if survey.Entries() != 0 {
		t.Errorf("a stood-down cache left %d outcomes behind", survey.Entries())
	}
}
