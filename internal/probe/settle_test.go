// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package probe_test

import (
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/probe"
)

// The rule is pure, so every shape of evidence a run could produce is reachable
// here rather than only the shapes a fixture happens to make. That is most of
// why this is a package: the three ways it stays conservative — an unprobed
// mutant, a binary that established nothing, a mutant nothing covers — are each
// one line of a run and each a verdict nobody could check from outside.

// candidate is one probed mutant covered by the named binaries.
func candidate(id string, index uint32, binaries ...int) probe.Candidate {
	return probe.Candidate{ID: id, Index: index, Probed: true, Binaries: binaries}
}

// measured is a pass that ran and named these indices. An empty list is the
// fact a caller acts on most: the binary ran and could rule everything out.
func measured(binary int, infected ...uint32) probe.Pass {
	if infected == nil {
		infected = []uint32{}
	}
	return probe.Pass{Binary: binary, Infected: infected}
}

// settle runs the rule and fails the test on a refusal.
func settle(t *testing.T, candidates []probe.Candidate, passes []probe.Pass) probe.Decision {
	t.Helper()

	decision, err := probe.Settle(candidates, passes)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	return decision
}

// TestAMutantNoCoveringBinaryNamedIsASurvivor is the saving the layer exists
// for.
func TestAMutantNoCoveringBinaryNamedIsASurvivor(t *testing.T) {
	t.Parallel()

	decision := settle(t,
		[]probe.Candidate{candidate("quiet", 7, 0, 1), candidate("loud", 8, 0, 1)},
		[]probe.Pass{measured(0, 8), measured(1)},
	)
	if !slices.Equal(decision.Settled, []string{"quiet"}) {
		t.Errorf("settled = %v, want the mutant no binary named", decision.Settled)
	}
	// The loud one is narrowed rather than settled: binary 1 ruled it out and
	// binary 0 did not, so only binary 0 is worth running it against.
	want := map[string][]int{"loud": {0}}
	if !maps.EqualFunc(decision.Narrowed, want, slices.Equal) {
		t.Errorf("narrowed = %v, want %v", decision.Narrowed, want)
	}
}

// TestAMutantEveryCoveringBinaryNamedIsNotNarrowed keeps the second saving
// honest: narrowing to the whole covering set is no narrowing, and reporting it
// would make a caller rewrite a run for nothing.
func TestAMutantEveryCoveringBinaryNamedIsNotNarrowed(t *testing.T) {
	t.Parallel()

	decision := settle(t,
		[]probe.Candidate{candidate("everywhere", 3, 0, 1)},
		[]probe.Pass{measured(0, 3), measured(1, 3)},
	)
	if len(decision.Settled) != 0 {
		t.Errorf("settled = %v, and both binaries named it", decision.Settled)
	}
	if len(decision.Narrowed) != 0 {
		t.Errorf("narrowed = %v, and there was nothing to narrow away", decision.Narrowed)
	}
}

// TestAnUnprobedMutantIsNeverSettled is the first of the three conservative
// clauses, and the one a consumer's whole fallback rests on.
//
// A mutant with no probe form left its file untouched, so the probe tree
// compiled without a call naming it and its absence from every log is the
// absence of a question rather than the answer to one.
func TestAnUnprobedMutantIsNeverSettled(t *testing.T) {
	t.Parallel()

	unprobed := probe.Candidate{ID: "formless", Index: 4, Probed: false, Binaries: []int{0}}
	decision := settle(t, []probe.Candidate{unprobed}, []probe.Pass{measured(0)})
	if len(decision.Settled) != 0 {
		t.Errorf("settled = %v, and nothing in the probe tree could ever have named it", decision.Settled)
	}
	if len(decision.Narrowed) != 0 {
		t.Errorf("narrowed = %v, and an unprobed mutant's covering set is not evidence either",
			decision.Narrowed)
	}
}

// TestABinaryThatEstablishedNothingIsNotSilence is the second, and the
// difference between two answers that are the same bytes.
//
// A pass that failed, timed out or could not write its log carries no infected
// set. Read as "this binary named nothing" it would settle every mutant it
// covers; read as "this binary is unknown" it settles none of them.
func TestABinaryThatEstablishedNothingIsNotSilence(t *testing.T) {
	t.Parallel()

	decision := settle(t,
		[]probe.Candidate{candidate("m", 1, 0, 1)},
		[]probe.Pass{{Binary: 0}, measured(1)},
	)
	if len(decision.Settled) != 0 {
		t.Errorf("settled = %v, and binary 0 established nothing", decision.Settled)
	}
	if want := map[string][]int{"m": {0}}; !maps.EqualFunc(decision.Narrowed, want, slices.Equal) {
		t.Errorf("narrowed = %v, want %v: the unknown binary is kept and the measured one dropped",
			decision.Narrowed, want)
	}
}

// TestAMutantNothingCoversIsNeverSettled is the third, and the one that is
// vacuously true in exactly the wrong direction.
//
// "No covering binary named it" is true of a mutant with no covering binaries,
// and settling it would report a survivor nothing ever looked at. Coverage
// settles those before this rule is asked; this refuses them anyway, because a
// rule that depends on its caller having already been careful is a rule with an
// unwritten precondition.
func TestAMutantNothingCoversIsNeverSettled(t *testing.T) {
	t.Parallel()

	decision := settle(t,
		[]probe.Candidate{{ID: "unreached", Index: 2, Probed: true}},
		[]probe.Pass{measured(0)},
	)
	if len(decision.Settled) != 0 {
		t.Errorf("settled = %v, and no binary covers it, so no binary looked", decision.Settled)
	}
}

// TestALogNamingAnUnknownIndexDiscardsEveryFact is the fail-closed case.
//
// The log's header carries the catalogue's digest and the reader bounds every
// index by its size, so an index that survives both and still names nothing
// means the catalogue and the tree came from different passes. Keeping the
// facts that do parse would be keeping a subset of the truth wearing the shape
// of the whole of it.
func TestALogNamingAnUnknownIndexDiscardsEveryFact(t *testing.T) {
	t.Parallel()

	_, err := probe.Settle(
		[]probe.Candidate{candidate("m", 1, 0)},
		[]probe.Pass{measured(0, 1, 99)},
	)
	if !errors.Is(err, probe.ErrInconsistent) {
		t.Fatalf("Settle = %v, want %v", err, probe.ErrInconsistent)
	}
	var coded *probe.Error
	if !errors.As(err, &coded) || coded.Code != probe.CodeInconsistent {
		t.Errorf("the refusal carries %v, want %s", err, probe.CodeInconsistent)
	}
}

// TestNothingToProbeIsNotAFailure tells the two empty runs apart from the one
// that went wrong.
func TestNothingToProbeIsNotAFailure(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name       string
		candidates []probe.Candidate
		passes     []probe.Pass
	}{
		{"no mutants", nil, []probe.Pass{measured(0)}},
		{"no binaries", []probe.Candidate{candidate("m", 1, 0)}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			_, err := probe.Settle(c.candidates, c.passes)
			var coded *probe.Error
			if !errors.As(err, &coded) || coded.Code != probe.CodeNothingToProbe {
				t.Errorf("Settle = %v, want %s", err, probe.CodeNothingToProbe)
			}
		})
	}
}

// TestACoverageOffRunSettlesNothing is the shape a run without coverage
// produces, and it is the "nothing covers it" clause wearing another hat.
//
// With coverage off every mutant is measured against every binary and none
// carries a covering set, so there is no intersection to empty. A rule that
// treated nil as "every binary" would settle a mutant on the strength of passes
// nobody had matched to it.
func TestACoverageOffRunSettlesNothing(t *testing.T) {
	t.Parallel()

	decision := settle(t,
		[]probe.Candidate{{ID: "m", Index: 1, Probed: true}},
		[]probe.Pass{measured(0), measured(1)},
	)
	if len(decision.Settled) != 0 || len(decision.Narrowed) != 0 {
		t.Errorf("decision = %+v, want nothing settled and nothing narrowed", decision)
	}
}

// TestEveryCodeIsUniqueAndInTheBlock keeps the numbering this package owns.
func TestEveryCodeIsUniqueAndInTheBlock(t *testing.T) {
	t.Parallel()

	seen := map[probe.Code]bool{}
	for _, code := range probe.Codes() {
		if seen[code] {
			t.Errorf("code %s is declared twice", code)
		}
		seen[code] = true
		if len(code) != 7 || code[:5] != "GOM71" {
			t.Errorf("code %s is outside this package's 71xx block", code)
		}
	}
	if len(seen) == 0 {
		t.Error("this package declares no codes at all")
	}
}
