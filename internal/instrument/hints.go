// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"strconv"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/mutation"
)

type Hints map[string]discover.Guard

func HintsOf(located []discover.Located) (Hints, error) {
	hints := make(Hints, len(located))
	for _, l := range located {
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
