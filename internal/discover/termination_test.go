// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/token"
	"strings"
	"testing"
)

// terminationCases are loops and the edits that do or do not stop them
// stopping.
//
// Every `want` here is reasoned about rather than observed, which is the point:
// the whole reason to prove this is that observing it costs a per-mutant budget
// twice. A case whose answer somebody had to run to find out would be a case
// this table cannot state.
var terminationCases = []struct {
	name string
	// rule and original identify the candidate in the scan, so that a source
	// holding several mutants can be asked about one.
	rule     string
	original string
	source   string
	want     string
	// absent says the phase should prove nothing, which is the honest answer
	// for a loop it does not recognise.
	absent bool
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
}

// TestTerminationIsProvedRatherThanTimedOut is the table.
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

// TestEveryProofNamesTheLoopItIsAbout keeps the coordinates usable.
//
// A proof a reader cannot point at is one they have to take on trust, and the
// whole reason to publish this rather than keep it internal is that somebody
// meeting a timeout should be able to see which loop it was.
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

// TestAForPostStatementHasNoCandidateToday records why the step arm of
// [fileScan.applyToLoop] is unreachable through a scan, and is the Red for the
// day that changes.
//
// A statement in a `for` post is refused by the guard chooser -- a block is not
// legal Go there -- so `i++` and `i += 2` in a post produce no candidate at
// all, and the edits that most obviously stop a loop stopping are edits
// go-mutants cannot currently make. The reasoning for them is written and
// tested below rather than left for later, because the shape of the answer is
// not what is missing.
//
// When a form that can express a post statement lands, this test fails, and the
// three cases it is standing in for move into the table above.
func TestAForPostStatementHasNoCandidateToday(t *testing.T) {
	t.Parallel()

	found := scanSource(t, `package pkg

func Stride(n int) int {
	total := 0
	for i := 0; i < n; i += 2 {
		total = total + 1
	}
	return total
}
`)
	for _, candidate := range found.candidates {
		if candidate.Original == "+=" || candidate.Original == "i += 2" {
			t.Fatalf("a `for` post statement now produces %s;\n"+
				"\tmove the step cases out of TestReversingOrDeletingTheStepIsUnbounded\n"+
				"\tand into terminationCases, where a scan will drive them",
				candidate.Rule.String())
		}
	}
	for _, site := range found.sites {
		if site.Reason == SkipUnnameableDeclType {
			return
		}
	}
	t.Fatalf("the post statement produced neither a candidate nor an %s skip;\n"+
		"\tsomething else changed and this test no longer says what it means",
		SkipUnnameableDeclType)
}

// TestReversingOrDeletingTheStepIsUnbounded is the step arm, reasoned about
// directly.
//
// It calls the decision rather than driving a scan, because a scan cannot
// reach it -- see the test above. What it pins is the arithmetic: a loop whose
// variable moves towards its bound stops, and one whose variable moves away
// from it or does not move at all does not.
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

// comparisonToken reads one operator the way the table spells it.
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

// TestANegationAndAMirrorAreEachOthersInverse pins the two tables the decision
// is made of.
//
// They are four lines each and the sort of thing a reader skims, which is why
// they are checked: a negation table with one row transposed produces a proof
// that is confidently wrong, and a confidently wrong proof is worse than none.
func TestANegationAndAMirrorAreEachOthersInverse(t *testing.T) {
	t.Parallel()

	for _, op := range []token.Token{token.LSS, token.LEQ, token.GTR, token.GEQ} {
		if got := negatedComparison(negatedComparison(op)); got != op {
			t.Errorf("negating %s twice gives %s", op, got)
		}
		if got := mirrored(mirrored(op)); got != op {
			t.Errorf("mirroring %s twice gives %s", op, got)
		}
		// Negating an ordering has to produce an ordering, or the loop reader
		// would refuse a condition it had itself produced.
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

// TestATaglessSwitchCaseIsProvedLikeAnIf is the other half of the case-label
// change.
//
// A tagless switch's label is exactly `bool` -- the implicit tag is the typed
// constant `true` -- so it is a condition in the same sense an `if`'s is, and
// the branch proof's lemma holds over it unchanged: a narrowing edit makes the
// clause fire less often, so a test during which none of its statements ran
// could not have told the two programs apart.
//
// The span is the clause's statements rather than a pair of braces, because a
// case clause has none. That is the same promise -- what a consumer does with
// the span is ask whether anything inside it ran.
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
		// `return 1` is on line 6 and is the whole of the clause's body.
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

// TestACaseClauseWithNoBodyIsNotProved keeps the empty-body refusal at the
// clause too.
//
// An empty clause gates nothing, so there is no body a test could have failed
// to enter and nothing the lemma can say.
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
