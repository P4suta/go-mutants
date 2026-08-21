// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"strconv"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// Hints are the rewrite sites discovery chose, indexed by full mutant id.
//
// Every catalogued mutant needs one. Which guard form an edit takes — and, for
// a declaration, which names it has to hoist and how to spell their types — are
// questions only a type checker can answer, and this package deliberately parses
// the snapshot without type-checking it: it is a byte rewriter, and the phase
// that already paid for go/types answers them once and hands the answers down.
// [discover.Guard] is that answer and documents the contract in full; this type
// is nothing but the index that gets it here.
//
// The key is the full 64 hex character id rather than the coordinates, because
// that is the one handle a mutant keeps from discovery through the catalogue's
// deduplication into the runtime's dense index. A mutant the catalogue holds and
// this map does not is a [CodeMissingGuard] refusal rather than a guess: guessing
// is exactly what the hint exists to stop.
type Hints map[string]discover.Guard

// HintsOf indexes a discovery pass by mutant id.
//
// Candidates the catalogue later drops as duplicates are indexed too. They cost
// one map entry each and nothing reads them, which is the right trade against
// making this depend on the deduplication having already happened: the caller
// would then have to keep the two in step, and the failure mode of getting it
// wrong is a missing hint for a mutant that is in the tree.
//
// The first hint for an id wins. Two candidates sharing an id are the same
// path, rule, span and text, so they resolve to the same site, and taking the
// first keeps the result a function of the discovery order rather than of a map
// iteration in the unforeseen case where they somehow do not.
func HintsOf(located []discover.Located) (Hints, error) {
	hints := make(Hints, len(located))
	for _, l := range located {
		// The promoted method of the embedded candidate: a Located has no
		// identity of its own, only the coordinates a human reads it by.
		id, err := l.ID()
		if err != nil {
			return nil, &Error{
				Code: CodeOptions,
				Message: "cannot index the guard hint of the candidate at " +
					strconv.Quote(l.Path) + " " + l.Span.String(),
				Err: err,
			}
		}
		if _, seen := hints[id]; !seen {
			hints[id] = l.Guard
		}
	}
	return hints, nil
}

// guardFor returns one mutant's hint, or the refusal that names what is
// missing.
func (h Hints) guardFor(m mutation.Mutant, srcPath string) (discover.Guard, error) {
	guard, ok := h[m.ID]
	if !ok {
		return discover.Guard{}, &Error{
			Code: CodeMissingGuard,
			Message: "mutant " + m.DisplayID + " at " + strconv.Quote(srcPath) + " " + m.Span.String() +
				" has no guard hint: instrumentation cannot choose a rewrite site on its own, " +
				"because which form an edit takes is a question about types",
		}
	}
	return guard, nil
}
