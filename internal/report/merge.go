// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/config"
	"github.com/P4suta/go-mutants/internal/mutation"
)

func ParseShard(spec string) (Shard, error) {
	indexText, totalText, ok := strings.Cut(strings.TrimSpace(spec), "/")
	if !ok {
		return Shard{}, invalidShardSpec(spec, "expected two numbers separated by a slash, as in 1/4")
	}
	index, indexErr := strconv.Atoi(strings.TrimSpace(indexText))
	total, totalErr := strconv.Atoi(strings.TrimSpace(totalText))
	switch {
	case indexErr != nil || totalErr != nil:
		return Shard{}, invalidShardSpec(spec, "both parts have to be whole numbers, as in 1/4")
	case total < 1:
		return Shard{}, invalidShardSpec(spec, "a run cannot be split into "+strconv.Itoa(total)+" shards")
	case index < 1 || index > total:
		return Shard{}, invalidShardSpec(spec, fmt.Sprintf(
			"the shard number is 1-based and never exceeds the total, so it has to be between 1 and %d", total))
	}
	return Shard{Index: index, Total: total, Assignment: mutation.ShardAssignment}, nil
}

func invalidShardSpec(spec, why string) error {
	return &Error{
		Code:    CodeInvalidShardSpec,
		Message: strconv.Quote(spec) + " is not a shard specification: " + why,
	}
}

func Parse(data []byte) (*Report, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var r Report
	if err := decoder.Decode(&r); err != nil {
		return nil, &Error{
			Code:    CodeMalformedDocument,
			Message: "this is not a go-mutants run report",
			Err:     err,
		}
	}
	if decoder.More() {
		return nil, &Error{
			Code:    CodeMalformedDocument,
			Message: "the file holds more than one document; a run report is a single JSON object",
		}
	}
	switch {
	case r.DocumentType != DocumentType:
		return nil, &Error{
			Code: CodeMalformedDocument,
			Message: fmt.Sprintf("this document is %q, not %q",
				r.DocumentType, DocumentType),
		}
	case r.SchemaVersion != SchemaVersion:
		return nil, &Error{
			Code: CodeMalformedDocument,
			Message: fmt.Sprintf("this document is run-report v%d and this build reads v%d",
				r.SchemaVersion, SchemaVersion),
		}
	}
	return &r, nil
}

type MergeOptions struct {
	RunID  string
	Shards []*Report
}

func MergeShards(opts MergeOptions) (*Report, error) {
	if len(opts.Shards) == 0 {
		return nil, &Error{
			Code:    CodeNoShardReports,
			Message: "there are no shard reports to merge",
		}
	}
	for i, shard := range opts.Shards {
		if shard == nil {
			return nil, &Error{
				Code:    CodeNoShardReports,
				Message: "the " + ordinal(i+1) + " report to merge is missing",
			}
		}
		if shard.Shard == nil {
			return nil, &Error{
				Code: CodeNotAShardReport,
				Message: "the " + ordinal(i+1) + " report (run " + shard.RunID +
					") was not produced by a --shard run, so there is nothing to merge it into",
			}
		}
		if shard.Merge != nil {
			return nil, &Error{
				Code: CodeNotAShardReport,
				Message: "the " + ordinal(i+1) + " report (run " + shard.RunID +
					") is itself a merge of " + strconv.Itoa(shard.Merge.Shards) + " shards; merge the shard reports, not the result",
			}
		}
	}

	first := opts.Shards[0]
	total := first.Shard.Total
	if err := congruent(opts.Shards); err != nil {
		return nil, err
	}
	owners, err := completeSet(opts.Shards, total)
	if err != nil {
		return nil, err
	}
	if err = ownership(opts.Shards); err != nil {
		return nil, err
	}

	mutants := make([]Mutant, 0, len(first.Mutants))
	for i, m := range first.Mutants {
		row := owners[mutation.ShardIndex(m.ID, total)].Mutants[i]
		row.Executions = nil
		row.PeakMemoryBytes = 0
		row.MemoryExceeded = false
		mutants = append(mutants, row)
	}

	expectations := Evaluate(ledgerOf(first.Expectations), dispositions(mutants, first.Rejected))

	started, finished, err := span(opts.Shards)
	if err != nil {
		return nil, err
	}
	coverage, err := mergedCoverage(opts.Shards, mutants)
	if err != nil {
		return nil, err
	}
	cache, err := mergedCache(opts.Shards, mutants)
	if err != nil {
		return nil, err
	}

	merged := &Report{
		DocumentType:  DocumentType,
		SchemaVersion: SchemaVersion,
		ToolVersion:   first.ToolVersion,
		RunID:         opts.RunID,
		Status:        mergedStatus(opts.Shards),
		StartedAt:     FormatTimestamp(started),
		FinishedAt:    FormatTimestamp(finished),
		DurationMS:    milliseconds(finished.Sub(started)),
		Workspace:     withoutSnapshot(first.Workspace),
		Selection:     mergedSelection(opts.Shards),
		Shard:         nil,
		Merge:         &Merge{Shards: total},
		Test:          withoutToolchain(first.Test),
		Coverage:      coverage,
		Cache:         cache,
		Mutants:       mutants,
		Rejected:      first.Rejected,
		Skips:         first.Skips,
		Expectations:  expectations,
		Warnings:      mergedWarnings(opts.Shards),
	}
	if !runIDPattern.MatchString(merged.RunID) {
		return nil, &Error{
			Code: CodeInvalidRunID,
			Message: fmt.Sprintf("%q is not a run id: expected a UTC timestamp and four hex digits, as in 20260218T091500Z-3f9c",
				merged.RunID),
		}
	}

	tally, err := merged.Tally()
	if err != nil {
		return nil, err
	}
	merged.Summary = summaryOf(tally, policyOf(first.Summary.Policy), false, expectations, mutants)
	return merged, nil
}

func withoutSnapshot(workspace Workspace) Workspace {
	workspace.Snapshot = nil
	return workspace
}

func withoutToolchain(test Test) Test {
	test.Toolchain = nil
	test.ResolvedCommand = nil
	return test
}

func congruent(shards []*Report) error {
	first := shards[0]
	for _, shard := range shards[1:] {
		mismatch := firstMismatch([]comparison{
			{"tool version", first.ToolVersion, shard.ToolVersion},
			{"workspace digest", first.Workspace.WorkspaceDigest, shard.Workspace.WorkspaceDigest},
			{"module path", first.Workspace.ModulePath, shard.Workspace.ModulePath},
			{"shard total", strconv.Itoa(first.Shard.Total), strconv.Itoa(shard.Shard.Total)},
			{"shard assignment", first.Shard.Assignment, shard.Shard.Assignment},
			{"changed ref", refText(first.Selection.ChangedRef), refText(shard.Selection.ChangedRef)},
			{"selection mode", string(first.Selection.Mode), string(shard.Selection.Mode)},
		})
		if mismatch != "" {
			return &Error{
				Code: CodeIncongruentShards,
				Message: "shard " + shardName(shard) + " does not describe the same run as shard " +
					shardName(first) + ": " + mismatch,
			}
		}
		if err := sameCatalog(first, shard); err != nil {
			return err
		}
	}
	return nil
}

type comparison struct {
	what        string
	first, next string
}

func firstMismatch(comparisons []comparison) string {
	for _, c := range comparisons {
		if c.first != c.next {
			return "the " + c.what + " is " + strconv.Quote(c.next) + " rather than " + strconv.Quote(c.first)
		}
	}
	return ""
}

func sameCatalog(first, shard *Report) error {
	if len(first.Mutants) != len(shard.Mutants) || len(first.Rejected) != len(shard.Rejected) {
		return &Error{
			Code: CodeIncongruentShards,
			Message: fmt.Sprintf("shard %s catalogues %d mutants and %d rejections, and shard %s catalogues %d and %d",
				shardName(shard), len(shard.Mutants), len(shard.Rejected),
				shardName(first), len(first.Mutants), len(first.Rejected)),
		}
	}
	for i := range first.Mutants {
		if first.Mutants[i].ID != shard.Mutants[i].ID {
			return &Error{
				Code: CodeIncongruentShards,
				Message: fmt.Sprintf("shard %s has mutant %s where shard %s has %s: the two did not discover the same catalogue",
					shardName(shard), display(shard.Mutants[i].ID),
					shardName(first), display(first.Mutants[i].ID)),
			}
		}
	}
	for i := range first.Rejected {
		if first.Rejected[i].ID != shard.Rejected[i].ID {
			return &Error{
				Code: CodeIncongruentShards,
				Message: fmt.Sprintf("shard %s rejected mutant %s where shard %s rejected %s: validation did not reach the same verdicts",
					shardName(shard), display(shard.Rejected[i].ID),
					shardName(first), display(first.Rejected[i].ID)),
			}
		}
	}
	return nil
}

func completeSet(shards []*Report, total int) (map[int]*Report, error) {
	owners := make(map[int]*Report, total)
	for _, shard := range shards {
		index := shard.Shard.Index
		if index < 1 || index > total {
			return nil, &Error{
				Code: CodeIncompleteShardSet,
				Message: fmt.Sprintf("a report claims to be shard %d of %d, which is not a shard of this run",
					index, total),
			}
		}
		if previous, seen := owners[index]; seen {
			return nil, &Error{
				Code: CodeIncompleteShardSet,
				Message: fmt.Sprintf("shard %d of %d was given twice, as run %s and as run %s",
					index, total, previous.RunID, shard.RunID),
			}
		}
		owners[index] = shard
	}
	missing := make([]string, 0, total)
	for index := 1; index <= total; index++ {
		if owners[index] == nil {
			missing = append(missing, strconv.Itoa(index))
		}
	}
	if len(missing) > 0 {
		return nil, &Error{
			Code: CodeIncompleteShardSet,
			Message: fmt.Sprintf("this is not the whole run: %d of the %d shards %s missing (%s)",
				len(missing), total, plural(len(missing), "is", "are"), strings.Join(missing, ", ")),
		}
	}
	return owners, nil
}

func ownership(shards []*Report) error {
	for _, shard := range shards {
		index, total := shard.Shard.Index, shard.Shard.Total
		for _, m := range shard.Mutants {
			owned := mutation.ShardIndex(m.ID, total) == index
			disclaimed := m.Outcome == OutcomeNotRun &&
				m.NotRunReason != nil && *m.NotRunReason == string(NotRunOtherShard)
			switch {
			case owned && disclaimed:
				return &Error{
					Code: CodeShardOwnershipMismatch,
					Message: fmt.Sprintf("shard %s reports mutant %s as another shard's, but %s is the shard that owns it",
						shardName(shard), display(m.ID), shardName(shard)),
				}
			case !owned && !disclaimed:
				return &Error{
					Code: CodeShardOwnershipMismatch,
					Message: fmt.Sprintf("shard %s reports mutant %s as %s, but shard %d of %d is the one that owns it",
						shardName(shard), display(m.ID), m.Outcome,
						mutation.ShardIndex(m.ID, total), total),
				}
			}
		}
	}
	return nil
}

func mergedSelection(shards []*Report) Selection {
	selection := shards[0].Selection
	selection.Mode = ModeAll
	if selection.ChangedRef != nil {
		selection.Mode = ModeChanged
	}
	selected := 0
	for _, shard := range shards {
		selected += shard.Selection.Selected
	}
	selection.Selected = selected
	return selection
}

func mergedStatus(shards []*Report) Status {
	rank := map[Status]int{StatusCompleted: 0, StatusInterrupted: 1, StatusFailed: 2}
	worst := StatusCompleted
	for _, shard := range shards {
		if rank[shard.Status] > rank[worst] {
			worst = shard.Status
		}
	}
	return worst
}

func span(shards []*Report) (time.Time, time.Time, error) {
	var started, finished time.Time
	for _, shard := range shards {
		from, err := parseTimestamp(shard.StartedAt, "start", shard)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		to, err := parseTimestamp(shard.FinishedAt, "finish", shard)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		if started.IsZero() || from.Before(started) {
			started = from
		}
		if to.After(finished) {
			finished = to
		}
	}
	if finished.Before(started) {
		finished = started
	}
	return started, finished, nil
}

func parseTimestamp(value, which string, shard *Report) (time.Time, error) {
	moment, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, &Error{
			Code:    CodeInvalidTimestamps,
			Message: "shard " + shardName(shard) + " has no readable " + which + " time (" + strconv.Quote(value) + ")",
			Err:     err,
		}
	}
	return moment, nil
}

func mergedCoverage(shards []*Report, mutants []Mutant) (Coverage, error) {
	mode := CoverageOff
	binaries, tests := 0, 0
	for _, shard := range shards {
		if finer(shard.Coverage.Mode, mode) {
			mode = shard.Coverage.Mode
		}
		if shard.Coverage.Binaries != nil {
			binaries = max(binaries, *shard.Coverage.Binaries)
		}
		if shard.Coverage.Tests != nil {
			tests = max(tests, *shard.Coverage.Tests)
		}
	}
	return coverageBlock(mode, binaries, tests, mutants)
}

func finer(a, b CoverageMode) bool {
	return slices.Index(CoverageModes(), a) > slices.Index(CoverageModes(), b)
}

func mergedCache(shards []*Report, mutants []Mutant) (Cache, error) {
	mode := CacheOff
	misses, writes := 0, 0
	for _, shard := range shards {
		if shard.Cache.Mode == CacheOn {
			mode = CacheOn
		}
		misses += shard.Cache.Misses
		writes += shard.Cache.Writes
	}
	return cacheBlock(mode, misses, writes, mutants)
}

func mergedWarnings(shards []*Report) []Warning {
	ordered := slices.Clone(shards)
	slices.SortFunc(ordered, func(x, y *Report) int { return x.Shard.Index - y.Shard.Index })

	out := make([]Warning, 0)
	seen := make(map[Warning]bool)
	for _, shard := range ordered {
		for _, warning := range shard.Warnings {
			if seen[warning] {
				continue
			}
			seen[warning] = true
			out = append(out, warning)
		}
	}
	return out
}

func ledgerOf(expectations []Expectation) []config.Expectation {
	ledger := make([]config.Expectation, 0, len(expectations))
	for _, e := range expectations {
		ledger = append(ledger, config.Expectation{ID: e.ID, Reason: e.Reason})
	}
	return ledger
}

func policyOf(result PolicyResult) mutation.Policy {
	return mutation.Policy{
		Strict:         result.Strict,
		MinimumScore:   result.MinimumScore,
		RequireMutants: result.RequireMutants,
	}
}

func refText(ref *string) string {
	if ref == nil {
		return ""
	}
	return *ref
}

func shardName(r *Report) string {
	if r.Shard == nil {
		return "run " + r.RunID
	}
	return fmt.Sprintf("%d of %d (run %s)", r.Shard.Index, r.Shard.Total, r.RunID)
}

func ordinal(n int) string {
	names := []string{"first", "second", "third", "fourth", "fifth"}
	if n >= 1 && n <= len(names) {
		return names[n-1]
	}
	return strconv.Itoa(n) + "th"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
