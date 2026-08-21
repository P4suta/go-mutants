// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gitdiff"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

// ids are four well-formed mutant ids for the selection tests. They are real
// hex of the right length because [mutation.ShardIndex] hashes them and
// [report.Shard.Owns] is asked about them.
var ids = []string{
	strings.Repeat("a", 64),
	strings.Repeat("b", 64),
	strings.Repeat("c", 64),
	strings.Repeat("d", 64),
}

func TestSelectionModeNamesTheOuterNarrowing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts Options
		want report.SelectionMode
	}{
		{"a whole run", Options{}, report.ModeAll},
		{"one mutant", Options{MutantPrefix: "abcd1234"}, report.ModeMutant},
		{"a diff", Options{Changed: true}, report.ModeChanged},
		{"a shard", Options{Shard: report.Shard{Index: 1, Total: 2}}, report.ModeShard},
		{
			// The shard is the outer partition, and the ref is not lost: it is
			// recorded in selection.changed_ref whatever the mode says.
			"a shard of a diff",
			Options{Changed: true, Shard: report.Shard{Index: 1, Total: 2}},
			report.ModeShard,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := selectionMode(c.opts); got != c.want {
				t.Errorf("selectionMode = %q, want %q", got, c.want)
			}
		})
	}
}

func TestShardOfStampsTheAssignment(t *testing.T) {
	t.Parallel()

	if shardOf(Options{}) != nil {
		t.Error("an unsharded run reports a shard")
	}
	shard := shardOf(Options{Shard: report.Shard{Index: 2, Total: 3}})
	if shard == nil {
		t.Fatal("a sharded run reports no shard")
	}
	if shard.Assignment != mutation.ShardAssignment {
		t.Errorf("assignment = %q, want %q", shard.Assignment, mutation.ShardAssignment)
	}
}

// TestOnChangedLinesKeepsWhatTheDiffTouched covers the three answers the filter
// can give: on a changed line, off one, and a mutant whose span reaches a
// changed line from an unchanged one.
func TestOnChangedLinesKeepsWhatTheDiffTouched(t *testing.T) {
	t.Parallel()

	st := &state{
		display: map[string]MutantResult{
			ids[0]: {Path: "a.go", Line: 10, Original: "=="},
			ids[1]: {Path: "a.go", Line: 40, Original: "=="},
			ids[2]: {Path: "b.go", Line: 3, Original: "=="},
			// A condition spanning lines 8 to 10, edited on its last line.
			ids[3]: {Path: "a.go", Line: 8, Original: "x &&\n\ty &&\n\tz"},
		},
		changed: &gitdiff.Changed{
			Ref:   "origin/main",
			Files: map[string][]gitdiff.Range{"a.go": {{First: 10, Last: 12}}},
		},
	}
	got := onChangedLines(slices.Clone(ids), st)
	if !slices.Equal(got, []string{ids[0], ids[3]}) {
		t.Errorf("onChangedLines kept %d mutants, want the two on a.go lines 8-12", len(got))
	}
}

// TestOnChangedLinesKeepsAMutantWithNoCoordinates pins the fail-open rule.
//
// A catalogued mutant with no coordinates is documented as impossible, and the
// cost of being wrong about it decides which way to fail: dropping it would
// silently take a mutant out of the run, and keeping it costs one execution.
func TestOnChangedLinesKeepsAMutantWithNoCoordinates(t *testing.T) {
	t.Parallel()

	st := &state{
		display: map[string]MutantResult{ids[0]: {}},
		changed: &gitdiff.Changed{Files: map[string][]gitdiff.Range{"a.go": {{First: 1, Last: 1}}}},
	}
	if got := onChangedLines([]string{ids[0]}, st); len(got) != 1 {
		t.Error("a mutant with no coordinates was dropped rather than executed")
	}
}

// TestShardsPartitionTheAcceptedSet proves the filter is a partition through
// the engine's own use of it: every accepted mutant is selected by exactly one
// shard, and every shard's selection is a subset of the whole.
func TestShardsPartitionTheAcceptedSet(t *testing.T) {
	t.Parallel()

	const total = 3
	seen := make(map[string]int, len(ids))
	for index := 1; index <= total; index++ {
		for _, id := range ownedByShard(ids, report.Shard{Index: index, Total: total}) {
			seen[id]++
		}
	}
	if len(seen) != len(ids) {
		t.Fatalf("%d of %d mutants were selected by a shard", len(seen), len(ids))
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("mutant %s was selected by %d shards", id[:8], count)
		}
	}
}

// TestNotRunReasonsAreShardFirst pins the precedence.
//
// In a sharded run every mutant another shard owns is that shard's to report,
// whatever else would also have excluded it here — that is the row `report
// merge` replaces. A mutant this shard owns and did not run is out of this
// run's selection, and no other shard will say otherwise.
func TestNotRunReasonsAreShardFirst(t *testing.T) {
	t.Parallel()

	shard := report.Shard{Index: 1, Total: 2, Assignment: mutation.ShardAssignment}
	st := &state{shard: &shard, notRun: map[string]report.NotRunReason{}}

	// Nothing was executed, so every accepted mutant gets a reason: the ones
	// this shard owns because the diff did not reach them, and the rest because
	// they belong to the other shard.
	recordNotRun(ids, nil, st)
	for _, id := range ids {
		want := report.NotRunOtherShard
		if shard.Owns(id) {
			want = report.NotRunOutOfSelection
		}
		if got := st.notRunReason(id); got != want {
			t.Errorf("mutant %s is %q, want %q", id[:8], got, want)
		}
	}
}

// TestSelectedMutantsHaveNoReason proves a mutant the run set out to execute is
// not recorded as narrowed away, so that an interruption is what a missing
// result means.
func TestSelectedMutantsHaveNoReason(t *testing.T) {
	t.Parallel()

	st := &state{notRun: map[string]report.NotRunReason{}}
	runs := []execute.MutantRun{{ID: ids[0]}, {ID: ids[1]}}
	recordNotRun(ids, runs, st)

	for _, id := range ids[:2] {
		if _, narrowed := st.notRun[id]; narrowed {
			t.Errorf("selected mutant %s was recorded as narrowed away", id[:8])
		}
		if got := st.notRunReason(id); got != report.NotRunInterrupted {
			t.Errorf("an unmeasured selected mutant is %q, want %q", got, report.NotRunInterrupted)
		}
	}
	for _, id := range ids[2:] {
		if got := st.notRunReason(id); got != report.NotRunOutOfSelection {
			t.Errorf("mutant %s is %q, want %q", id[:8], got, report.NotRunOutOfSelection)
		}
	}
}

// TestNarrowSelectionPublishesWhatItDecided proves the event carries both
// narrowings when both applied, so that a reader is never told half the reason
// a run is smaller than they expected.
func TestNarrowSelectionPublishesWhatItDecided(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 4)
	s := &session{events: events}
	shard := report.Shard{Index: 1, Total: 2, Assignment: mutation.ShardAssignment}
	st := &state{
		shard: &shard,
		changed: &gitdiff.Changed{
			Ref:   "origin/main",
			Files: map[string][]gitdiff.Range{"a.go": {{First: 1, Last: 100}}},
		},
		display: map[string]MutantResult{},
	}
	for _, id := range ids {
		st.display[id] = MutantResult{Path: "a.go", Line: 5, Original: "=="}
	}

	kept := s.narrowSelection(slices.Clone(ids), st)
	close(events)

	var narrowed SelectionNarrowed
	for e := range events {
		if n, ok := e.(SelectionNarrowed); ok {
			narrowed = n
		}
	}
	if narrowed.ChangedRef != "origin/main" {
		t.Errorf("the event names the ref %q", narrowed.ChangedRef)
	}
	if narrowed.Shard != 1 || narrowed.Shards != 2 {
		t.Errorf("the event names shard %d of %d", narrowed.Shard, narrowed.Shards)
	}
	if narrowed.Of != len(ids) || narrowed.Selected != len(kept) {
		t.Errorf("the event says %d of %d, and %d of %d were kept",
			narrowed.Selected, narrowed.Of, len(kept), len(ids))
	}
}

// TestNarrowSelectionSaysNothingWhenItNarrowedNothing proves a whole run
// publishes no selection line at all: there is nothing to explain.
func TestNarrowSelectionSaysNothingWhenItNarrowedNothing(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 4)
	s := &session{events: events}
	st := &state{display: map[string]MutantResult{}}
	if got := s.narrowSelection(ids, st); !slices.Equal(got, ids) {
		t.Error("a run that narrowed nothing lost mutants")
	}
	close(events)
	for e := range events {
		if _, ok := e.(SelectionNarrowed); ok {
			t.Error("a run that narrowed nothing published a narrowing")
		}
	}
}

// TestChangedLinesAreNotResolvedWithoutTheFlag proves a run that did not ask
// for a diff never goes looking for a repository — which is what lets
// go-mutants work in a directory that is not one.
func TestChangedLinesAreNotResolvedWithoutTheFlag(t *testing.T) {
	t.Parallel()

	s := &session{}
	changed, err := s.changedLines(t.Context(), Options{}, t.TempDir())
	if err != nil {
		t.Fatalf("changedLines: %v", err)
	}
	if changed != nil {
		t.Error("a run with no --changed resolved a diff")
	}
}
