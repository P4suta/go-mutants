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

func shardOf(opts Options) *report.Shard {
	if opts.Shard.Total <= 0 {
		return nil
	}
	shard := opts.Shard
	shard.Assignment = mutation.ShardAssignment
	return &shard
}

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

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func ownedByShard(ids []string, shard report.Shard) []string {
	out := make([]string, 0, len(ids)/max(shard.Total, 1)+1)
	for _, id := range ids {
		if shard.Owns(id) {
			out = append(out, id)
		}
	}
	return out
}

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

func (st *state) notRunReason(id string) report.NotRunReason {
	if reason, narrowed := st.notRun[id]; narrowed {
		return reason
	}
	return report.NotRunInterrupted
}
