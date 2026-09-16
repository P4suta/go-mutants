// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The accounting, as a claim rather than as an arithmetic accident.
//
// A run report says every catalogued mutant reached exactly one outcome, and
// that claim rests on two things being true at once: [Tally.Record] routes
// every member of the vocabulary, and [Tally.Total] adds up the same members it
// routes. Both are true, and neither was written down — the first because
// Record's default returns an error nobody had counted the cases against, and
// the second because Total is a sum a reader has to check by eye.
//
// The cost of leaving them unwritten is not that they break. It is that a
// seventh outcome would break them in a way that reads as an ordinary failing
// test somewhere else: a mutant that reached no bucket makes Total smaller than
// the catalogue, and the report that carries it is wrong about a number rather
// than loud about a member it did not know.
package mutation

import "testing"

// TestEveryOutcomeIsRecorded routes every member of the vocabulary and refuses
// the default.
//
// This is the engine's half of the pair goatest keeps on the other side: the
// runner switches on the same vocabulary and decides what each member means to
// a verdict, and this one only asks that each member is a member. Adding a
// seventh outcome fails here first, which is the point — before it is a wrong
// number in a document, it is a case nobody wrote.
func TestEveryOutcomeIsRecorded(t *testing.T) {
	t.Parallel()

	for _, outcome := range Outcomes() {
		var tally Tally
		if err := tally.Record(Result{Outcome: outcome}); err != nil {
			t.Errorf("Record(%v) is an error: %v;\n"+
				"\tevery outcome the vocabulary names has to reach a bucket, or a mutant with "+
				"that outcome is one the accounting cannot see", outcome, err)
			continue
		}
		if got := tally.Total(); got != 1 {
			t.Errorf("Record(%v) left Total() at %d, want 1;\n"+
				"\tthe outcome reached a field Total does not add, so the catalogue and the "+
				"summary would disagree by one for every mutant that ends this way", outcome, got)
		}
	}
}

// TestAnOutcomeOutsideTheVocabularyIsRefused is the other half: Record's
// default has to be reachable, or the test above is asserting that a switch
// with no default routes everything.
func TestAnOutcomeOutsideTheVocabularyIsRefused(t *testing.T) {
	t.Parallel()

	beyond := Outcome(len(Outcomes()) + 1)
	var tally Tally
	if err := tally.Record(Result{Outcome: beyond}); err == nil {
		t.Errorf("Record(%v) succeeded for an outcome the vocabulary does not name;\n"+
			"\tthe default is what stops an unknown outcome being counted as whichever "+
			"bucket came last", beyond)
	}
	if got := tally.Total(); got != 0 {
		t.Errorf("a refused outcome still moved Total to %d", got)
	}
}

// TestTotalIsEveryBucketRecordAdds pins the sum against the buckets, so that a
// field added to one and not the other is caught where it is introduced.
func TestTotalIsEveryBucketRecordAdds(t *testing.T) {
	t.Parallel()

	var tally Tally
	for _, outcome := range Outcomes() {
		if err := tally.Record(Result{Outcome: outcome}); err != nil {
			t.Fatalf("Record(%v): %v", outcome, err)
		}
	}
	// One of each, plus the second survivor shape: Survived is the one outcome
	// that lands in two fields depending on the ledger, and Total has to add
	// both of them.
	if err := tally.Record(Result{Outcome: OutcomeSurvived, ExpectedSurvivor: true}); err != nil {
		t.Fatalf("Record(expected survivor): %v", err)
	}

	want := len(Outcomes()) + 1
	if got := tally.Total(); got != want {
		t.Errorf("Total() is %d after recording %d results;\n"+
			"\tthe difference is a bucket Record fills and Total does not add, which is a "+
			"mutant the report loses", got, want)
	}
}
