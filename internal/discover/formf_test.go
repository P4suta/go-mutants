// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"testing"
)

// Form F exists because two of Go's statement slots hold a *simple* statement
// rather than any statement. `for i := 0; i < n; if __gm.M[3] { … }` does not
// parse, and neither does an `if` whose initialiser is a block -- so every edit
// in a `for` post statement or an `if`, `switch` or `for` initialiser was a
// recorded refusal, which over this repository was most of them. A call is an
// expression, an expression alone is an expression statement, and an expression
// statement is simple: the guard goes inside a closure and the closure is
// called where the statement was.

// TestAForPostStatementIsNowASite is the slot the form was written for, and the
// one that held the most refusals.
func TestAForPostStatementIsNowASite(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Sum(values []int) int {
	total := 0
	for i := 0; i < len(values); i += 2 {
		total += values[i]
	}
	return total
}
`)
	if !got.has("add-assign-to-sub-assign", "+=", "-=") {
		t.Errorf("scan found %v, want the post statement's compound assignment", got.rules())
	}
	found := false
	for _, candidate := range got.candidates {
		if candidate.Original != "+=" || candidate.Line != 5 {
			continue
		}
		found = true
		if candidate.Guard.Form != GuardFormF {
			t.Errorf("the post statement uses %q, want %q", candidate.Guard.Form, GuardFormF)
		}
	}
	if !found {
		t.Fatalf("scan found %v, want a candidate in the post statement", got.rules())
	}
	if got.hasSkip(SkipUnnameableDeclType) {
		t.Errorf("scan still records %v for a post statement", got.skips())
	}
}

// TestAnAssignmentInAnInitialiserIsNowASite is the other slot, and it is the
// commonest shape in real Go of the two: `if err = f(); err != nil`.
func TestAnAssignmentInAnInitialiserIsNowASite(t *testing.T) {
	t.Parallel()

	for name, source := range map[string]string{
		"an if initialiser": `package pkg

func Run(values []int) int {
	total := 0
	if total = values[0] + 1; total > 0 {
		return total
	}
	return 0
}
`,
		"a switch initialiser": `package pkg

func Run(values []int) int {
	total := 0
	switch total = values[0] + 1; {
	case total > 0:
		return total
	}
	return 0
}
`,
		"a for initialiser": `package pkg

func Run(values []int, n int) int {
	total := 0
	for total = n + 1; total < 10; total++ {
		total += values[0]
	}
	return total
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := scanSource(t, source)
			if !got.has("add-to-sub", "+", "-") {
				t.Errorf("scan found %v, want the initialiser's addition", got.rules())
			}
			if got.hasSkip(SkipUnnameableDeclType) {
				t.Errorf("scan still records %v for an initialiser", got.skips())
			}
		})
	}
}

// TestACommunicationClauseIsNotAFormFSite pins the one slot that refuses a call
// as well as a block. A `case` of a `select` has to be a send or a receive, and
// a call is neither -- so the *statement* is no site of this form, and what
// carries an edit inside it is the expression, through Form E.
func TestACommunicationClauseIsNotAFormFSite(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Send(ch chan int, n int) string {
	select {
	case ch <- n + 1:
		return "sent"
	default:
		return "full"
	}
}
`)
	for _, candidate := range got.candidates {
		if candidate.Guard.Form == GuardFormF {
			t.Errorf("%s over %q uses Form F in a communication clause, where a call is "+
				"neither a send nor a receive", candidate.Rule.Name, candidate.Original)
		}
	}
	if !got.has("add-to-sub", "+", "-") {
		t.Errorf("scan found %v, want the sent value's addition", got.rules())
	}
}

// TestAnOrdinaryStatementStillUsesFormS is the ordering promise: Form F is
// reached only where a block is not legal, so every statement a block *is*
// legal for is covered by exactly the form that covered it before.
func TestAnOrdinaryStatementStillUsesFormS(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Run(values []int) int {
	total := 0
	total += values[0]
	return total
}
`)
	for _, candidate := range got.candidates {
		if candidate.Guard.Form == GuardFormF {
			t.Errorf("%s over %q uses Form F where a block is legal",
				candidate.Rule.Name, candidate.Original)
		}
	}
}
