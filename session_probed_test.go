// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/validate"
)

func TestProbedImpliesAccepted(t *testing.T) {
	t.Parallel()

	const source = "package a\n\nvar flag = true\n"
	rule, ok := mutation.CanonicalRegistry().Lookup("true-to-false")
	if !ok {
		t.Fatal("the canonical registry does not know true-to-false")
	}
	span, err := mutation.NewSpan(22, 26)
	if err != nil {
		t.Fatalf("building the span: %v", err)
	}
	if got := source[span.StartByte:span.EndByte]; got != "true" {
		t.Fatalf("fixture span covers %q, want %q", got, "true")
	}
	builder := mutation.NewBuilder()
	if addErr := builder.Add(mutation.Candidate{
		Path:         "a.go",
		Rule:         rule,
		Span:         span,
		Original:     "true",
		Replacement:  "false",
		SourceDigest: mutation.DigestString(source),
	}); addErr != nil {
		t.Fatalf("adding the candidate: %v", addErr)
	}
	catalog, err := builder.Build()
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}
	if catalog.Len() != 1 {
		t.Fatalf("the fixture catalogues %d mutants, want 1", catalog.Len())
	}
	id := catalog.Mutants()[0].ID

	public, _ := makeCatalog(
		"", gocmd.Toolchain{}, "balanced", discover.Result{}, catalog,
		[]validate.Rejection{{ID: id, Diagnostic: "a.go:3:12: cannot use false"}},
		map[string]bool{},
		map[string]bool{id: true},
		nil,
		nil,
	)
	if len(public.Mutants) != 1 {
		t.Fatalf("the public catalogue holds %d mutants, want 1", len(public.Mutants))
	}
	mutant := public.Mutants[0]
	if mutant.Accepted {
		t.Fatalf("the rejected mutant is Accepted: %+v", mutant)
	}
	if mutant.Probed {
		t.Errorf("mutant %s is rejected and Probed; a mutant that is never executed"+
			" must not carry a probe status a consumer would read as a fact", mutant.DisplayID)
	}
}

func TestFilterInfectedDropsRejectedMutants(t *testing.T) {
	t.Parallel()

	mutants := []Mutant{
		{DisplayID: "aaaa", Accepted: true},
		{DisplayID: "bbbb", Accepted: false},
		{DisplayID: "cccc", Accepted: true},
	}
	for _, test := range []struct {
		name     string
		infected []uint32
		want     []uint32
	}{
		{
			name:     "a rejected index is dropped and the order survives",
			infected: []uint32{0, 1, 2},
			want:     []uint32{0, 2},
		},
		{
			name:     "every index rejected is still a set",
			infected: []uint32{1},
			want:     []uint32{},
		},
		{
			name:     "an index outside the catalogue cannot panic",
			infected: []uint32{0, 99},
			want:     []uint32{0},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := filterInfected(test.infected, mutants)
			if got == nil {
				t.Fatalf("filterInfected(%v) = nil, want a set: nil means no facts at all", test.infected)
			}
			if !slices.Equal(got, test.want) {
				t.Errorf("filterInfected(%v) = %v, want %v", test.infected, got, test.want)
			}
		})
	}

	if got := filterInfected(nil, mutants); got != nil {
		t.Errorf("filterInfected(nil) = %v, want nil", got)
	}
}
