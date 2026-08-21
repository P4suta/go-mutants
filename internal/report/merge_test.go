// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/P4suta/go-mutants/internal/cache"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/schemas"
)

// mergedRunID is the id the merged document is given. It is a constant so that
// the comparison below has exactly one field to ignore.
const mergedRunID = "20260218T092000Z-77aa"

// shardOptions turns the whole-run fixture into one shard of a split run.
//
// Everything a shard does not own becomes not-run with `other-shard`, which is
// exactly what internal/engine's selection stage produces; everything it does
// own keeps the outcome the whole-run fixture gave it. That is what makes the
// round trip below meaningful: the merged document has to reassemble the
// fixture out of pieces that were split by the real assignment function.
func shardOptions(t *testing.T, index, total int) report.Options {
	t.Helper()
	opts := fixtureOptions(t)
	opts.RunID = fmt.Sprintf("20260218T09150%dZ-3f9c", index)
	opts.Mode = report.ModeShard
	opts.Shard = &report.Shard{Index: index, Total: total}

	// The cache counters are recomputed for the share this shard owns, and that
	// is what makes summing them across the set the whole run's numbers again: a
	// shard looks up only its own mutants, so every hit, miss and write belongs
	// to exactly one shard. Handing every shard the whole run's counters would
	// make the merged document claim the work was done N times over — which is
	// exactly what this fixture used to do, and what the merge property caught.
	selected, misses, writes := 0, 0, 0
	for i, result := range opts.Results {
		if mutation.ShardIndex(result.ID, total) == index {
			selected++
			switch {
			case result.Cached:
			default:
				misses++
				if cache.Cacheable(result.Outcome) {
					writes++
				}
			}
			continue
		}
		opts.Results[i] = report.MutantResult{
			ID:           result.ID,
			Outcome:      mutation.OutcomeNotRun,
			NotRunReason: report.NotRunOtherShard,
		}
	}
	opts.Selected = selected
	opts.CacheMisses = misses
	opts.CacheWrites = writes
	return opts
}

// shards builds a complete set of shard reports.
func shards(t *testing.T, total int) []*report.Report {
	t.Helper()
	out := make([]*report.Report, 0, total)
	for index := 1; index <= total; index++ {
		r, err := report.Build(shardOptions(t, index, total))
		if err != nil {
			t.Fatalf("building shard %d of %d: %v", index, total, err)
		}
		out = append(out, r)
	}
	return out
}

// mergeShards merges a set and fails the test if it will not merge.
func mergeShards(t *testing.T, set []*report.Report) *report.Report {
	t.Helper()
	merged, err := report.MergeShards(report.MergeOptions{RunID: mergedRunID, Shards: set})
	if err != nil {
		t.Fatalf("MergeShards: %v", err)
	}
	return merged
}

// TestMergedShardsAreTheWholeRun is the property `--shard` exists to have.
//
// Splitting a run into shards and merging them back has to produce the document
// the unsharded run would have written — not merely the same score, but the same
// document: the same rows in the same order with the same outcomes, the same
// expectations ledger judged the same way, the same counts. Anything less makes
// a sharded CI job a different measurement from a local one, and then the two
// cannot be compared and nobody can tell which to believe.
//
// Exactly two fields are exempt, and each is a fact about the merge rather than
// about the run: the run id, which is the merged document's own identity, and
// `merge`, which is what marks it as merged at all. Everything else — including
// `selection.mode`, which is `shard` in the pieces and has to come back to `all`
// in the whole — is compared field for field.
func TestMergedShardsAreTheWholeRun(t *testing.T) {
	t.Parallel()

	for _, total := range []int{1, 2, 3, 8} {
		t.Run(fmt.Sprintf("%d shards", total), func(t *testing.T) {
			t.Parallel()

			whole := buildFixture(t)
			merged := mergeShards(t, shards(t, total))

			if diff := cmp.Diff(whole, merged, cmpopts.IgnoreFields(report.Report{}, "RunID", "Merge")); diff != "" {
				t.Errorf("the merge of %d shards is not the whole run (-whole +merged):\n%s", total, diff)
			}
			if merged.Merge == nil || merged.Merge.Shards != total {
				t.Errorf("merge = %+v, want %d shards", merged.Merge, total)
			}
			if merged.Shard != nil {
				t.Errorf("the merged document claims to be shard %+v", merged.Shard)
			}
			if merged.RunID != mergedRunID {
				t.Errorf("run_id = %q, want %q", merged.RunID, mergedRunID)
			}
			if err := schemas.Validate(schemas.RunReportV1, mustMarshal(t, merged)); err != nil {
				t.Errorf("the merged document does not satisfy the schema: %v", err)
			}
		})
	}
}

// TestMergingChangedShardsKeepsTheDiff is the composition case: `--shard` over a
// `--changed` run.
//
// The two narrowings compose, so the shards report `mode: "shard"` with a
// `changed_ref` alongside it, and merging has to keep the honest half. A merged
// document that said `all` would claim to have measured a catalogue that it
// deliberately did not: the not-run rows the diff excluded are still in it.
//
// It is also the one place the merged mode is *derived* rather than checked.
// [MergeShards] assembles the document directly rather than through [Build], so
// the "a changed run must name its ref" rule never runs on it, and this is what
// holds the two together instead.
func TestMergingChangedShardsKeepsTheDiff(t *testing.T) {
	t.Parallel()

	const (
		total = 2
		ref   = "origin/main"
	)
	set := make([]*report.Report, 0, total)
	for index := 1; index <= total; index++ {
		opts := shardOptions(t, index, total)
		opts.ChangedRef = ref
		r, err := report.Build(opts)
		if err != nil {
			t.Fatalf("building shard %d: %v", index, err)
		}
		if r.Selection.Mode != report.ModeShard {
			t.Fatalf("shard %d reports mode %q, want %q", index, r.Selection.Mode, report.ModeShard)
		}
		set = append(set, r)
	}

	merged := mergeShards(t, set)
	if merged.Selection.Mode != report.ModeChanged {
		t.Errorf("selection.mode = %q, want %q: the shards narrowed by a diff and the whole run did too",
			merged.Selection.Mode, report.ModeChanged)
	}
	if merged.Selection.ChangedRef == nil || *merged.Selection.ChangedRef != ref {
		t.Errorf("selection.changed_ref = %v, want %q", merged.Selection.ChangedRef, ref)
	}
	if merged.Shard != nil || merged.Merge == nil {
		t.Errorf("the merged document reports shard %+v and merge %+v", merged.Shard, merged.Merge)
	}
	if err := schemas.Validate(schemas.RunReportV1, mustMarshal(t, merged)); err != nil {
		t.Errorf("the merged document does not satisfy the schema: %v", err)
	}
}

// TestEveryMutantIsMeasuredExactlyOnce proves the split itself is a partition,
// through the documents rather than through the assignment function: every row
// of the whole run is claimed by one shard and disclaimed by all the others.
func TestEveryMutantIsMeasuredExactlyOnce(t *testing.T) {
	t.Parallel()

	const total = 3
	claims := make(map[string]int)
	for _, shard := range shards(t, total) {
		for _, m := range shard.Mutants {
			if m.NotRunReason != nil && *m.NotRunReason == string(report.NotRunOtherShard) {
				continue
			}
			claims[m.ID]++
		}
	}
	whole := buildFixture(t)
	if len(claims) != len(whole.Mutants) {
		t.Errorf("%d of the %d mutants were claimed by a shard", len(claims), len(whole.Mutants))
	}
	for id, count := range claims {
		if count != 1 {
			t.Errorf("mutant %s was claimed by %d shards", id[:8], count)
		}
	}
}

// TestMergeRefuses is the congruence table: one case per way a set of documents
// can fail to describe one run, and the code each is refused with.
//
// Every one of them is a refusal rather than a repair. A merged document is
// what a CI job publishes and what a score gate reads, so the failure mode this
// table guards against is not a crash — it is a plausible-looking document with
// numbers describing a run that never happened.
func TestMergeRefuses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// build produces the set to merge, starting from a valid pair.
		build func(t *testing.T, set []*report.Report) []*report.Report
		code  report.Code
		says  string
	}{
		{
			name:  "nothing to merge",
			build: func(*testing.T, []*report.Report) []*report.Report { return nil },
			code:  report.CodeNoShardReports,
			says:  "no shard reports",
		},
		{
			name: "a missing document",
			build: func(_ *testing.T, set []*report.Report) []*report.Report {
				return []*report.Report{set[0], nil}
			},
			code: report.CodeNoShardReports,
			says: "second report",
		},
		{
			name: "a report from an unsharded run",
			build: func(t *testing.T, set []*report.Report) []*report.Report {
				return []*report.Report{set[0], buildFixture(t)}
			},
			code: report.CodeNotAShardReport,
			says: "not produced by a --shard run",
		},
		{
			name: "a report that is itself a merge",
			build: func(t *testing.T, set []*report.Report) []*report.Report {
				merged := mergeShards(t, set)
				merged.Shard = &report.Shard{Index: 1, Total: 2, Assignment: mutation.ShardAssignment}
				return []*report.Report{set[0], merged}
			},
			code: report.CodeNotAShardReport,
			says: "is itself a merge",
		},
		{
			name: "two different builds of go-mutants",
			build: func(_ *testing.T, set []*report.Report) []*report.Report {
				set[1].ToolVersion = "0.0.0-other"
				return set
			},
			code: report.CodeIncongruentShards,
			says: "tool version",
		},
		{
			name: "two different workspaces",
			build: func(_ *testing.T, set []*report.Report) []*report.Report {
				set[1].Workspace.WorkspaceDigest = strings.Repeat("cd", 32)
				return set
			},
			code: report.CodeIncongruentShards,
			says: "workspace digest",
		},
		{
			name: "two different shard totals",
			build: func(_ *testing.T, set []*report.Report) []*report.Report {
				set[1].Shard.Total = 4
				return set
			},
			code: report.CodeIncongruentShards,
			says: "shard total",
		},
		{
			name: "two different assignment functions",
			build: func(_ *testing.T, set []*report.Report) []*report.Report {
				set[1].Shard.Assignment = "id-hash-v2"
				return set
			},
			code: report.CodeIncongruentShards,
			says: "shard assignment",
		},
		{
			name: "two different diffs",
			build: func(_ *testing.T, set []*report.Report) []*report.Report {
				ref := "origin/main"
				set[1].Selection.ChangedRef = &ref
				return set
			},
			code: report.CodeIncongruentShards,
			says: "changed ref",
		},
		{
			name: "two different catalogues",
			build: func(_ *testing.T, set []*report.Report) []*report.Report {
				set[1].Mutants[0].ID = strings.Repeat("9", 64)
				return set
			},
			code: report.CodeIncongruentShards,
			says: "did not discover the same catalogue",
		},
		{
			name: "two different verdicts from validation",
			build: func(_ *testing.T, set []*report.Report) []*report.Report {
				set[1].Rejected[0].ID = strings.Repeat("9", 64)
				return set
			},
			code: report.CodeIncongruentShards,
			says: "validation did not reach the same verdicts",
		},
		{
			name: "a catalogue of a different size",
			build: func(_ *testing.T, set []*report.Report) []*report.Report {
				set[1].Mutants = set[1].Mutants[:len(set[1].Mutants)-1]
				return set
			},
			code: report.CodeIncongruentShards,
			says: "catalogues",
		},
		{
			name: "a shard missing from the set",
			build: func(_ *testing.T, set []*report.Report) []*report.Report {
				return set[:1]
			},
			code: report.CodeIncompleteShardSet,
			says: "not the whole run",
		},
		{
			name: "one shard given twice",
			build: func(_ *testing.T, set []*report.Report) []*report.Report {
				return []*report.Report{set[0], set[0]}
			},
			code: report.CodeIncompleteShardSet,
			says: "given twice",
		},
		{
			name: "an index outside the split",
			build: func(_ *testing.T, set []*report.Report) []*report.Report {
				set[1].Shard.Index = 7
				return set
			},
			code: report.CodeIncompleteShardSet,
			says: "not a shard of this run",
		},
		{
			name: "a shard that measured another shard's mutant",
			build: func(t *testing.T, set []*report.Report) []*report.Report {
				disclaim(t, set[0], false)
				return set
			},
			code: report.CodeShardOwnershipMismatch,
			says: "is the one that owns it",
		},
		{
			name: "a shard that disclaimed one of its own",
			build: func(t *testing.T, set []*report.Report) []*report.Report {
				disclaim(t, set[0], true)
				return set
			},
			code: report.CodeShardOwnershipMismatch,
			says: "is the shard that owns it",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			set := c.build(t, shards(t, 2))
			_, err := report.MergeShards(report.MergeOptions{RunID: mergedRunID, Shards: set})
			if err == nil {
				t.Fatal("MergeShards accepted documents that do not describe one run")
			}
			if code := report.CodeOf(err); code != c.code {
				t.Fatalf("code = %q, want %q (%v)", code, c.code, err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not mention %q: %v", c.says, err)
			}
		})
	}
}

// disclaim breaks one shard's rows in one of the two possible directions:
// claiming a mutant it does not own, or disowning one it does.
func disclaim(t *testing.T, shard *report.Report, own bool) {
	t.Helper()
	other := string(report.NotRunOtherShard)
	for i, m := range shard.Mutants {
		owned := mutation.ShardIndex(m.ID, shard.Shard.Total) == shard.Shard.Index
		if owned != own {
			continue
		}
		if own {
			shard.Mutants[i].Outcome = report.OutcomeNotRun
			shard.Mutants[i].NotRunReason = &other
		} else {
			shard.Mutants[i].Outcome = report.OutcomeSurvived
			shard.Mutants[i].NotRunReason = nil
		}
		return
	}
	t.Fatalf("shard %d of %d has no mutant to break", shard.Shard.Index, shard.Shard.Total)
}

// TestMergeTakesTheWorstStatus proves a merge cannot flatter a run that did not
// finish: a completed shard beside a failed one is a failed run, because the
// failed shard's mutants were never measured.
func TestMergeTakesTheWorstStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		second report.Status
		want   report.Status
	}{
		{report.StatusCompleted, report.StatusCompleted},
		{report.StatusInterrupted, report.StatusInterrupted},
		{report.StatusFailed, report.StatusFailed},
	}
	for _, c := range cases {
		set := shards(t, 2)
		set[1].Status = c.second
		if got := mergeShards(t, set).Status; got != c.want {
			t.Errorf("merging a completed shard with a %s one gives %s, want %s", c.second, got, c.want)
		}
	}
}

// TestMergeIsTheEnvelopeOfTheShardClocks proves the merged duration describes
// the run rather than the sum of the machines: shards run at the same time, so
// adding their durations would report a wall-clock time nobody waited.
func TestMergeIsTheEnvelopeOfTheShardClocks(t *testing.T) {
	t.Parallel()

	set := shards(t, 2)
	set[0].StartedAt = "2026-02-18T09:15:00Z"
	set[0].FinishedAt = "2026-02-18T09:15:30Z"
	set[1].StartedAt = "2026-02-18T09:15:10Z"
	set[1].FinishedAt = "2026-02-18T09:15:50Z"

	merged := mergeShards(t, set)
	if merged.StartedAt != "2026-02-18T09:15:00Z" || merged.FinishedAt != "2026-02-18T09:15:50Z" {
		t.Errorf("the merged run ran %s to %s", merged.StartedAt, merged.FinishedAt)
	}
	if merged.DurationMS != 50_000 {
		t.Errorf("duration_ms = %d, want 50000", merged.DurationMS)
	}
}

// TestMergedWarningsAreDeduplicated proves the merged document keeps every
// distinct warning once, in shard order, rather than one copy per shard of the
// sentence they all said.
func TestMergedWarningsAreDeduplicated(t *testing.T) {
	t.Parallel()

	set := shards(t, 2)
	only := report.Warning{Code: "GOM4041", Message: "the per-run temporary directory could not be removed"}
	set[1].Warnings = append(set[1].Warnings, only)

	merged := mergeShards(t, set)
	if len(merged.Warnings) != len(set[0].Warnings)+1 {
		t.Fatalf("merged warnings = %v", merged.Warnings)
	}
	if merged.Warnings[len(merged.Warnings)-1] != only {
		t.Errorf("the warning only one shard published is not last: %v", merged.Warnings)
	}
}

// TestParseRoundTripsADocument proves a report survives being written and read
// back, which is what `report merge` does to every file it is given.
func TestParseRoundTripsADocument(t *testing.T) {
	t.Parallel()

	original := buildFixture(t)
	parsed, err := report.Parse(mustMarshal(t, original))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if diff := cmp.Diff(original, parsed); diff != "" {
		t.Errorf("the document did not survive the round trip (-written +read):\n%s", diff)
	}
}

// TestParseRefuses covers the files somebody will point `report merge` at by
// mistake.
func TestParseRefuses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		data string
		says string
	}{
		{"not JSON at all", "this is not a document\n", "not a go-mutants run report"},
		{
			"another tool's document",
			`{"document_type":"go-mutants/catalog","schema_version":1}`,
			"not \"go-mutants/run-report\"",
		},
		{
			"a version this build does not read",
			`{"document_type":"go-mutants/run-report","schema_version":2}`,
			"run-report v2",
		},
		{
			"a field nothing here declares",
			`{"document_type":"go-mutants/run-report","schema_version":1,"future_field":true}`,
			"not a go-mutants run report",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			_, err := report.Parse([]byte(c.data))
			if err == nil {
				t.Fatal("Parse accepted it")
			}
			if code := report.CodeOf(err); code != report.CodeMalformedDocument {
				t.Fatalf("code = %q, want %q (%v)", code, report.CodeMalformedDocument, err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not mention %q: %v", c.says, err)
			}
		})
	}
}

// TestParseShard covers the `--shard` specification, which is the one piece of
// this feature a user types by hand.
func TestParseShard(t *testing.T) {
	t.Parallel()

	valid := []struct {
		spec  string
		index int
		total int
	}{
		{"1/1", 1, 1},
		{"1/4", 1, 4},
		{"4/4", 4, 4},
		{" 2 / 3 ", 2, 3},
	}
	for _, c := range valid {
		shard, err := report.ParseShard(c.spec)
		if err != nil {
			t.Errorf("ParseShard(%q): %v", c.spec, err)
			continue
		}
		if shard.Index != c.index || shard.Total != c.total {
			t.Errorf("ParseShard(%q) = %d/%d, want %d/%d", c.spec, shard.Index, shard.Total, c.index, c.total)
		}
		if shard.Assignment != mutation.ShardAssignment {
			t.Errorf("ParseShard(%q) named the assignment %q", c.spec, shard.Assignment)
		}
	}

	invalid := []struct{ spec, says string }{
		{"", "two numbers separated by a slash"},
		{"3", "two numbers separated by a slash"},
		{"a/b", "whole numbers"},
		{"1/0", "cannot be split into 0 shards"},
		{"0/4", "between 1 and 4"},
		{"5/4", "between 1 and 4"},
		{"-1/4", "between 1 and 4"},
	}
	for _, c := range invalid {
		_, err := report.ParseShard(c.spec)
		if err == nil {
			t.Errorf("ParseShard(%q) was accepted", c.spec)
			continue
		}
		if code := report.CodeOf(err); code != report.CodeInvalidShardSpec {
			t.Errorf("ParseShard(%q) code = %q, want %q", c.spec, code, report.CodeInvalidShardSpec)
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("ParseShard(%q) does not mention %q: %v", c.spec, c.says, err)
		}
	}
}

// TestBuildRefusesAnImpossibleShard proves a document cannot state a shard it
// could not have been.
func TestBuildRefusesAnImpossibleShard(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		shard report.Shard
		code  report.Code
	}{
		{"an index above the total", report.Shard{Index: 3, Total: 2}, report.CodeInvalidShard},
		{"an index below one", report.Shard{Index: 0, Total: 2}, report.CodeInvalidShard},
		{"a total of zero", report.Shard{Index: 1, Total: 0}, report.CodeInvalidShard},
		{
			"an assignment this build does not implement",
			report.Shard{Index: 1, Total: 2, Assignment: "positional-v1"},
			report.CodeInvalidShard,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			opts := shardOptions(t, 1, 2)
			opts.Shard = &c.shard
			_, err := report.Build(opts)
			if err == nil {
				t.Fatal("Build wrote a report claiming an impossible shard")
			}
			if code := report.CodeOf(err); code != c.code {
				t.Errorf("code = %q, want %q (%v)", code, c.code, err)
			}
		})
	}
}

// TestBuildRefusesAShardWithoutTheMode holds the two halves of a sharded
// document together: a `shard` block and a selection mode that does not say
// `shard` would be two contradictory statements about one run.
func TestBuildRefusesAShardWithoutTheMode(t *testing.T) {
	t.Parallel()

	opts := shardOptions(t, 1, 2)
	opts.Mode = report.ModeAll
	_, err := report.Build(opts)
	if err == nil {
		t.Fatal("Build wrote a sharded report claiming to have run everything")
	}
	if code := report.CodeOf(err); code != report.CodeInvalidSelection {
		t.Errorf("code = %q, want %q (%v)", code, report.CodeInvalidSelection, err)
	}
}

// TestBuildRefusesAChangedRunWithNoRef holds the other half of the same rule:
// a run that narrowed itself to a diff has to say which diff.
func TestBuildRefusesAChangedRunWithNoRef(t *testing.T) {
	t.Parallel()

	opts := fixtureOptions(t)
	opts.Mode = report.ModeChanged
	_, err := report.Build(opts)
	if err == nil {
		t.Fatal("Build wrote a changed run that names no ref")
	}
	if code := report.CodeOf(err); code != report.CodeInvalidSelection {
		t.Errorf("code = %q, want %q (%v)", code, report.CodeInvalidSelection, err)
	}
}

// TestChangedRefIsRecorded proves the ref reaches the document, and that a run
// which did not narrow by a diff writes null rather than an empty string.
func TestChangedRefIsRecorded(t *testing.T) {
	t.Parallel()

	opts := fixtureOptions(t)
	opts.Mode = report.ModeChanged
	opts.ChangedRef = "origin/main"
	r, err := report.Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Selection.ChangedRef == nil || *r.Selection.ChangedRef != "origin/main" {
		t.Fatalf("changed_ref = %v", r.Selection.ChangedRef)
	}
	if got := string(mustMarshal(t, r)); !strings.Contains(got, `"changed_ref": "origin/main"`) {
		t.Error("the ref is not in the document")
	}
	if got := string(mustMarshal(t, buildFixture(t))); !strings.Contains(got, `"changed_ref": null`) {
		t.Error("a run that took no diff does not write changed_ref as null")
	}
}

// TestNotRunReasonIsBiconditional proves both halves of the pairing: a mutant
// that was not run says why, and a mutant that was does not.
func TestNotRunReasonIsBiconditional(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		outcome mutation.Outcome
		reason  report.NotRunReason
		says    string
	}{
		{"a not-run mutant with no reason", mutation.OutcomeNotRun, "", "does not say why"},
		{"a killed mutant with a reason", mutation.OutcomeKilled, report.NotRunInterrupted, "was measured"},
		{"a reason nobody defined", mutation.OutcomeNotRun, "shrugged", "is not a reason"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			opts := fixtureOptions(t)
			opts.Results[0].Outcome = c.outcome
			opts.Results[0].NotRunReason = c.reason
			_, err := report.Build(opts)
			if err == nil {
				t.Fatal("Build accepted a contradictory result")
			}
			if code := report.CodeOf(err); code != report.CodeInvalidNotRunReason {
				t.Fatalf("code = %q, want %q (%v)", code, report.CodeInvalidNotRunReason, err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not mention %q: %v", c.says, err)
			}
		})
	}
}
