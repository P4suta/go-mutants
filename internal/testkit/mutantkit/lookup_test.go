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
