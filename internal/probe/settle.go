// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package probe

import (
	"slices"
	"strconv"
)

type Candidate struct {
	ID       string
	Index    uint32
	Probed   bool
	Binaries []int
}

type Pass struct {
	Binary   int
	Infected []uint32
}

type Decision struct {
	Settled  []string
	Narrowed map[string][]int
}

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

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
