// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/token"
	"strings"
	"testing"
)

var terminationCases = []struct {
	name     string
	rule     string
	original string
	source   string
	want     string
	absent   bool
}{
	{
		name: "a negated counting condition runs away from its bound",
		rule: "negate-loop-condition", original: "i < n",
		want: TerminationUnbounded,
		source: `package pkg

func Sum(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += i
	}
	return total
}
`,
	},
	{
		name: "a negated countdown runs away too",
		rule: "negate-loop-condition", original: "i > 0",
		want: TerminationUnbounded,
		source: `package pkg

func Down(n int) int {
	total := 0
	for i := n; i > 0; i-- {
		total++
	}
	return total
}
`,
	},
	{
		name: "moving the boundary by one changes how many iterations run",
		rule: "lt-to-le", original: "<",
		want: TerminationBounded,
		source: `package pkg

func Sum(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += i
	}
	return total
}
`,
	},
	{
		name: "a countdown that includes zero still reaches minus one",
		rule: "gt-to-ge", original: ">",
		want: TerminationBounded,
		source: `package pkg

func Down(n int) int {
	total := 0
	for i := n; i > 0; i-- {
		total++
	}
	return total
}
`,
	},
	{
		name: "an edit in the body leaves the bound alone",
		rule: "add-to-sub", original: "+",
		want: TerminationBounded,
		source: `package pkg

func Sum(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total = total + i
	}
	return total
}
`,
	},
	{
		name: "a range loop is not a counted loop",
		rule: "add-to-sub", original: "+",
		absent: true,
		source: `package pkg

func Sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total = total + x
	}
	return total
}
`,
	},
	{
		name: "a loop whose body moves the bound is not one this phase reads",
		rule: "negate-loop-condition", original: "i < n",
		absent: true,
		source: `package pkg

func Grow(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		n = n - 1
		total++
	}
	return total
}
`,
	},
	{
		name: "a loop whose body moves the variable is not one this phase reads",
		rule: "negate-loop-condition", original: "i < n",
		absent: true,
		source: `package pkg

func Skip(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		i = i + 1
		total++
	}
	return total
}
`,
	},
	{
		name: "a loop with no condition has no measure to reason about",
		rule: "add-to-sub", original: "+",
		absent: true,
		source: `package pkg

func Forever(n int) int {
	total := 0
	for {
		total = total + n
		if total > 10 {
			return total
		}
	}
}
`,
	},
	{
		name: "an equality edit in a loop body leaves the counted loop counting",
		rule: "eq-to-neq", original: "==",
		want: TerminationBounded,
		source: `package pkg

func Find(xs []int, want int) int {
	for i := 0; i < len(xs); i++ {
		if xs[i] == want {
			return i
		}
	}
	return -1
}
`,
	},
	{
		name: "an edit outside every loop proves nothing",
		rule: "gt-to-ge", original: ">",
		absent: true,
		source: `package pkg

func Positive(v int) bool {
	return v > 0
}
`,
	},
	{
		name: "a reversed step runs away from its bound",
		rule: "incr-to-decr", original: "++",
		want: TerminationUnbounded,
		source: `package pkg

func Sum(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += i
	}
	return total
}
`,
	},
	{
		name: "a deleted step never reaches its bound",
		rule: "delete-incdec", original: "i++",
		want: TerminationUnbounded,
		source: `package pkg

func Sum(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += i
	}
	return total
}
`,
	},
	{
		name: "a reversed stride runs away from its bound",
		rule: "add-assign-to-sub-assign", original: "+=",
		want: TerminationUnbounded,
		source: `package pkg

func Stride(n int) int {
	total := 0
	for i := 0; i < n; i += 2 {
		total = total + 1
	}
	return total
}
`,
	},
	{
		name: "a reversed countdown stride runs away too",
		rule: "sub-assign-to-add-assign", original: "-=",
		want: TerminationUnbounded,
		source: `package pkg

func Countdown(n int) int {
	total := 0
	for i := n; i > 0; i -= 2 {
		total = total + 1
	}
	return total
}
`,
	},
	{
		name: "a deleted step in the body is not the loop's step",
		rule: "delete-assignment", original: "total = total + 1",
		want: TerminationBounded,
		source: `package pkg

func Stride(n int) int {
	total := 0
	for i := 0; i < n; i += 2 {
		total = total + 1
	}
	return total
}
`,
	},
}

func TestTerminationIsProvedRatherThanTimedOut(t *testing.T) {
	t.Parallel()

	for _, testCase := range terminationCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			found := scanSource(t, testCase.source)
			var proofs []*TerminationProof
			for _, candidate := range found.candidates {
				if candidate.Rule.Name == testCase.rule && candidate.Original == testCase.original {
					proofs = append(proofs, candidate.Termination)
				}
			}
			if len(proofs) == 0 {
				t.Fatalf("the scan found no %s candidate replacing %q; it found %v",
					testCase.rule, testCase.original, found.rules())
			}
			for _, proof := range proofs {
				if testCase.absent {
					if proof != nil {
						t.Errorf("this phase proved %q about a loop it should not read: %s",
							proof.Verdict, proof.Reason)
					}
					continue
				}
				if proof == nil {
					t.Fatalf("no proof, want %s", testCase.want)
				}
				if proof.Verdict != testCase.want {
					t.Errorf("verdict = %q, want %q (%s)", proof.Verdict, testCase.want, proof.Reason)
				}
				if strings.TrimSpace(proof.Reason) == "" {
					t.Errorf("a %s verdict with no reason is one nobody can check", proof.Verdict)
				}
				if proof.LoopLine < 1 {
					t.Errorf("the proof addresses line %d", proof.LoopLine)
				}
			}
		})
	}
}

func TestEveryProofNamesTheLoopItIsAbout(t *testing.T) {
	t.Parallel()

	found := scanSource(t, `package pkg

func Sum(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += i
	}
	return total
}
`)
	proved := 0
	for _, candidate := range found.candidates {
		if candidate.Termination == nil {
			continue
		}
		proved++
		if candidate.Termination.LoopLine != 5 {
			t.Errorf("%s proof addresses line %d, and the `for` is on line 5",
				candidate.Rule.Name, candidate.Termination.LoopLine)
		}
		if candidate.Termination.LoopColumn < 1 {
			t.Errorf("%s proof addresses column %d", candidate.Rule.Name, candidate.Termination.LoopColumn)
		}
	}
	if proved == 0 {
		t.Fatal("nothing in a counted loop was proved, so this checks no coordinates")
	}
}

func TestReversingOrDeletingTheStepIsUnbounded(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name       string
		step       int
		comparison string
		bounded    bool
	}{
		{name: "counting up towards an upper bound", step: 1, comparison: "<", bounded: true},
		{name: "counting up towards an inclusive upper bound", step: 1, comparison: "<=", bounded: true},
		{name: "counting down towards a lower bound", step: -1, comparison: ">", bounded: true},
		{name: "counting down towards an inclusive lower bound", step: -1, comparison: ">=", bounded: true},
		{name: "counting down away from an upper bound", step: -1, comparison: "<", bounded: false},
		{name: "counting up away from a lower bound", step: 1, comparison: ">", bounded: false},
		{name: "a step of nothing", step: 0, comparison: "<", bounded: false},
		{name: "a striding step towards its bound", step: 4, comparison: "<", bounded: true},
		{name: "a striding step away from its bound", step: -4, comparison: "<", bounded: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			loop := inductionLoop{step: testCase.step, comparison: comparisonToken(t, testCase.comparison)}
			if got := loop.progresses(); got != testCase.bounded {
				t.Errorf("a loop stepping %+d against %s progresses = %t, want %t",
					testCase.step, testCase.comparison, got, testCase.bounded)
			}
		})
	}
}

func comparisonToken(t *testing.T, spelling string) token.Token {
	t.Helper()
	switch spelling {
	case "<":
		return token.LSS
	case "<=":
		return token.LEQ
	case ">":
		return token.GTR
	case ">=":
		return token.GEQ
	default:
		t.Fatalf("the table spells an operator this test does not read: %q", spelling)
		return token.ILLEGAL
	}
}

func TestANegationAndAMirrorAreEachOthersInverse(t *testing.T) {
	t.Parallel()

	for _, op := range []token.Token{token.LSS, token.LEQ, token.GTR, token.GEQ} {
		if got := negatedComparison(negatedComparison(op)); got != op {
			t.Errorf("negating %s twice gives %s", op, got)
		}
		if got := mirrored(mirrored(op)); got != op {
			t.Errorf("mirroring %s twice gives %s", op, got)
		}
		if !isOrdering(negatedComparison(op)) {
			t.Errorf("negating %s gives %s, which is not an ordering", op, negatedComparison(op))
		}
	}
	for _, op := range []token.Token{token.EQL, token.NEQ, token.ADD} {
		if got := negatedComparison(op); got != token.ILLEGAL {
			t.Errorf("negating %s gives %s, and this table only knows orderings", op, got)
		}
	}
}

func TestATaglessSwitchCaseIsProvedLikeAnIf(t *testing.T) {
	t.Parallel()

	found := scanSource(t, `package pkg

func Pick(a, b int) int {
	switch {
	case a <= b:
		return 1
	}
	return 0
}
`)
	var proved int
	for _, candidate := range found.candidates {
		if candidate.Rule.Name != "le-to-lt" {
			continue
		}
		proved++
		proof := candidate.Branch
		if proof == nil {
			t.Fatalf("a narrowing edit on a tagless switch label carries no branch proof")
		}
		if proof.BodyStartLine != 6 || proof.BodyEndLine != 6 {
			t.Errorf("the proof spans lines %d..%d, and the clause's body is line 6 alone",
				proof.BodyStartLine, proof.BodyEndLine)
		}
		if proof.BodyStartColumn >= proof.BodyEndColumn {
			t.Errorf("the proof spans columns %d..%d, which covers nothing",
				proof.BodyStartColumn, proof.BodyEndColumn)
		}
	}
	if proved == 0 {
		t.Fatalf("the scan found no le-to-lt candidate in a tagless switch label; it found %v", found.rules())
	}
}

func TestACaseClauseWithNoBodyIsNotProved(t *testing.T) {
	t.Parallel()

	found := scanSource(t, `package pkg

func Pick(a, b int) int {
	switch {
	case a <= b:
	}
	return 0
}
`)
	for _, candidate := range found.candidates {
		if candidate.Rule.Name == "le-to-lt" && candidate.Branch != nil {
			t.Errorf("an empty clause was proved: %+v", candidate.Branch)
		}
	}
}
