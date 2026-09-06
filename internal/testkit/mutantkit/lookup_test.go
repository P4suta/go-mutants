// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit_test

import (
	"strings"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// TestMutantAtRefusesTwoMatches is the assertion that turns a fixture's layout
// into a contract.
//
// Every integration suite here names a mutant by the rule that produced it and
// the file it is in — never by an identity, which is a digest, and never by a
// catalogue position, which changes when a rule is added. That only names one
// mutant while the fixture keeps one function per file and no repeated operator,
// so the lookup asserts it rather than returning the first match: a fixture that
// grew a second `==` would otherwise silently re-point half a suite's assertions
// at a mutant nobody meant.
func TestMutantAtRefusesTwoMatches(t *testing.T) {
	t.Parallel()

	catalog := catalogOf(t,
		candidate("pkg/a.go", "eq-to-neq", 10, "==", "!="),
		candidate("pkg/a.go", "eq-to-neq", 40, "==", "!="),
	)

	rec := expectFatal(t, func(tb testing.TB) {
		mutantkit.MutantAt(tb, catalog, "pkg/a.go", "eq-to-neq")
	})
	report := rec.first(t, "a lookup that matched twice")
	for _, want := range []string{"2", "pkg/a.go", "eq-to-neq"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not mention %q:\n%s", want, report)
		}
	}
}

// TestMutantAtRefusesNoMatch is the other direction, and the one a renamed rule
// produces: a lookup that returned a zero Mutant would be asserted against as if
// it were a mutant, and the failure would be about a replacement being empty
// rather than about the rule not existing.
func TestMutantAtRefusesNoMatch(t *testing.T) {
	t.Parallel()

	catalog := catalogOf(t, candidate("pkg/a.go", "eq-to-neq", 10, "==", "!="))

	rec := expectFatal(t, func(tb testing.TB) {
		mutantkit.MutantAt(tb, catalog, "pkg/a.go", "lt-to-le")
	})
	if report := rec.first(t, "a lookup that matched nothing"); !strings.Contains(report, "lt-to-le") {
		t.Errorf("the report does not name the rule that was asked for:\n%s", report)
	}
}

// TestMutantAtReturnsTheOneMatch is the happy path, and it is here because a
// helper that reported on a match would bury the failures above in noise.
func TestMutantAtReturnsTheOneMatch(t *testing.T) {
	t.Parallel()

	catalog := catalogOf(t,
		candidate("pkg/a.go", "eq-to-neq", 10, "==", "!="),
		candidate("pkg/b.go", "eq-to-neq", 10, "==", "!="),
	)

	got := mutantkit.MutantAt(t, catalog, "pkg/b.go", "eq-to-neq")
	if got.Path != "pkg/b.go" || got.Rule.Name != "eq-to-neq" {
		t.Errorf("MutantAt returned %s %s, want pkg/b.go eq-to-neq", got.Path, got.Rule.Name)
	}
	if byRule := mutantkit.ByRule(t, catalogOf(t, candidate("pkg/a.go", "lt-to-le", 10, "<", "<=")), "lt-to-le"); byRule.Rule.Name != "lt-to-le" {
		t.Errorf("ByRule returned %s", byRule.Rule.Name)
	}
}

// TestAPIMutantAtRefusesARejectedMutant is the extra claim the public catalogue
// carries and the internal one does not.
//
// gomutants.Catalog lists every catalogued mutant, accepted or not: a rejected
// one is an edit that did not compile, and it is published so that `--explain`
// can say so. Every test that looks one up is about to activate it and assert on
// what the suite did, which a rejected mutant can never be — so a lookup that
// returned one would produce a run that failed for a reason nothing in the test
// mentions.
func TestAPIMutantAtRefusesARejectedMutant(t *testing.T) {
	t.Parallel()

	catalog := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{Path: "pkg/a.go", Rule: "eq-to-neq", DisplayID: "aaaa", Accepted: false},
		{Path: "pkg/b.go", Rule: "lt-to-le", DisplayID: "bbbb", Accepted: true},
	}}

	rec := expectFatal(t, func(tb testing.TB) {
		mutantkit.APIMutantAt(tb, catalog, "pkg/a.go", "eq-to-neq")
	})
	report := rec.first(t, "a lookup of a rejected mutant")
	if !strings.Contains(report, "rejected") {
		t.Errorf("the report does not say the mutant was rejected:\n%s", report)
	}

	if got := mutantkit.APIMutantAt(t, catalog, "pkg/b.go", "lt-to-le"); got.DisplayID != "bbbb" {
		t.Errorf("APIMutantAt returned %+v, want the accepted mutant", got)
	}
	if got := mutantkit.APIByRule(t, catalog, "lt-to-le"); got.DisplayID != "bbbb" {
		t.Errorf("APIByRule returned %+v, want the accepted mutant", got)
	}
}

// candidate builds one proposed edit, with the span derived from the offset and
// the original text so that the candidate validates.
func candidate(path, rule string, at uint32, original, replacement string) mutation.Candidate {
	return mutation.Candidate{
		Path: path,
		Rule: mutation.Rule{
			Family:  mutation.FamilyComparison,
			Name:    rule,
			Version: 1,
			Tier:    mutation.TierBalanced,
		},
		Span:         mutation.Span{StartByte: at, EndByte: at + uint32(len(original))},
		Original:     original,
		Replacement:  replacement,
		SourceDigest: mutation.DigestString("the source of " + path),
	}
}

// catalogOf catalogues a handful of candidates, which is how a lookup is tested
// without a toolchain: the catalogue is a pure value, and discovery is not what
// these assertions are about.
func catalogOf(t *testing.T, candidates ...mutation.Candidate) *mutation.Catalog {
	t.Helper()
	builder := mutation.NewBuilder()
	if err := builder.AddAll(candidates); err != nil {
		t.Fatalf("cataloguing the candidates: %v", err)
	}
	catalog, err := builder.Build()
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}
	return catalog
}
