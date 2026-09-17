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

func candidate(id string, index uint32, binaries ...int) probe.Candidate {
	return probe.Candidate{ID: id, Index: index, Probed: true, Binaries: binaries}
}

func measured(binary int, infected ...uint32) probe.Pass {
	if infected == nil {
		infected = []uint32{}
	}
	return probe.Pass{Binary: binary, Infected: infected}
}

func settle(t *testing.T, candidates []probe.Candidate, passes []probe.Pass) probe.Decision {
	t.Helper()

	decision, err := probe.Settle(candidates, passes)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	return decision
}

func TestAMutantNoCoveringBinaryNamedIsASurvivor(t *testing.T) {
	t.Parallel()

	decision := settle(t,
		[]probe.Candidate{candidate("quiet", 7, 0, 1), candidate("loud", 8, 0, 1)},
		[]probe.Pass{measured(0, 8), measured(1)},
	)
	if !slices.Equal(decision.Settled, []string{"quiet"}) {
		t.Errorf("settled = %v, want the mutant no binary named", decision.Settled)
	}
	want := map[string][]int{"loud": {0}}
	if !maps.EqualFunc(decision.Narrowed, want, slices.Equal) {
		t.Errorf("narrowed = %v, want %v", decision.Narrowed, want)
	}
}

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
