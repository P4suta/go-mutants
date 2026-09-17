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

func TestProbeSiteSurvivesHintsOf(t *testing.T) {
	t.Parallel()

	const src = "package sample\n\nfunc Measure() int { return 1 }\n"
	candidate := mutation.Candidate{
		Path:         sampleFile,
		Rule:         lookupRule(t, "return-zero-numeric"),
		Span:         mutation.Span{StartByte: 44, EndByte: 45},
		Original:     "1",
		Replacement:  "0",
		SourceDigest: mutation.Digest([]byte(src)),
	}
	id, err := candidate.ID()
	if err != nil {
		t.Fatalf("identifying the candidate: %v", err)
	}
	site := &discover.ProbeSite{
		Form:  discover.ProbeFormReturn,
		Span:  mutation.Span{StartByte: 37, EndByte: 45},
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
			Probe:    site,
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
	if !reflect.DeepEqual(got.Probe, site) {
		t.Errorf("the indexed hint's return site = %+v, want %+v", got.Probe, site)
	}
}

func TestProbesAnswersWhichMutantsAProbeTreeSpeaksFor(t *testing.T) {
	t.Parallel()

	const src = "package sample\n\nfunc Measure() int { return 1 }\n"
	span := mutation.Span{StartByte: 44, EndByte: 45}
	site := &discover.ProbeSite{
		Form:  discover.ProbeFormReturn,
		Span:  mutation.Span{StartByte: 37, EndByte: 45},
		Types: []string{"int"},
		Index: 0,
	}

	mutantOf := func(t *testing.T, rule, replacement string) mutation.Mutant {
		t.Helper()
		candidate := mutation.Candidate{
			Path:         sampleFile,
			Rule:         lookupRule(t, rule),
			Span:         span,
			Original:     "1",
			Replacement:  replacement,
			SourceDigest: mutation.Digest([]byte(src)),
		}
		id, err := candidate.ID()
		if err != nil {
			t.Fatalf("identifying the candidate: %v", err)
		}
		return mutation.Mutant{Index: 0, ID: id, DisplayID: id[:8], Candidate: candidate}
	}
	hintOf := func(t *testing.T, m mutation.Mutant, returnSite *discover.ProbeSite) instrument.Hints {
		t.Helper()
		hints, err := instrument.HintsOf([]discover.Located{{
			Candidate: m.Candidate,
			Line:      3,
			Column:    21,
			Package:   "example.com/mini",
			Guard: discover.Guard{
				Form:     discover.GuardFormS,
				SiteSpan: site.Span,
				Probe:    returnSite,
			},
		}})
		if err != nil {
			t.Fatalf("HintsOf: %v", err)
		}
		return hints
	}

	probed := mutantOf(t, "return-zero-numeric", "0")
	if !hintOf(t, probed, site).Probes(probed) {
		t.Error("a return-value mutant with a return site is not probed")
	}
	if hintOf(t, probed, nil).Probes(probed) {
		t.Error("a mutant whose statement discovery refused is reported as probed")
	}
	shadowed := mutantOf(t, "eq-to-neq", "!=")
	if hintOf(t, shadowed, site).Probes(shadowed) {
		t.Error("a mutant whose replacement is not a probe constant is reported as probed")
	}
	if (instrument.Hints{}).Probes(probed) {
		t.Error("a mutant with no hint at all is reported as probed")
	}
	unknown := *site
	unknown.Form = "a-form-from-a-later-release"
	if hintOf(t, probed, &unknown).Probes(probed) {
		t.Error("a site whose form this build cannot render is reported as probed")
	}
}
