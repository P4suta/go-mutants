// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"reflect"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// TestReturnSiteSurvivesHintsOf is the other end of the hint's journey.
//
// The return probe hint is computed where the type checker is and consumed
// where the byte rewriter is, and the only thing between them is this index. It
// carries [discover.Guard] whole rather than the fields it happens to know
// about, so a hint that grew a field arrives without anything here being
// changed — which is worth one test, because the failure mode is a probe tree
// that silently rewrites nothing rather than an error anybody would see.
func TestReturnSiteSurvivesHintsOf(t *testing.T) {
	t.Parallel()

	const src = "package sample\n\nfunc Measure() int { return 1 }\n"
	candidate := mutation.Candidate{
		Path:         sampleFile,
		Rule:         lookupRule(t, "return-zero-numeric"),
		Span:         mutation.Span{StartByte: 43, EndByte: 44},
		Original:     "1",
		Replacement:  "0",
		SourceDigest: mutation.Digest([]byte(src)),
	}
	id, err := candidate.ID()
	if err != nil {
		t.Fatalf("identifying the candidate: %v", err)
	}
	site := &discover.ReturnSite{
		Span:  mutation.Span{StartByte: 36, EndByte: 44},
		Types: []string{"int"},
		Index: 0,
	}
	located := []discover.Located{{
		Candidate: candidate,
		Line:      3,
		Column:    21,
		Package:   "example.com/mini",
		Guard: discover.Guard{
			Form:     discover.GuardFormS,
			SiteSpan: site.Span,
			Return:   site,
		},
	}}

	hints, err := instrument.HintsOf(located)
	if err != nil {
		t.Fatalf("HintsOf: %v", err)
	}
	got, ok := hints[id]
	if !ok {
		t.Fatalf("HintsOf did not index the candidate under %s", id)
	}
	if !reflect.DeepEqual(got.Return, site) {
		t.Errorf("the indexed hint's return site = %+v, want %+v", got.Return, site)
	}
}
