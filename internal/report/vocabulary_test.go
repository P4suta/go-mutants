// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report_test

import (
	"math"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/report"
)

// The document's vocabulary, asked directly.
//
// Every enumerated value in this package renders itself with a `String` method
// and answers `Valid` about itself, and both are read by somebody: `String` by
// every message that names a mode or an outcome, `Valid` by the builder before
// it writes one down. The golden documents pin the spellings that end up in a
// file and nothing else — a `String` that returned "" would leave the golden
// byte-identical, because the encoder writes the underlying string rather than
// calling the method — so the methods themselves are asked here.

// TestEveryStringRendersItsOwnValue walks the seven enumerated types and asks
// each to render one of its values.
//
// The expectation is written out rather than derived from the value, so that a
// `String` that returned the empty string, a constant, or another member's
// spelling is a failure rather than a tautology.
func TestEveryStringRendersItsOwnValue(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		got  string
		want string
	}{
		"status":            {report.StatusCompleted.String(), "completed"},
		"selection mode":    {report.ModeChanged.String(), "changed"},
		"coverage mode":     {report.CoveragePackage.String(), "package"},
		"cache mode":        {report.CacheOn.String(), "on"},
		"outcome":           {report.OutcomeTimedOut.String(), "timed-out"},
		"expectation state": {report.StateFulfilled.String(), "fulfilled"},
		"stage result":      {report.StageSucceeded.String(), "succeeded"},
		"code":              {report.CodeInvalidRunID.String(), "GOM5101"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Errorf("String() = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// TestEveryValidAcceptsItsOwnAndRefusesTheRest asks each `Valid` both
// questions.
//
// One direction alone proves nothing: a `Valid` that always answered true
// passes every acceptance and a `Valid` that always answered false passes every
// refusal, and the builder that reads these is the thing standing between a
// caller's typo and a document that states a value no consumer can read.
func TestEveryValidAcceptsItsOwnAndRefusesTheRest(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		accepts []bool
		refuses []bool
	}{
		"status": {
			accepts: []bool{report.StatusCompleted.Valid(), report.StatusInterrupted.Valid(), report.StatusFailed.Valid()},
			refuses: []bool{report.Status("done").Valid(), report.Status("").Valid()},
		},
		"selection mode": {
			accepts: []bool{report.ModeAll.Valid(), report.ModeMutant.Valid(), report.ModeChanged.Valid(), report.ModeShard.Valid()},
			refuses: []bool{report.SelectionMode("some").Valid(), report.SelectionMode("").Valid()},
		},
		"timeout source": {
			accepts: []bool{report.TimeoutDerived.Valid(), report.TimeoutExplicit.Valid()},
			refuses: []bool{report.TimeoutSource("guessed").Valid(), report.TimeoutSource("").Valid()},
		},
		"memory source": {
			accepts: []bool{report.MemoryExplicit.Valid(), report.MemoryDerived.Valid(), report.MemoryUnavailable.Valid()},
			refuses: []bool{report.MemorySource("guessed").Valid(), report.MemorySource("").Valid()},
		},
		"coverage mode": {
			accepts: []bool{report.CoverageOff.Valid(), report.CoveragePackage.Valid()},
			refuses: []bool{report.CoverageMode("file").Valid(), report.CoverageMode("").Valid()},
		},
		"cache mode": {
			accepts: []bool{report.CacheOff.Valid(), report.CacheOn.Valid()},
			refuses: []bool{report.CacheMode("auto").Valid(), report.CacheMode("").Valid()},
		},
		"stage result": {
			accepts: []bool{report.StageSucceeded.Valid(), report.StageFailed.Valid(), report.StageSkipped.Valid()},
			refuses: []bool{report.StageResult("ok").Valid(), report.StageResult("").Valid()},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for i, ok := range tc.accepts {
				if !ok {
					t.Errorf("Valid() = false for defined value %d, want true", i)
				}
			}
			for i, ok := range tc.refuses {
				if ok {
					t.Errorf("Valid() = true for undefined value %d, want false", i)
				}
			}
		})
	}
}

// TestObservedIsTheFourOutcomesOnePassCanSee pins the other predicate over the
// outcome vocabulary: the two verdicts that are statements about several
// passes are not things one pass observed.
func TestObservedIsTheFourOutcomesOnePassCanSee(t *testing.T) {
	t.Parallel()

	for _, o := range []report.Outcome{
		report.OutcomeKilled, report.OutcomeSurvived, report.OutcomeTimedOut, report.OutcomeErrored,
	} {
		if !o.Observed() {
			t.Errorf("%s.Observed() = false, want true", o)
		}
	}
	for _, o := range []report.Outcome{report.OutcomeInconclusive, report.OutcomeNotRun, report.Outcome("")} {
		if o.Observed() {
			t.Errorf("%s.Observed() = true, want false", o)
		}
	}
}

// TestOutcomeRoundTripsThroughTheCoreVocabulary asks both translations, in both
// directions, and refuses a spelling neither knows.
func TestOutcomeRoundTripsThroughTheCoreVocabulary(t *testing.T) {
	t.Parallel()

	for _, core := range mutation.Outcomes() {
		document, err := report.OutcomeOf(core)
		if err != nil {
			t.Fatalf("OutcomeOf(%v): %v", core, err)
		}
		back, err := document.Mutation()
		if err != nil {
			t.Fatalf("%s.Mutation(): %v", document, err)
		}
		if back != core {
			t.Errorf("%v round-tripped to %v through %q", core, back, document)
		}
	}
	if _, err := report.Outcome("unheard-of").Mutation(); report.CodeOf(err) != report.CodeInvalidOutcome {
		t.Errorf("Mutation() of an unknown spelling = %v, want %s", err, report.CodeInvalidOutcome)
	}
}

// TestOneShardOwnsEachMutant asks [report.Shard.Owns] the only question that
// does not depend on the assignment function's hash: of the n shards a run is
// split into, exactly one owns any given id.
//
// It is written that way on purpose. Asserting that a particular id belongs to
// a particular shard would pin the hash rather than the predicate, and would
// have to be rewritten the day the assignment changes; "exactly one" is what
// the partition promises and is what a merge relies on.
func TestOneShardOwnsEachMutant(t *testing.T) {
	t.Parallel()

	const total = 4
	for _, id := range []string{
		strings.Repeat("0", 64),
		strings.Repeat("a", 64),
		strings.Repeat("7f", 32),
		strings.Repeat("c3", 32),
	} {
		owners := 0
		for index := 1; index <= total; index++ {
			if (report.Shard{Index: index, Total: total, Assignment: mutation.ShardAssignment}).Owns(id) {
				owners++
			}
		}
		if owners != 1 {
			t.Errorf("mutant %s is owned by %d of %d shards, want exactly 1", id[:8], owners, total)
		}
	}
}

// TestStoredRunScoreReportsThePercentageAndItsAbsence covers both answers of
// the listing's score accessor: a run that measured something reports the
// percentage the document holds, and a run that measured nothing says so rather
// than reporting a zero it never computed.
func TestStoredRunScoreReportsThePercentageAndItsAbsence(t *testing.T) {
	t.Parallel()

	measured := 87.5
	run := report.StoredRun{Summary: report.Summary{ScorePercent: &measured}}
	switch score, ok := run.Score(); {
	case !ok:
		t.Error("Score() of a run with a percentage reports none")
	case score != measured:
		t.Errorf("Score() = %v, want %v", score, measured)
	}

	if score, ok := (report.StoredRun{}).Score(); ok {
		t.Errorf("Score() of a run that measured nothing = (%v, true), want (0, false)", score)
	}
}

// TestMarshalRefusesADocumentJSONCannotHold is the encoder's own failure,
// reached through the one field of a report that can hold a value JSON has no
// spelling for.
//
// `score_percent` is a `*float64`, and a non-finite float is exactly what
// encoding/json refuses. It cannot come out of [report.Build] — the score is
// computed from integer counts — so the report is assembled by hand, which is
// the only way to ask what [report.Report.Marshal] does when the encoder says
// no. What it must not do is hand back half a document with no error.
func TestMarshalRefusesADocumentJSONCannotHold(t *testing.T) {
	t.Parallel()

	notANumber := math.NaN()
	r := &report.Report{DocumentType: report.DocumentType, SchemaVersion: report.SchemaVersion}
	r.Summary.ScorePercent = &notANumber

	data, err := r.Marshal()
	if got := report.CodeOf(err); got != report.CodeEncodeFailed {
		t.Fatalf("Marshal of an unencodable report = %v (code %q), want %s", err, got, report.CodeEncodeFailed)
	}
	if data != nil {
		t.Errorf("Marshal returned %d bytes beside its failure, want none", len(data))
	}
	if !strings.Contains(err.Error(), "the run report could not be encoded as JSON") {
		t.Errorf("the failure does not say what could not be encoded: %v", err)
	}
}

// TestTallyRefusesAnOutcomeItCannotCount is the counting side of the same
// vocabulary, asked of a document rather than of a value.
//
// [report.Report.Tally] is how the exit decision is made from the file rather
// than beside it, and the file can have been written by another build or edited
// by hand — `report merge` reads four of them off a CI runner's disk. An
// outcome this build has no spelling for must stop the count rather than be
// folded into one of the six, because a mutant counted as not-run leaves the
// score's denominator and flatters the suite.
func TestTallyRefusesAnOutcomeItCannotCount(t *testing.T) {
	t.Parallel()

	r := &report.Report{
		DocumentType: report.DocumentType, SchemaVersion: report.SchemaVersion,
		Mutants: []report.Mutant{
			{ID: strings.Repeat("ab", 32), DisplayID: "abababab", Outcome: report.OutcomeKilled},
			{ID: strings.Repeat("cd", 32), DisplayID: "cdcdcdcd", Outcome: report.Outcome("shrugged")},
		},
	}
	tally, err := r.Tally()
	if got := report.CodeOf(err); got != report.CodeInvalidOutcome {
		t.Fatalf("Tally over an unreadable outcome = %v (code %q), want %s", err, got, report.CodeInvalidOutcome)
	}
	if !strings.Contains(err.Error(), `"shrugged" is not an outcome this report can read`) {
		t.Errorf("the refusal does not quote the outcome it could not read: %v", err)
	}
	if tally != (mutation.Tally{}) {
		t.Errorf("a partial tally came back beside the failure: %+v", tally)
	}
}
