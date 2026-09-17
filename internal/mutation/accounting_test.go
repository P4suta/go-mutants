// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutation

import "testing"

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

func TestTotalIsEveryBucketRecordAdds(t *testing.T) {
	t.Parallel()

	var tally Tally
	for _, outcome := range Outcomes() {
		if err := tally.Record(Result{Outcome: outcome}); err != nil {
			t.Fatalf("Record(%v): %v", outcome, err)
		}
	}
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
