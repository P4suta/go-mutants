// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

// What `report merge` says, and the four failures underneath it that only a
// document read off a disk can produce.
//
// merge_test.go proves the refusals happen. This file reads the sentences they
// are made of, because those sentences are the whole of what a person has to
// work with: a CI job hands `report merge` four files from four runners, and
// "the shards do not describe one run" without saying which shard and which
// field sends somebody to open all four by hand.

// TestAMissingShardIsNamedByItsPlaceInTheList reads the ordinal out of the
// refusal.
//
// The list is the order the user wrote the files in, so "the third report to
// merge is missing" is a position on their own command line. Counting from the
// wrong end, or falling off the end of the words and printing "0th", turns that
// into a puzzle — and the sixth is here because the words run out at five and
// the number has to take over without going out of bounds.
func TestAMissingShardIsNamedByItsPlaceInTheList(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		missing int
		total   int
		says    string
	}{
		{missing: 1, total: 1, says: "the first report to merge is missing"},
		{missing: 2, total: 2, says: "the second report to merge is missing"},
		{missing: 3, total: 3, says: "the third report to merge is missing"},
		{missing: 5, total: 5, says: "the fifth report to merge is missing"},
		{missing: 6, total: 6, says: "the 6th report to merge is missing"},
		{missing: 7, total: 7, says: "the 7th report to merge is missing"},
	} {
		t.Run(tc.says, func(t *testing.T) {
			t.Parallel()

			set := make([]*report.Report, 0, tc.total)
			for index := 1; index <= tc.total; index++ {
				if index == tc.missing {
					set = append(set, nil)
					continue
				}
				set = append(set, placeholderShard(index, tc.total))
			}
			_, err := report.MergeShards(report.MergeOptions{RunID: mergedRunID, Shards: set})
			if got := report.CodeOf(err); got != report.CodeNoShardReports {
				t.Fatalf("MergeShards = %v (code %q), want %s", err, got, report.CodeNoShardReports)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

// TestAReportThatIsNotAShardIsNamedByItsPlaceToo covers the other two refusals
// the list walk can reach, which count the same way.
func TestAReportThatIsNotAShardIsNamedByItsPlaceToo(t *testing.T) {
	t.Parallel()

	set := shards(t, 2)
	whole := buildFixture(t)
	_, err := report.MergeShards(report.MergeOptions{
		RunID: mergedRunID, Shards: []*report.Report{set[0], whole},
	})
	if got := report.CodeOf(err); got != report.CodeNotAShardReport {
		t.Fatalf("MergeShards = %v (code %q), want %s", err, got, report.CodeNotAShardReport)
	}
	if want := "the second report (run " + whole.RunID + ") was not produced by a --shard run"; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not say %q: %v", want, err)
	}

	merged := mergeShards(t, shards(t, 2))
	merged.Shard = &report.Shard{Index: 1, Total: 2, Assignment: mutation.ShardAssignment}
	_, err = report.MergeShards(report.MergeOptions{
		RunID: mergedRunID, Shards: []*report.Report{set[0], merged},
	})
	if got := report.CodeOf(err); got != report.CodeNotAShardReport {
		t.Fatalf("MergeShards = %v (code %q), want %s", err, got, report.CodeNotAShardReport)
	}
	if want := "the second report (run " + merged.RunID + ") is itself a merge of 2 shards"; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not say %q: %v", want, err)
	}
}

// TestAnIncompleteSetSaysHowManyAreMissingAndWhich reads the count and the
// verb.
//
// One missing shard and two are the same failure and a differently shaped
// sentence, and the sentence is what tells a CI author whether one job died or
// the matrix is the wrong size. The list in brackets is the actionable half: it
// names the indices to go and look for.
func TestAnIncompleteSetSaysHowManyAreMissingAndWhich(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		total int
		given int
		says  string
	}{
		"one of two":    {total: 2, given: 1, says: "1 of the 2 shards is missing (2)"},
		"two of three":  {total: 3, given: 1, says: "2 of the 3 shards are missing (2, 3)"},
		"one of three":  {total: 3, given: 2, says: "1 of the 3 shards is missing (3)"},
		"three of four": {total: 4, given: 1, says: "3 of the 4 shards are missing (2, 3, 4)"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			set := shards(t, tc.total)[:tc.given]
			_, err := report.MergeShards(report.MergeOptions{RunID: mergedRunID, Shards: set})
			if got := report.CodeOf(err); got != report.CodeIncompleteShardSet {
				t.Fatalf("MergeShards = %v (code %q), want %s", err, got, report.CodeIncompleteShardSet)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

// TestARefusalNamesTheShardAsTheUserWouldNameIt reads the other half of every
// merge refusal: which of the documents it is about.
//
// "2 of 4" is how a CI matrix names a job and "run 20260218T091502Z-3f9c" is
// what the file is called. A refusal that gave one without the other would send
// somebody to the right job with no way to tell which of its two artefacts to
// open, or to the right file with no idea which job wrote it.
func TestARefusalNamesTheShardAsTheUserWouldNameIt(t *testing.T) {
	t.Parallel()

	set := shards(t, 2)
	set[1].ToolVersion = "0.0.0-other"
	_, err := report.MergeShards(report.MergeOptions{RunID: mergedRunID, Shards: set})
	if got := report.CodeOf(err); got != report.CodeIncongruentShards {
		t.Fatalf("MergeShards = %v (code %q), want %s", err, got, report.CodeIncongruentShards)
	}
	want := "shard 2 of 2 (run " + set[1].RunID + ") does not describe the same run as shard 1 of 2 (run " +
		set[0].RunID + "): the tool version is \"0.0.0-other\" rather than \"" + set[0].ToolVersion + "\""
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal is not the one a user can act on:\n got %v\nwant it to contain %q", err, want)
	}
}

// TestMergeRefusesADocumentWhoseClockCannotBeRead covers both timestamps.
//
// The merged duration is the envelope of the shards' clocks, so a shard whose
// own start or finish is not a time makes the whole envelope meaningless — and
// the failure a caller must never get instead is a merged document quietly
// stamped with the zero time, which reads as a run that happened in the year 1.
func TestMergeRefusesADocumentWhoseClockCannotBeRead(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		break_ func(r *report.Report)
		says   string
	}{
		"a start that is not a time":  {func(r *report.Report) { r.StartedAt = "yesterday" }, `has no readable start time ("yesterday")`},
		"a finish that is not a time": {func(r *report.Report) { r.FinishedAt = "later" }, `has no readable finish time ("later")`},
		"a start that is missing":     {func(r *report.Report) { r.StartedAt = "" }, `has no readable start time ("")`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			set := shards(t, 2)
			tc.break_(set[1])
			_, err := report.MergeShards(report.MergeOptions{RunID: mergedRunID, Shards: set})
			if got := report.CodeOf(err); got != report.CodeInvalidTimestamps {
				t.Fatalf("MergeShards = %v (code %q), want %s", err, got, report.CodeInvalidTimestamps)
			}
			if want := "shard 2 of 2 (run " + set[1].RunID + ") " + tc.says; !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not say %q: %v", want, err)
			}
		})
	}
}

// TestMergeRefusesRowsItsOwnBlocksContradict is the three checks that read the
// merged rows rather than the shards' own headers.
//
// Each of them is a statement the merged document must not be able to make, and
// each is reachable only from a file: [report.Build] refuses all three on the
// way in, and `report merge` reads documents that another build, another
// version, or somebody's editor produced.
func TestMergeRefusesRowsItsOwnBlocksContradict(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		break_ func(t *testing.T, set []*report.Report)
		code   report.Code
		says   string
	}{
		"a mutant marked uncovered in a run with coverage off": {
			break_: func(t *testing.T, set []*report.Report) {
				t.Helper()
				set[0].Mutants[ownedRow(t, set[0])].Uncovered = true
			},
			code: report.CodeInvalidCoverage,
			says: `is marked uncovered in a run whose coverage mode is "off"`,
		},
		"more outcomes stored than were looked up": {
			break_: func(t *testing.T, set []*report.Report) {
				t.Helper()
				set[0].Cache.Writes = set[0].Cache.Misses + 5
			},
			code: report.CodeInvalidCache,
			says: "an outcome is only stored for a mutant the cache did not already have",
		},
		"an outcome this build cannot count": {
			break_: func(t *testing.T, set []*report.Report) {
				t.Helper()
				set[0].Mutants[ownedRow(t, set[0])].Outcome = report.Outcome("shrugged")
			},
			code: report.CodeInvalidOutcome,
			says: `"shrugged" is not an outcome this report can read`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			set := shards(t, 2)
			tc.break_(t, set)
			merged, err := report.MergeShards(report.MergeOptions{RunID: mergedRunID, Shards: set})
			if got := report.CodeOf(err); got != tc.code {
				t.Fatalf("MergeShards = %v (code %q), want %s", err, got, tc.code)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q: %v", tc.says, err)
			}
			if merged != nil {
				t.Error("a merged document came back beside the failure")
			}
		})
	}
}

// TestMergeRefusesARunIDItCouldNotFile is the merged document's own identity,
// which is the one field of it nothing else checked.
//
// The id becomes a file name under `runs/`, so a value with a path separator in
// it is a path bug waiting to happen — and the merged document is exactly the
// one a CI job publishes, with an id the job invented.
func TestMergeRefusesARunIDItCouldNotFile(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"", "nightly", "../escape", "20260218T091500Z-3f9cX"} {
		merged, err := report.MergeShards(report.MergeOptions{RunID: id, Shards: shards(t, 2)})
		if got := report.CodeOf(err); got != report.CodeInvalidRunID {
			t.Fatalf("MergeShards(run id %q) = %v (code %q), want %s", id, err, got, report.CodeInvalidRunID)
		}
		if !strings.Contains(err.Error(), "is not a run id: expected a UTC timestamp and four hex digits") {
			t.Errorf("the refusal does not say what a run id is: %v", err)
		}
		if merged != nil {
			t.Error("a merged document came back beside the failure")
		}
	}
}

// TestOwnershipReadsTheOutcomeRatherThanTheReason is about a document that
// contradicts itself, and which half of it the ownership check believes.
//
// A row that states an outcome and a not-run reason at once cannot come out of
// [report.Build] — the pairing is refused in both directions — but it can come
// out of a file. What decides whether a shard disclaimed a mutant is that it
// was not run; the reason only says which kind of not-run it was. A check that
// read the reason alone would see a measured mutant as somebody else's and
// refuse a set that is perfectly complete.
func TestOwnershipReadsTheOutcomeRatherThanTheReason(t *testing.T) {
	t.Parallel()

	set := shards(t, 2)
	row := ownedRow(t, set[0])
	if set[0].Mutants[row].Outcome == report.OutcomeNotRun {
		t.Fatalf("the fixture row chosen for this test was not measured: %s", set[0].Mutants[row].DisplayID)
	}
	other := string(report.NotRunOtherShard)
	set[0].Mutants[row].NotRunReason = &other

	if _, err := report.MergeShards(report.MergeOptions{RunID: mergedRunID, Shards: set}); err != nil {
		t.Fatalf("MergeShards refused a measured row that carries a reason as well: %v", err)
	}
}

// TestMergedStatusIsAlwaysOneTheSchemaKnows guards the merged document's own
// status against a value it copied from somewhere.
//
// The status of a merge is the worst thing that happened to any shard, ranked
// over the three a run can end in. A document with a fourth value in it — an
// older build, a newer one, a hand edit — is not evidence that anything went
// wrong, and letting it become the merged status would publish a document that
// fails the schema its own package defines.
func TestMergedStatusIsAlwaysOneTheSchemaKnows(t *testing.T) {
	t.Parallel()

	for _, status := range []report.Status{"", "aborted", "cancelled"} {
		set := shards(t, 2)
		set[1].Status = status
		merged := mergeShards(t, set)
		if merged.Status != report.StatusCompleted {
			t.Errorf("a shard reporting %q made the merged run %q, want %q",
				status, merged.Status, report.StatusCompleted)
		}
		if !merged.Status.Valid() {
			t.Errorf("the merged status %q is not one a run can end in", merged.Status)
		}
	}
}

// TestMergedWarningsAreInShardOrderWhateverOrderTheFilesArrive pins the one
// ordering a merged document has that no shard has.
//
// `report merge a.json b.json c.json` is a command line, and a CI job's glob
// hands the files over in whatever order the runner listed them. The warnings
// are one list made of three, and the only order that means anything is the
// shards' own: shard 1's warnings, then shard 2's, then shard 3's. Publication
// order inside a shard is preserved for the same reason, so the two together
// are a timeline rather than a set.
func TestMergedWarningsAreInShardOrderWhateverOrderTheFilesArrive(t *testing.T) {
	t.Parallel()

	set := shards(t, 3)
	unique := make([]report.Warning, 0, len(set))
	for i, shard := range set {
		w := report.Warning{
			Code:    "GOM4041",
			Message: fmt.Sprintf("shard %d could not remove its temporary directory", i+1),
		}
		shard.Warnings = append(shard.Warnings, w)
		unique = append(unique, w)
	}

	// Handed over out of order, which is what a glob does.
	merged := mergeShards(t, []*report.Report{set[2], set[0], set[1]})
	got := merged.Warnings[len(merged.Warnings)-len(unique):]
	for i := range unique {
		if got[i] != unique[i] {
			t.Fatalf("the shards' own warnings came back as\n %v\nwant\n %v", got, unique)
		}
	}
}

// TestParseRefusesAFileHoldingMoreThanOneDocument is the one shape of file a
// decoder accepts silently.
//
// Two concatenated reports decode as the first one, so a `report merge` given a
// log somebody appended to would take the older document and say nothing. The
// second document is what proves the file is not a run report, and it has to be
// looked for after the first one has been read rather than instead of it.
func TestParseRefusesAFileHoldingMoreThanOneDocument(t *testing.T) {
	t.Parallel()

	one := mustMarshalReport(t, buildFixture(t))
	r, err := report.Parse(append(append([]byte{}, one...), one...))
	if got := report.CodeOf(err); got != report.CodeMalformedDocument {
		t.Fatalf("Parse of two documents = %v (code %q), want %s", err, got, report.CodeMalformedDocument)
	}
	if !strings.Contains(err.Error(), "the file holds more than one document; a run report is a single JSON object") {
		t.Errorf("the refusal does not say the file holds two documents: %v", err)
	}
	if r != nil {
		t.Error("a report came back beside the failure")
	}
}

// placeholderShard is the least a document can be and still get past the list
// walk: a shard block and nothing else. The walk checks three things in order
// and returns on the first, so a set built to reach the third position only has
// to be non-nil, sharded, and not itself a merge.
func placeholderShard(index, total int) *report.Report {
	return &report.Report{
		DocumentType:  report.DocumentType,
		SchemaVersion: report.SchemaVersion,
		RunID:         fmt.Sprintf("20260218T09150%dZ-3f9c", index%10),
		Shard:         &report.Shard{Index: index, Total: total, Assignment: mutation.ShardAssignment},
	}
}

// ownedRow returns the index of a row this shard measured itself: one it owns,
// and that it did not adopt from the cache, so that breaking it reaches the
// check under test rather than the cache block above it.
func ownedRow(t *testing.T, shard *report.Report) int {
	t.Helper()
	for i, m := range shard.Mutants {
		if m.Cached || !shard.Shard.Owns(m.ID) {
			continue
		}
		return i
	}
	t.Fatalf("shard %d of %d measured nothing of its own", shard.Shard.Index, shard.Shard.Total)
	return -1
}

// mustMarshalReport encodes a report or fails the test.
func mustMarshalReport(t *testing.T, r *report.Report) []byte {
	t.Helper()
	data, err := r.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return data
}

// TestShardNameFallsBackToTheRunItself covers the one rendering a merge cannot
// reach.
//
// Every caller has refused a report with no `shard` block long before it gets
// here, so this is what the sentence would say if one ever did — and "run
// 20260218T091500Z-3f9c" is the answer, because the run id is what the file is
// called and is the only thing such a document could be identified by.
func TestShardNameFallsBackToTheRunItself(t *testing.T) {
	t.Parallel()

	sharded := &report.Report{RunID: fixtureRunID, Shard: &report.Shard{Index: 2, Total: 4}}
	if want := "2 of 4 (run " + fixtureRunID + ")"; report.ShardName(sharded) != want {
		t.Errorf("ShardName = %q, want %q", report.ShardName(sharded), want)
	}
	whole := &report.Report{RunID: fixtureRunID}
	if want := "run " + fixtureRunID; report.ShardName(whole) != want {
		t.Errorf("ShardName = %q, want %q", report.ShardName(whole), want)
	}
}
