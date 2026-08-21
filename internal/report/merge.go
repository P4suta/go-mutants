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

// ParseShard reads a `--shard K/N` specification.
//
// Both numbers are required and K is 1-based, which is how a CI matrix counts:
// "shard 1 of 3" is what a job is called, and a zero-based flag would make every
// pipeline configuration one off-by-one away from silently running two copies of
// one shard and none of another.
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

// invalidShardSpec builds the refusal for a `--shard` value, quoting what was
// written and saying what was expected.
func invalidShardSpec(spec, why string) error {
	return &Error{
		Code:    CodeInvalidShardSpec,
		Message: strconv.Quote(spec) + " is not a shard specification: " + why,
	}
}

// Parse reads a run report back from the bytes of a document.
//
// The decoding is strict: a field this build does not declare is a failure
// rather than a silently dropped value. That is the same discipline the schema
// states with `additionalProperties: false`, and it matters most for the one
// thing this function exists for — `report merge`, where a field quietly
// discarded on the way in would be a field missing from the merged document
// with nothing to say it had ever been there.
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
	// Exactly one document, not one followed by whatever else is in the file.
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

// MergeOptions is everything [MergeShards] needs.
type MergeOptions struct {
	// RunID identifies the merged document. It is minted by the caller rather
	// than here: a run id is a run's identity, and this package files documents
	// rather than starting runs.
	RunID string
	// Shards are the documents to merge, in the order the user named them —
	// which is the order a discrepancy is reported in, so that "the first
	// discrepancy" means the first one they would look for.
	Shards []*Report
}

// MergeShards combines the shard reports of one split run into the document that run
// would have produced unsharded.
//
// Nothing is combined until everything has been proven congruent, because a
// merged document is exactly the kind of artefact nobody re-derives: it is what
// a CI job publishes and what a score gate reads, and a merge of two runs over
// different code, or of three shards out of four, would produce numbers that
// describe no run that ever happened. So the refusals come first and they are
// total — one tool version, one workspace, one catalogue, one changed ref, one
// shard total, every index exactly once, and every row assigned to the shard
// that the assignment function says owns it.
//
// What is *not* checked is as deliberate. Baseline timings, the derived
// timeout, and the worker count legitimately differ between machines, and two
// runners disagreeing about how long the tests take is not a reason to refuse a
// merge; those fields are taken from the first shard and this document says so
// rather than pretending they were one measurement. The platform is taken the
// same way, and is safe to: a genuinely different build would have produced a
// different catalogue, which is checked.
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
		mutants = append(mutants, owners[mutation.ShardIndex(m.ID, total)].Mutants[i])
	}

	// The ledger is judged again, against the whole run this time. In a shard's
	// own report every expectation about another shard's mutant is unfulfilled,
	// because that shard did not measure it; re-evaluating here is what turns
	// those back into the verdicts the unsharded run would have reached.
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
		Workspace:     first.Workspace,
		Selection:     mergedSelection(opts.Shards),
		Shard:         nil,
		Merge:         &Merge{Shards: total},
		Test:          first.Test,
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
	// The same counting the shards' own summaries used, asked about the merged
	// rows. The policy is the first shard's: every shard obeyed one
	// configuration, and a merge of runs that did not is a mismatch nobody
	// asked this function to referee.
	merged.Summary = summaryOf(tally, policyOf(first.Summary.Policy), false, expectations, mutants)
	return merged, nil
}

// congruent proves that every document describes the same run of the same code,
// naming the first field that does not match.
//
// The catalogue comparison is by ordered id rather than by set, and the strength
// is deliberate: catalogue order is a pure function of the candidates, so two
// shards that read the same tree produce the same order, and comparing
// positionally is what lets the merge take one row from each shard by index
// without a second lookup. A difference in order is a difference in what was
// discovered, whatever the sets say.
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

// A comparison is one field of two documents that has to match.
type comparison struct {
	what        string
	first, next string
}

// firstMismatch names the first comparison that differs, or "" when none does.
func firstMismatch(comparisons []comparison) string {
	for _, c := range comparisons {
		if c.first != c.next {
			return "the " + c.what + " is " + strconv.Quote(c.next) + " rather than " + strconv.Quote(c.first)
		}
	}
	return ""
}

// sameCatalog proves two shards discovered and validated the same mutants, in
// the same order.
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

// completeSet indexes the shards by their own index, refusing a set that is not
// every shard exactly once.
//
// A missing shard is the dangerous one, and it is why this is a refusal rather
// than a warning: its mutants are not-run in every document that is present, so
// a merge would report them as never measured, take them out of the score's
// denominator, and publish a flattering number that nothing in the file says is
// incomplete.
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

// ownership proves every row was written by the shard the assignment function
// says owns it.
//
// Checking the assignment rather than merely checking that the executed sets do
// not overlap is what catches the interesting failure: a shard that ran the
// wrong subset — a stale `--shard` value in one CI job, a mutant whose id
// changed between two runs — leaves gaps as well as overlaps, and a
// disjointness test alone would accept a merge that had measured nothing at all
// twice over.
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

// mergedSelection is the selection block of the run as a whole.
//
// The mode is what the unsharded run would have said: `all` when nothing else
// narrowed it, and `changed` when the shards were diff runs — the shard is gone
// from a merged document, so claiming `shard` would describe a split that this
// document no longer represents. `selected` is summed, because each shard set
// out to execute its own share and the run set out to execute all of them.
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

// mergedStatus is the worst thing that happened to any shard.
//
// A merge of a completed shard and a failed one is a failed run: the mutants of
// the failed shard were not measured, and a document saying `completed` would
// invite a reader to trust a score that is missing a quarter of its
// denominator.
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

// span is the envelope of the shards' clocks: the earliest start and the latest
// finish.
//
// It is an envelope rather than a sum because the shards ran at the same time,
// on different machines, and each read its own clock. The duration is therefore
// how long the split run took in wall-clock terms if they were started together
// — which is the number a person wants — and not how much computer time it
// consumed.
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

// parseTimestamp reads one of a document's own timestamps.
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

// mergedCoverage is the coverage block of the run as a whole.
//
// A shard whose coverage pass failed open reports `off` while its neighbours
// report `package`, and the merged document has to be able to hold both: the
// rows from the `package` shards carry `uncovered`, which only a `package`
// document may state. So the mode is `package` whenever any shard managed it,
// the binary count is the largest any shard profiled — they profiled the same
// set — and `mutants_uncovered` is recounted from the merged rows by the same
// code that counts it for a run.
func mergedCoverage(shards []*Report, mutants []Mutant) (Coverage, error) {
	mode := CoverageOff
	binaries := 0
	for _, shard := range shards {
		if shard.Coverage.Mode == CoveragePackage {
			mode = CoveragePackage
		}
		if shard.Coverage.Binaries != nil {
			binaries = max(binaries, *shard.Coverage.Binaries)
		}
	}
	return coverageBlock(mode, binaries, mutants)
}

// mergedCache is the cache block of the run as a whole.
//
// The counters add up because the shards partition the work: each looked up
// only the mutants it owned, so no mutant was counted twice and every miss and
// every write belongs to exactly one shard. The hits are recounted from the
// merged rows by the same code that counts them for a run rather than summed,
// which is the same argument `mutants_uncovered` gets above.
//
// The mode is `on` whenever any shard managed it, for the reason
// [mergedCoverage] takes `package`: the merged rows include that shard's cached
// mutants, and only an `on` document may carry one. A matrix where one runner
// ran with `--cache off` is not an incongruent run — the cache changes nothing
// about what a mutant's outcome is — so it is deliberately not among the fields
// [congruent] compares.
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

// mergedWarnings collects what the shards published, in shard order, keeping
// one copy of anything they all said.
//
// Every shard warns about the same custom test command or the same unremovable
// snapshot, and four copies of one sentence is noise. Two warnings that differ
// by a single character are two facts and both are kept: the deduplication is
// on the whole pair, never on the code alone.
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

// ledgerOf recovers the expectations ledger from a document, so that it can be
// judged again against the merged run.
//
// The rows are the ledger: `[[mutation.expect]]` is an id and a reason, and
// that is exactly what the document keeps. Re-deriving it here rather than
// asking the caller for the configuration is what makes `report merge` work on
// documents alone — the run that wrote them may have been on another machine.
func ledgerOf(expectations []Expectation) []config.Expectation {
	ledger := make([]config.Expectation, 0, len(expectations))
	for _, e := range expectations {
		ledger = append(ledger, config.Expectation{ID: e.ID, Reason: e.Reason})
	}
	return ledger
}

// policyOf recovers the gating configuration from a document's summary.
func policyOf(result PolicyResult) mutation.Policy {
	return mutation.Policy{
		Strict:         result.Strict,
		MinimumScore:   result.MinimumScore,
		RequireMutants: result.RequireMutants,
	}
}

// refText renders a changed ref for a comparison, so that "no ref" and a ref
// that happens to be empty compare as the same thing they are.
func refText(ref *string) string {
	if ref == nil {
		return ""
	}
	return *ref
}

// shardName renders a shard for a message: "2 of 4 (run 20260218T091500Z-3f9c)".
func shardName(r *Report) string {
	if r.Shard == nil {
		return "run " + r.RunID
	}
	return fmt.Sprintf("%d of %d (run %s)", r.Shard.Index, r.Shard.Total, r.RunID)
}

// ordinal renders 1 as "first", and anything past the fifth as a number.
func ordinal(n int) string {
	names := []string{"first", "second", "third", "fourth", "fifth"}
	if n >= 1 && n <= len(names) {
		return names[n-1]
	}
	return strconv.Itoa(n) + "th"
}

// plural picks the verb form for a count.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
