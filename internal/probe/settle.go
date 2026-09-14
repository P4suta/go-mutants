// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package probe

import (
	"slices"
	"strconv"
)

// A Candidate is one mutant a run is about to execute, as the rule needs to see
// it.
//
// It is deliberately not [execute.MutantRun]: the rule needs four fields and
// that type carries a dozen, and a package whose whole point is that it decides
// nothing else should not be able to read a timeout.
type Candidate struct {
	// ID is the mutant's identity, which is how a verdict is matched back to
	// the run it belongs to.
	ID string
	// Index is the catalogue's dense index, which is what a log holds.
	Index uint32
	// Probed says whether the probe tree speaks for this mutant at all. A
	// mutant with no form left its file untouched, so its absence from every
	// log is not evidence; only internal/instrument can answer this, and it
	// arrives here already answered.
	Probed bool
	// Binaries are the indices of the test binaries that cover this mutant,
	// into the same slice [Pass.Binary] indexes. Nil means every binary, which
	// is what a run with coverage off produces.
	Binaries []int
}

// A Pass is what one probe pass established about one test binary.
type Pass struct {
	// Binary is the index of the test binary this pass ran, into the run's own
	// slice of them.
	Binary int
	// Infected are the catalogue indices this pass could not rule out, sorted
	// ascending and distinct, exactly as internal/execute reports them.
	//
	// It is non-nil exactly when the pass was a measurement, the empty set
	// included. A nil Infected is a pass that established nothing, and the rule
	// reads it as "this binary is unknown" rather than as "this binary saw
	// nothing" — which is the same bytes and the opposite meaning.
	Infected []uint32
}

// A Decision is what the rule made of one run's worth of evidence.
type Decision struct {
	// Settled are the mutants no covering binary could observe, by identity.
	// They are survivors, and the run may report them without starting a
	// process for either.
	Settled []string
	// Narrowed are the mutants the run still has to execute, by identity, each
	// with the binaries worth executing them against. A mutant whose covering
	// set could not be narrowed is absent: nothing was learned about it, and
	// the caller leaves its run as it found it.
	Narrowed map[string][]int
}

// Settle applies the rule to one run's candidates and one run's passes.
//
// The two halves of the answer are both savings and they are not the same
// saving. A settled mutant costs no process at all. A narrowed one costs the
// binaries that named it and not the ones that did not — which is where most of
// the saving lives on a module with several test packages, because a mutant in
// one package is typically named by one binary and covered by several.
//
// A total is `total`, which is how many mutants the caller had; it is passed in
// rather than derived so that the refusal for an empty run is this package's
// rather than the caller's, and so that the two reasons a run has nothing to
// settle are told apart.
//
// An index no candidate holds is [ErrInconsistent], and it discards the whole
// decision rather than the offending fact. A log whose header matched the
// catalogue's digest and whose indices are inside its size and which still
// names nothing has to have been written by a tree built from another pass —
// and a repaired set is one nobody can vouch for, so the caller gets no facts
// and measures everything.
func Settle(candidates []Candidate, passes []Pass) (Decision, error) {
	if len(candidates) == 0 || len(passes) == 0 {
		return Decision{}, &Error{
			Code: CodeNothingToProbe,
			Message: "nothing to probe: " + countNoun(len(candidates), "mutant") +
				" and " + countNoun(len(passes), "test binary"),
		}
	}
	known := make(map[uint32]bool, len(candidates))
	for _, candidate := range candidates {
		known[candidate.Index] = true
	}
	// Which binaries were measured, and which mutants each of them named.
	measured := make(map[int]map[uint32]bool, len(passes))
	for _, pass := range passes {
		if pass.Infected == nil {
			continue
		}
		named := make(map[uint32]bool, len(pass.Infected))
		for _, index := range pass.Infected {
			if !known[index] {
				return Decision{}, &Error{
					Code: CodeInconsistent,
					Message: "the infection log of test binary " + strconv.Itoa(pass.Binary) +
						" names mutant index " + strconv.FormatUint(uint64(index), 10) +
						", which this catalogue does not hold",
					Err: ErrInconsistent,
				}
			}
			named[index] = true
		}
		measured[pass.Binary] = named
	}

	decision := Decision{Narrowed: map[string][]int{}}
	for _, candidate := range candidates {
		covering := candidate.Binaries
		if !candidate.Probed || len(covering) == 0 {
			// A mutant nothing speaks for, and a mutant nothing covers. The
			// second is the dangerous one: an empty intersection is vacuously
			// "no binary named it", which reads exactly like "every covering
			// binary looked and saw nothing".
			continue
		}
		keep := make([]int, 0, len(covering))
		for _, binary := range covering {
			named, ran := measured[binary]
			if !ran || named[candidate.Index] {
				keep = append(keep, binary)
			}
		}
		if len(keep) == 0 {
			decision.Settled = append(decision.Settled, candidate.ID)
			continue
		}
		if len(keep) < len(covering) {
			decision.Narrowed[candidate.ID] = keep
		}
	}
	slices.Sort(decision.Settled)
	return decision, nil
}

// countNoun is "1 mutant" or "3 mutants", so that a refusal reads as a
// sentence. It is this package's own because internal/probe imports nothing
// from this module: the rule is pure, and a dependency on a console helper
// would be the first thing to make it otherwise.
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
