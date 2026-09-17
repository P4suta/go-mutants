// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	goanalysis "github.com/P4suta/go-mutants/goatest/internal/golang"
)

const (
	branchBodyStartLine   = 10
	branchBodyStartColumn = 2
	branchBodyEndLine     = 12
	branchBodyEndColumn   = 16
	branchMutantLine      = 9
	branchMutantColumn    = 4
	timeoutSample         = 2 * time.Second
	timeoutLimit          = 3 * time.Second

	anotherMutantIndex = 3
)

func branchMutant(change func(*gomutants.Mutant)) gomutants.Mutant {
	mutant := gomutants.Mutant{
		ID: "m-1", Line: branchMutantLine, Column: branchMutantColumn,
		Branch: &gomutants.BranchProof{
			BodyStartLine: branchBodyStartLine, BodyStartColumn: branchBodyStartColumn,
			BodyEndLine: branchBodyEndLine, BodyEndColumn: branchBodyEndColumn,
		},
	}
	if change != nil {
		change(&mutant)
	}
	return mutant
}

func TestANarrowedBranchSpanIsTheBodyAMutantOpensAheadOfIt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		change   func(*gomutants.Mutant)
		narrowed bool
	}{
		{name: "a mutant a line above its body", narrowed: true},
		{
			name: "a mutant a column before its body",
			change: func(m *gomutants.Mutant) {
				m.Line, m.Column = branchBodyStartLine, branchBodyStartColumn-1
			},
			narrowed: true,
		},
		{name: "a mutant that gates no branch", change: func(m *gomutants.Mutant) { m.Branch = nil }},
		{
			name:   "a body that starts on line zero",
			change: func(m *gomutants.Mutant) { m.Branch.BodyStartLine = 0 },
		},
		{
			name:   "a body that starts in column zero",
			change: func(m *gomutants.Mutant) { m.Branch.BodyStartColumn = 0 },
		},
		{
			name:   "a body that ends on line zero",
			change: func(m *gomutants.Mutant) { m.Branch.BodyEndLine = 0 },
		},
		{
			name:   "a body that ends in column zero",
			change: func(m *gomutants.Mutant) { m.Branch.BodyEndColumn = 0 },
		},
		{
			name:   "a body that ends on an earlier line",
			change: func(m *gomutants.Mutant) { m.Branch.BodyEndLine = branchBodyStartLine - 1 },
		},
		{
			name: "a body that ends in an earlier column of its line",
			change: func(m *gomutants.Mutant) {
				m.Branch.BodyEndLine = branchBodyStartLine
				m.Branch.BodyEndColumn = branchBodyStartColumn - 1
			},
		},
		{
			name: "a body that opens and closes on one line",
			change: func(m *gomutants.Mutant) {
				m.Branch.BodyEndLine = branchBodyStartLine
				m.Branch.BodyEndColumn = branchBodyStartColumn
			},
			narrowed: true,
		},
		{
			name:   "a mutant past the line its body starts on",
			change: func(m *gomutants.Mutant) { m.Line = branchBodyStartLine + 1 },
		},
		{
			name: "a mutant where its body starts",
			change: func(m *gomutants.Mutant) {
				m.Line, m.Column = branchBodyStartLine, branchBodyStartColumn
			},
		},
		{
			name: "a mutant past the column its body starts in",
			change: func(m *gomutants.Mutant) {
				m.Line, m.Column = branchBodyStartLine, branchBodyStartColumn+1
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			span, narrowed := narrowedBranchSpan(branchMutant(test.change))
			if narrowed != test.narrowed {
				t.Fatalf("narrowedBranchSpan = (%+v, %t), want %t", span, narrowed, test.narrowed)
			}
			if !narrowed {
				if span != (goanalysis.CoverageSpan{}) {
					t.Errorf("a mutant it refused answered with %+v, want no span", span)
				}
				return
			}
			if span.StartLine != branchBodyStartLine || span.StartColumn != branchBodyStartColumn {
				t.Fatalf("narrowedBranchSpan = %+v, want the body the mutant gates", span)
			}
		})
	}
}

func TestAMutationExecutionTimeoutAddsItsSamplesUpToItsLimit(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		limit   time.Duration
		samples []time.Duration
		want    time.Duration
	}{
		{name: "no sample at all", limit: timeoutLimit},
		{name: "one sample under the limit", limit: timeoutLimit, samples: []time.Duration{time.Second}, want: time.Second},
		{
			name: "samples that reach the limit exactly", limit: timeoutLimit,
			samples: []time.Duration{time.Second, timeoutSample}, want: timeoutLimit,
		},
		{
			name: "samples that pass the limit", limit: timeoutLimit,
			samples: []time.Duration{timeoutSample, timeoutSample}, want: timeoutLimit,
		},
		{
			name:    "no limit at all",
			samples: []time.Duration{timeoutSample, timeoutSample}, want: 2 * timeoutSample,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := mutationExecutionTimeout(test.limit, test.samples...); got != test.want {
				t.Fatalf("mutationExecutionTimeout(%s, %v) = %s, want %s",
					test.limit, test.samples, got, test.want)
			}
		})
	}
}

func TestAControlExecutionTimeoutFallsBackToTheLimitWhenNothingWasSampled(t *testing.T) {
	t.Parallel()
	if got := controlExecutionTimeout(timeoutLimit); got != timeoutLimit {
		t.Fatalf("controlExecutionTimeout with no sample = %s, want the limit %s", got, timeoutLimit)
	}
	if got := controlExecutionTimeout(timeoutLimit, time.Second); got != time.Second {
		t.Fatalf("controlExecutionTimeout with one sample = %s, want %s", got, time.Second)
	}
	if got := controlExecutionTimeout(0); got != 0 {
		t.Fatalf("controlExecutionTimeout with no limit and no sample = %s, want none", got)
	}
}

func TestValidatingAMutationCatalogRefusesEveryShapeItCannotAccountFor(t *testing.T) {
	t.Parallel()
	sound := gomutants.Catalog{
		Mutants: []gomutants.Mutant{
			{ID: "m-1", Accepted: true},
			{ID: "m-2"},
		},
		Rejections: []gomutants.Rejection{{ID: "m-2", Diagnostic: "does not compile"}},
	}
	for _, test := range []struct {
		name    string
		change  func(*gomutants.Catalog)
		refuses string
	}{
		{name: "a catalog that accounts for every mutant"},
		{
			name:   "a mutant with no identity",
			change: func(c *gomutants.Catalog) { c.Mutants[0].ID = "" }, refuses: "empty mutant ID",
		},
		{
			name:    "one identity used twice",
			change:  func(c *gomutants.Catalog) { c.Mutants[1].ID = "m-1" },
			refuses: "duplicate mutant m-1",
		},
		{
			name:    "a rejection of a mutant nothing catalogued",
			change:  func(c *gomutants.Catalog) { c.Rejections[0].ID = "m-3" },
			refuses: "absent from the mutation catalog",
		},
		{
			name: "one rejection written twice",
			change: func(c *gomutants.Catalog) {
				c.Rejections = append(c.Rejections, c.Rejections[0])
			},
			refuses: "duplicate rejection m-2",
		},
		{
			name:    "a mutant that is both executable and rejected",
			change:  func(c *gomutants.Catalog) { c.Mutants[1].Accepted = true },
			refuses: "both executable and compile-rejected",
		},
		{
			name:    "a mutant that is neither executable nor rejected",
			change:  func(c *gomutants.Catalog) { c.Rejections = nil },
			refuses: "has no compile rejection",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			catalog := gomutants.Catalog{
				Mutants:    append([]gomutants.Mutant(nil), sound.Mutants...),
				Rejections: append([]gomutants.Rejection(nil), sound.Rejections...),
			}
			if test.change != nil {
				test.change(&catalog)
			}
			err := validateMutationCatalog(catalog)
			switch {
			case test.refuses == "" && err != nil:
				t.Fatalf("validateMutationCatalog refused a sound catalog: %v", err)
			case test.refuses != "" && (err == nil || !strings.Contains(err.Error(), test.refuses)):
				t.Fatalf("validateMutationCatalog reported %v, want it to say %q", err, test.refuses)
			}
		})
	}
}

func TestAValidCheckpointProbeFactCarriesOnlyWhatAMeasurementCan(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		measured  bool
		duration  int64
		infected  []uint32
		wholeTree bool
		want      bool
	}{
		{name: "a measurement of nothing", measured: true, want: true},
		{
			name: "a measurement that took time and saw infections", measured: true,
			duration: 1, infected: []uint32{1, 2}, want: true,
		},
		{name: "a measurement that claims the whole tree", measured: true, wholeTree: true, want: true},
		{name: "no measurement at all", want: true},
		{name: "a duration below zero", measured: true, duration: -1},
		{name: "no measurement that took time", duration: 1},
		{name: "no measurement that saw an infection", infected: []uint32{1}},
		{name: "no measurement that claims the whole tree", wholeTree: true},
		{name: "infections that repeat", measured: true, infected: []uint32{1, 1}},
		{name: "infections out of order", measured: true, infected: []uint32{2, 1}},
		{
			name: "an ascending run that dips at the end", measured: true,
			infected: []uint32{1, 2, 3, 2},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := validCheckpointProbeFact(test.measured, test.duration, test.infected, test.wholeTree)
			if got != test.want {
				t.Fatalf("validCheckpointProbeFact(%t, %d, %v, %t) = %t, want %t",
					test.measured, test.duration, test.infected, test.wholeTree, got, test.want)
			}
		})
	}
}

func TestAMutationProbeIndexFingerprintChangesWithEveryFactItCovers(t *testing.T) {
	t.Parallel()
	base := gomutants.Catalog{Mutants: []gomutants.Mutant{
		{Index: 1, ID: "m-1", Accepted: true, Probed: true},
		{Index: 2, ID: "m-2"},
	}}
	fingerprint := mutationProbeIndexFingerprint(base)
	if fingerprint == "" {
		t.Fatal("a catalog of two mutants has no fingerprint")
	}
	for _, test := range []struct {
		name   string
		change func(*gomutants.Catalog)
	}{
		{name: "another index", change: func(c *gomutants.Catalog) { c.Mutants[0].Index = anotherMutantIndex }},
		{name: "another identity", change: func(c *gomutants.Catalog) { c.Mutants[0].ID = "m-3" }},
		{name: "one fewer mutant", change: func(c *gomutants.Catalog) { c.Mutants = c.Mutants[:1] }},
		{name: "a mutant nothing accepts", change: func(c *gomutants.Catalog) { c.Mutants[0].Accepted = false }},
		{name: "a mutant nothing probed", change: func(c *gomutants.Catalog) { c.Mutants[0].Probed = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			catalog := gomutants.Catalog{Mutants: append([]gomutants.Mutant(nil), base.Mutants...)}
			test.change(&catalog)
			if mutationProbeIndexFingerprint(catalog) == fingerprint {
				t.Fatalf("changing %s left the fingerprint at %q", test.name, fingerprint)
			}
		})
	}
	reordered := gomutants.Catalog{Mutants: []gomutants.Mutant{base.Mutants[1], base.Mutants[0]}}
	if mutationProbeIndexFingerprint(reordered) != fingerprint {
		t.Fatal("the fingerprint moved when the same mutants were listed in another order")
	}
}
