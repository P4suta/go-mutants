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

// TestAChangedTestFileIsNotSilentlyNarrowedAway is the third outcome of a
// narrowing, and the reason it has to exist is that the other two are both
// wrong here.
//
// `--changed` keeps the mutants the diff touched, and a `_test.go` file holds
// none: internal/discover never mutates one. So a diff that edited only tests
// touches no mutant, the selection comes back empty, and the run publishes a
// score over nothing as though it had looked — which is the same fiction
// [scopedBinaries] refuses at the pattern, arriving by a different road.
//
// Keeping every mutant instead is the other wrong answer: almost every real
// commit edits a test beside the code it tests, so that rule would turn the
// flag off for the runs it was built for.
//
// What a test edit changes is which mutants the suite kills, and nothing at
// this point in the run knows which those are — the coverage mapping that could
// say is built from the selection this function produces. So the run says the
// narrowing could not see them rather than answering as if it had.
func TestAChangedTestFileIsNotSilentlyNarrowedAway(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 8)
	s := &session{events: events}
	st := &state{
		changed: &gitdiff.Changed{
			Ref: "origin/main",
			Files: map[string][]gitdiff.Range{
				"internal/thing/thing_test.go": {{First: 1, Last: 40}},
			},
		},
		display: map[string]MutantResult{},
	}
	for _, id := range ids {
		st.display[id] = MutantResult{Path: "internal/thing/thing.go", Line: 5, Original: "=="}
	}

	if got := s.narrowSelection(slices.Clone(ids), st); len(got) != 0 {
		t.Fatalf("the diff touched no mutant line, so the selection is %d, not %d", 0, len(got))
	}
	close(events)

	var warned Warning
	for e := range events {
		if w, ok := e.(Warning); ok && w.Code == string(CodeChangedTestsUnaccounted) {
			warned = w
		}
	}
	if warned.Code == "" {
		t.Fatal("a diff that changed only tests narrowed to nothing without saying so")
	}
	if !strings.Contains(warned.Detail, "internal/thing/thing_test.go") {
		t.Errorf("the warning names the test files it could not account for, and said %q", warned.Detail)
	}
}

// TestADiffOfCodeAloneSaysNothingAboutTests is the counterpart that proves the
// warning above is a warning and not a banner.
//
// A gate only ever observed firing is a gate whose silence nobody has checked:
// one that fired on every `--changed` run would pass the test above and say
// nothing true. So a diff that edited no test file has to narrow without a
// word, and it is the test file in the diff — never the size of the selection —
// that decides which happens.
func TestADiffOfCodeAloneSaysNothingAboutTests(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 8)
	s := &session{events: events}
	st := &state{
		changed: &gitdiff.Changed{
			Ref:   "origin/main",
			Files: map[string][]gitdiff.Range{"internal/thing/thing.go": {{First: 900, Last: 901}}},
		},
		display: map[string]MutantResult{},
	}
	for _, id := range ids {
		st.display[id] = MutantResult{Path: "internal/thing/thing.go", Line: 5, Original: "=="}
	}

	if got := s.narrowSelection(slices.Clone(ids), st); len(got) != 0 {
		t.Fatalf("the diff touched line 900 and every mutant is on line 5, so %d were kept", len(got))
	}
	close(events)
	for e := range events {
		if w, ok := e.(Warning); ok && w.Code == string(CodeChangedTestsUnaccounted) {
			t.Error("a diff that edited no test file was told its tests were unaccounted for")
		}
	}
}

// TestAChangedTestIsUnaccountedForEvenWhenMutantsWereKept pins the condition
// the warning is really about.
//
// The tempting rule is "warn when the selection came back empty", and it is
// wrong in the direction that costs a finding: a commit that edits `a.go` and
// `b_test.go` keeps the mutants on the lines of `a.go` — a selection nobody
// would call suspicious — while the edit to `b_test.go` can have changed the
// verdict of a mutant in `b.go` that this run does not execute at all.
func TestAChangedTestIsUnaccountedForEvenWhenMutantsWereKept(t *testing.T) {
	t.Parallel()

	events := make(chan Event, 8)
	s := &session{events: events}
	st := &state{
		changed: &gitdiff.Changed{
			Ref: "origin/main",
			Files: map[string][]gitdiff.Range{
				"internal/thing/a.go":      {{First: 1, Last: 100}},
				"internal/other/b_test.go": {{First: 1, Last: 40}},
			},
		},
		display: map[string]MutantResult{},
	}
	for _, id := range ids {
		st.display[id] = MutantResult{Path: "internal/thing/a.go", Line: 5, Original: "=="}
	}

	if got := s.narrowSelection(slices.Clone(ids), st); len(got) != len(ids) {
		t.Fatalf("the diff covers every mutant's line, so %d of %d were kept", len(got), len(ids))
	}
	close(events)

	said := false
	for e := range events {
		if w, ok := e.(Warning); ok && w.Code == string(CodeChangedTestsUnaccounted) {
			said = true
			if strings.Contains(w.Detail, "internal/thing/a.go") {
				t.Errorf("the warning listed a file that is not a test: %q", w.Detail)
			}
		}
	}
	if !said {
		t.Error("a full selection beside a changed test reported nothing unaccounted for")
	}
}
