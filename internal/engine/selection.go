// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/P4suta/go-mutants/internal/coverage"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gitdiff"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

// The selection stage, which is the one part of a run that decides what *not*
// to do.
//
// Everything here narrows execution and nothing narrows discovery. That is the
// property the whole feature rests on: `--changed`, `--shard` and `--mutant`
// all leave the catalogue, the validation and the rejections identical to a
// full run's, so the ids in a narrowed report are the ids a whole run would
// have minted. Two shards can therefore be merged, a changed run can be
// compared against the run before it, and an id copied out of any of them can
// be handed straight back to `--mutant`.
//
// Narrowing discovery instead would be faster and wrong. Mutant ids are minted
// from the file's own bytes and the catalogue is deduplicated across the
// module, so a discovery pass that skipped unchanged files would produce a
// different catalogue — and the shard a mutant lands in, which is a function of
// its id, would move as a side effect of somebody editing an unrelated file.

// selectionMode is how the report describes what this run set out to execute.
//
// The order is a precedence and not a preference. A shard says so first,
// because a shard is the outer partition and `report merge` reads the mode to
// decide what it is merging; a changed run says so next, because the ref it
// diffed against is the interesting fact about it; `--mutant` last of the three.
// Nothing is lost by the ordering: `changed_ref` and the `shard` block are
// recorded independently of the mode, so a shard of a changed run states both.
func selectionMode(opts Options) report.SelectionMode {
	switch {
	case opts.Shard.Total > 0:
		return report.ModeShard
	case opts.Changed:
		return report.ModeChanged
	case opts.MutantPrefix != "":
		return report.ModeMutant
	default:
		return report.ModeAll
	}
}

// shardOf renders the shard this run is, or nil for a run that was not split.
//
// The assignment function is stamped here rather than taken from the caller:
// there is exactly one, and a caller repeating it would be a second place for
// it to be wrong.
func shardOf(opts Options) *report.Shard {
	if opts.Shard.Total <= 0 {
		return nil
	}
	shard := opts.Shard
	shard.Assignment = mutation.ShardAssignment
	return &shard
}

// changedLines resolves the changed-line set for a `--changed` run, and returns
// nil for every other run.
//
// It reads the user's own workspace rather than the snapshot, which is not an
// optimisation: a snapshot deliberately excludes `.git`, so there is no
// repository in it to ask.
//
// Every failure is an error rather than a warning, and that is the whole
// judgement here. Coverage-guided selection fails open — it is an optimisation,
// and a run that loses it reaches the same verdicts more slowly — but
// `--changed` is not an optimisation, it is the question the user asked. A run
// that could not read the diff and quietly measured everything would take
// twenty minutes where one was expected; one that quietly measured nothing
// would exit 0 having proved nothing at all.
func (s *session) changedLines(ctx context.Context, opts Options, root string) (*gitdiff.Changed, error) {
	if !opts.Changed {
		return nil, nil
	}
	changed, err := gitdiff.Resolve(ctx, gitdiff.Options{Root: root, Ref: opts.ChangedRef})
	if err != nil {
		return nil, err
	}
	return &changed, nil
}

// narrowSelection applies the changed-line and shard filters, in that order,
// and publishes what they decided.
//
// The order is arithmetic rather than policy — both are filters, so composing
// them commutes — and it is written this way round because it reads as the
// sentence the user wrote: of the mutants on lines I changed, the ones this
// shard owns.
func (s *session) narrowSelection(ids []string, st *state) []string {
	if st.changed == nil && st.shard == nil {
		return ids
	}
	before := len(ids)
	if st.changed != nil {
		ids = onChangedLines(ids, st)
	}
	if st.shard != nil {
		ids = ownedByShard(ids, *st.shard)
	}

	narrowed := SelectionNarrowed{Selected: len(ids), Of: before}
	if st.changed != nil {
		narrowed.ChangedRef = st.changed.Ref
		s.sayWhatTheDiffsTestsHide(st.changed)
	}
	if st.shard != nil {
		narrowed.Shard, narrowed.Shards = st.shard.Index, st.shard.Total
	}
	s.emit(narrowed)
	return ids
}

// onChangedLines keeps the mutants whose own lines the diff touched.
//
// The span is the mutant's first line through the last line its original text
// covers, which is [coverage.EndLine]'s question and is answered by the same
// function: a multi-line condition edited anywhere is an edited condition, and
// two implementations of "which lines is this mutant on" would eventually
// disagree about one.
//
// A mutant whose coordinates the display index does not have is kept rather
// than dropped. internal/engine documents that as impossible — the index is
// built from the catalogue — and the failure mode of being wrong about it
// matters: dropping it would silently take a mutant out of the run, while
// keeping it costs one extra execution.
func onChangedLines(ids []string, st *state) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		shown := st.display[id]
		if shown.Path == "" || shown.Line < 1 {
			out = append(out, id)
			continue
		}
		if st.changed.Touches(shown.Path, shown.Line, coverage.EndLine(shown.Line, shown.Original)) {
			out = append(out, id)
		}
	}
	return out
}

// sayWhatTheDiffsTestsHide states the part of a diff the narrowing cannot see.
//
// [onChangedLines] keeps the mutants whose own lines the diff touched, and a
// `_test.go` file holds none of them: internal/discover walks packages and not
// their test variants, so nothing in a test file is ever catalogued. A diff of
// tests alone therefore narrows to nothing, and without this the run reports a
// score over an empty selection as though it had looked.
//
// What a test edit changes is which mutants the suite kills. Naming those would
// need the coverage mapping, and the mapping is built from the selection this
// narrowing produces — the answer does not exist yet at the moment the question
// is asked. Widening instead of saying so is the other wrong answer: nearly
// every commit edits a test beside the code it tests, so a rule that kept every
// mutant whenever a test moved would be the flag switched off for the runs it
// was built for.
//
// So it is said once per run, whatever the selection came to. A diff that also
// edited code keeps mutants on the lines it touched, and the test it edited
// beside them can still have changed the verdict of a mutant somewhere else
// that this run will not execute. The count is not the condition; the test file
// in the diff is.
func (s *session) sayWhatTheDiffsTestsHide(changed *gitdiff.Changed) {
	tests := make([]string, 0, 4)
	for _, path := range changed.Paths() {
		if strings.HasSuffix(path, "_test.go") {
			tests = append(tests, path)
		}
	}
	if len(tests) == 0 {
		return
	}
	s.warnDetail(
		string(CodeChangedTestsUnaccounted),
		fmt.Sprintf("the diff edited %s, and a changed test moves verdicts this narrowing cannot see",
			plural(len(tests), "test file", "test files")),
		"Changed tests are not mutated, so they select no mutant of their own, and which\n"+
			"mutants their edit kills is not known until the run that executes them. These\n"+
			"were edited:\n  "+strings.Join(tests, "\n  ")+"\n"+
			"Run without --changed to measure the mutants they reach.",
	)
}

// plural renders a count with the noun that agrees with it.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ownedByShard keeps the mutants this shard is responsible for.
func ownedByShard(ids []string, shard report.Shard) []string {
	out := make([]string, 0, len(ids)/max(shard.Total, 1)+1)
	for _, id := range ids {
		if shard.Owns(id) {
			out = append(out, id)
		}
	}
	return out
}

// recordNotRun writes down why each accepted mutant the selection left out was
// not executed.
//
// The shard is asked first, and the precedence is the point rather than a
// tie-break: in a sharded run every mutant another shard owns is that shard's
// to report, whatever else would also have excluded it here, and `report merge`
// replaces exactly those rows with the owning shard's. A mutant this shard owns
// and did not run — because the diff did not touch it, or because `--mutant`
// named another — is out of *this* run's selection, and no other shard will
// report it any differently.
func recordNotRun(accepted []string, runs []execute.MutantRun, st *state) {
	selected := make(map[string]bool, len(runs))
	for _, run := range runs {
		selected[run.ID] = true
	}
	for _, id := range accepted {
		if selected[id] {
			continue
		}
		reason := report.NotRunOutOfSelection
		if st.shard != nil && !st.shard.Owns(id) {
			reason = report.NotRunOtherShard
		}
		st.notRun[id] = reason
	}
}

// notRunReason is why one mutant carries [mutation.OutcomeNotRun].
//
// A mutant the selection left out has a recorded reason; anything else was
// selected and never measured, which only an interruption can produce. Reading
// it this way round rather than recording "interrupted" per mutant is what
// makes the field total: there is no path that can leave a not-run mutant
// without a reason, which is the invariant [report.Build] refuses to write a
// document without.
func (st *state) notRunReason(id string) report.NotRunReason {
	if reason, narrowed := st.notRun[id]; narrowed {
		return reason
	}
	return report.NotRunInterrupted
}
