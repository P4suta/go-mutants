// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// The branch-proof tests drive whole modules through the real toolchain, the
// way the rest of this package's tests do, because almost every refusal below
// is a question only go/types can answer: whether a selector dereferences a
// pointer, whether a divisor is a constant, whether two operands can be
// compared without a panic. A hand-built syntax tree would have to invent all
// three, and would then be testing the invention.
//
// Every fixture is assembled by [branchSource] from a header of fixed length,
// so a case's first line is always [branchCaseLine]. The coordinates the tables
// assert are literal numbers counted off the case text, never recomputed from
// the file: re-deriving them would agree with any consistent miscount, and the
// whole worth of the span is that it addresses the same bytes
// `go test -coverprofile` reports its blocks in.

// branchCaseLine is the line a case's first line lands on. Above it sit the
// licence block, a blank line, the package clause, a blank line, and the
// function whose body the case is.
const branchCaseLine = 7

// branchGoMod is the module every table is written into.
const branchGoMod = "module example.com/branchfix\n\ngo 1.26\n"

// branchSupport is everything the cases share: the types they select fields
// through, the functions they call, and the package-level variables that keep
// an operand from folding to a constant.
//
// It sits *below* the case in every fixture on purpose. A helper added here
// must never move the lines a table names, and it cannot, because nothing here
// is ever above a case.
var branchSupport = []string{
	"",
	"type pointee struct{ f int }",
	"",
	"type outer struct{ *pointee }",
	"",
	"type pair struct{ u, v int }",
	"",
	"type holder struct{}",
	"",
	"func (holder) method() bool { return true }",
	"",
	"func call() bool { return true }",
	"",
	"func takes(bool) bool { return true }",
	"",
	"func fail() error { return nil }",
	"",
	"var (",
	"\ta, b, k int",
	"\tn       uint",
	"\tx, y    bool",
	"\te       error",
	"\ts       []int",
	"\tq       *int",
	"\tp       *pointee",
	"\to       outer",
	"\tval     pointee",
	"\tpr1     pair",
	"\tpr2     pair",
	"\ti, j, v any",
	"\tm       map[string]error",
	"\tmk      string",
	"\tch      chan bool",
	"\th       holder",
	")",
}

// branchSource renders one case into a file.
//
// The six header lines are counted, not decorated: [branchCaseLine] is derived
// from this literal and a case that stopped starting there would be asserting
// coordinates of the wrong statement.
func branchSource(caseLines []string) string {
	source := []string{
		"// SPDX-FileCopyrightText: 2026 go-mutants contributors",
		"// SPDX-License-Identifier: MIT OR Apache-2.0",
		"",
		"package branchfix",
		"",
		"func target() any {",
	}
	source = append(source, caseLines...)
	source = append(source, "\treturn nil", "}")
	source = append(source, branchSupport...)
	return strings.Join(source, "\n") + "\n"
}

// A branchCase is one fixture: the lines of a function body, the rule whose
// candidate the case is about, and the proof that candidate must carry.
type branchCase struct {
	name string
	rule string
	// lines are the statements of the function body, the first of them on
	// [branchCaseLine].
	lines []string
	// want is the proof every candidate of the rule must carry, or nil when the
	// case is a refusal.
	want *BranchProof
	// candidates is how many candidates of the rule the case holds. It is
	// stated rather than derived so that a case which quietly stops producing
	// one fails here instead of passing without asserting anything.
	candidates int
}

// proof is the expectation constructor. Every proof this phase emits is
// decreasing, so the direction is not a table column.
func proof(startLine, startColumn, endLine, endColumn int) *BranchProof {
	return &BranchProof{
		Direction:       BranchDecreasing,
		BodyStartLine:   startLine,
		BodyStartColumn: startColumn,
		BodyEndLine:     endLine,
		BodyEndColumn:   endColumn,
	}
}

// runBranchCases writes a whole table into one module — one package directory
// per case — and checks each case against the single discovery that answers all
// of them.
//
// One module rather than one per case, because the expensive half of a
// discovery is loading and type-checking, and a table of thirty cases would
// otherwise pay for thirty toolchain invocations to ask thirty questions about
// thirty independent files.
func runBranchCases(t *testing.T, cases []branchCase) {
	t.Helper()
	files := map[string]string{"go.mod": branchGoMod}
	for i, c := range cases {
		files[branchCaseFile(i)] = branchSource(c.lines)
	}
	root := writeModule(t, files)
	result, err := Discover(context.Background(), Options{SnapshotRoot: root, Toolchain: toolchain(t)})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := branchCaseFile(i)
			found := branchCandidates(result.Candidates, path, c.rule)
			if len(found) != c.candidates {
				t.Fatalf("%s holds %d %s candidates, want %d", path, len(found), c.rule, c.candidates)
			}
			for _, got := range found {
				assertBranchProof(t, got, c.want)
			}
		})
	}
}

// branchCaseFile is the module-relative path of one case's file. A directory
// each, so that the cases cannot see each other's declarations.
func branchCaseFile(index int) string {
	return "case" + strconv.Itoa(index) + "/branch.go"
}

// branchCandidates returns every candidate of one rule in one file.
func branchCandidates(candidates []Located, path, rule string) []Located {
	var found []Located
	for _, c := range candidates {
		if c.Path == path && c.Rule.Name == rule {
			found = append(found, c)
		}
	}
	return found
}

// assertBranchProof compares one candidate's proof against the expectation,
// reporting the whole of both because four coordinates are unreadable one at a
// time.
func assertBranchProof(t *testing.T, got Located, want *BranchProof) {
	t.Helper()
	switch {
	case want == nil && got.Branch == nil:
		return
	case want == nil:
		t.Fatalf("%s %s carries the proof %s, want none", got.Path, got.Rule.Name, formatBranch(got.Branch))
	case got.Branch == nil:
		t.Fatalf("%s %s carries no proof, want %s", got.Path, got.Rule.Name, formatBranch(want))
	case *got.Branch != *want:
		t.Fatalf("%s %s carries the proof %s, want %s",
			got.Path, got.Rule.Name, formatBranch(got.Branch), formatBranch(want))
	}
}

// formatBranch renders a proof the way the tables are written.
func formatBranch(b *BranchProof) string {
	if b == nil {
		return "none"
	}
	return b.Direction + " " +
		strconv.Itoa(b.BodyStartLine) + ":" + strconv.Itoa(b.BodyStartColumn) + "," +
		strconv.Itoa(b.BodyEndLine) + ":" + strconv.Itoa(b.BodyEndColumn)
}

// TestBranchProofNamesTheBodyOfADecreasingCondition is the contract: each of
// the four decreasing rules, in each of the two statements that gate a body,
// wherever the condition puts the edit.
//
// The coordinates are the braces of the body, and the last case is the reason
// they have to be: a body whose first statement shares the opening brace's line
// has no line of its own to name.
func TestBranchProofNamesTheBodyOfADecreasingCondition(t *testing.T) {
	runBranchCases(t, []branchCase{
		{
			name:       "le in an if",
			rule:       "le-to-lt",
			lines:      []string{"\tif a <= b {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 12, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "ge in an if",
			rule:       "ge-to-gt",
			lines:      []string{"\tif a >= b {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 12, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "or in an if",
			rule:       "or-to-and",
			lines:      []string{"\tif x || y {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 12, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "nil error branch in an if",
			rule:       "nil-error-branch",
			lines:      []string{"\tif e != nil {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 14, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "le in a for",
			rule:       "le-to-lt",
			lines:      []string{"\tfor a <= b {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 13, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "ge in a for",
			rule:       "ge-to-gt",
			lines:      []string{"\tfor a >= b {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 13, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "or in a for",
			rule:       "or-to-and",
			lines:      []string{"\tfor x || y {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 13, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "nil error branch in a for",
			rule:       "nil-error-branch",
			lines:      []string{"\tfor e != nil {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 15, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			// The initialiser is a call, and a call is exactly what the
			// inertness rule refuses inside a condition. It is not inside one:
			// an `if`'s init statement runs before the condition is ever
			// evaluated, and the mutant runs it too.
			name:       "an if with an init statement",
			rule:       "nil-error-branch",
			lines:      []string{"\tif err := fail(); err != nil {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 31, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name: "an else if",
			rule: "le-to-lt",
			lines: []string{
				"\tif x {",
				"\t\treturn 1",
				"\t} else if a <= b {",
				"\t\treturn 2",
				"\t}",
			},
			want:       proof(branchCaseLine+2, 19, branchCaseLine+4, 2),
			candidates: 1,
		},
		{
			name:       "the left operand of a conjunction",
			rule:       "le-to-lt",
			lines:      []string{"\tif a <= b && x {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 17, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "the right operand of a disjunction",
			rule:       "le-to-lt",
			lines:      []string{"\tif x || a <= b {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 17, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "parenthesised",
			rule:       "le-to-lt",
			lines:      []string{"\tif (a <= b) {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 14, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			// The proof is about the literal's own `if`, and the walk stops at
			// the literal rather than climbing out into the assignment.
			name: "inside a function literal's own if",
			rule: "le-to-lt",
			lines: []string{
				"\tfn := func() int {",
				"\t\tif a <= b {",
				"\t\t\treturn 1",
				"\t\t}",
				"\t\treturn 0",
				"\t}",
				"\t_ = fn",
			},
			want:       proof(branchCaseLine+1, 13, branchCaseLine+3, 3),
			candidates: 1,
		},
		{
			name:       "a body that shares the brace's line",
			rule:       "le-to-lt",
			lines:      []string{"\tif a <= b { return 1 }"},
			want:       proof(branchCaseLine, 12, branchCaseLine, 23),
			candidates: 1,
		},
	})
}

// TestBranchProofIsAbsentForAnIncreasingEdit is the other half of the table in
// branch.go: an edit that can make a condition *more* often true proves nothing
// about a body nobody entered, because the mutant may enter it where the
// original did not.
func TestBranchProofIsAbsentForAnIncreasingEdit(t *testing.T) {
	runBranchCases(t, []branchCase{
		{
			name:       "lt to le",
			rule:       "lt-to-le",
			lines:      []string{"\tif a < b {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "gt to ge",
			rule:       "gt-to-ge",
			lines:      []string{"\tif a > b {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "and to or",
			rule:       "and-to-or",
			lines:      []string{"\tif x && y {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			// Neither direction: `==` and `!=` move a condition both ways
			// depending on the operands, so neither implies the other.
			name:       "neq to eq",
			rule:       "neq-to-eq",
			lines:      []string{"\tif a != b {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "eq to neq",
			rule:       "eq-to-neq",
			lines:      []string{"\tif a == b {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
	})
}

// TestBranchProofRefusesANegatedPolarity covers the operators that survive the
// walk's shape but destroy its monotonicity: `!` inverts the implication, and a
// boolean equality turns "less often true" into "differently true".
func TestBranchProofRefusesANegatedPolarity(t *testing.T) {
	runBranchCases(t, []branchCase{
		{
			name:       "a negated comparison",
			rule:       "le-to-lt",
			lines:      []string{"\tif !(a <= b) {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "a negated disjunction",
			rule:       "or-to-and",
			lines:      []string{"\tif !(x || y) {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "compared for equality",
			rule:       "le-to-lt",
			lines:      []string{"\tif (a <= b) == x {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "compared for inequality",
			rule:       "le-to-lt",
			lines:      []string{"\tif (a <= b) != x {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
	})
}

// TestBranchProofRefusesAnEditOutsideACondition covers the walk terminating
// anywhere but an `if` or a `for`. There is no body to name, so there is
// nothing to prove.
func TestBranchProofRefusesAnEditOutsideACondition(t *testing.T) {
	runBranchCases(t, []branchCase{
		{
			name:       "a returned value",
			rule:       "le-to-lt",
			lines:      []string{"\treturn a <= b"},
			candidates: 1,
		},
		{
			name:       "an assigned value",
			rule:       "le-to-lt",
			lines:      []string{"\tok := a <= b", "\t_ = ok"},
			candidates: 1,
		},
		{
			// A case label is suppressed before a candidate is ever proposed,
			// so this case asserts that there is nothing here to prove and no
			// proof either.
			name:       "a switch case label",
			rule:       "le-to-lt",
			lines:      []string{"\tswitch {", "\tcase a <= b:", "\t\treturn 1", "\t}"},
			candidates: 0,
		},
		{
			name:       "a call argument",
			rule:       "le-to-lt",
			lines:      []string{"\t_ = takes(a <= b)"},
			candidates: 1,
		},
		{
			// The condition of an `if`, but the walk never reaches that `if`:
			// it stops at the literal's `return`, which is where the edit stops
			// being a condition.
			name:       "a condition written as a function literal call",
			rule:       "le-to-lt",
			lines:      []string{"\tif func() bool { return a <= b }() {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
	})
}

// TestBranchProofRefusesAnEffectfulCondition covers the reason the whole
// condition has to be inert and not merely the edited part of it: the mutant
// may evaluate fewer sub-expressions than the original, so an effect in one the
// mutant skips is an observable difference even when the branch is identical.
func TestBranchProofRefusesAnEffectfulCondition(t *testing.T) {
	runBranchCases(t, []branchCase{
		{
			name:       "a function call",
			rule:       "le-to-lt",
			lines:      []string{"\tif call() && a <= b {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "a method call",
			rule:       "le-to-lt",
			lines:      []string{"\tif h.method() && a <= b {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "a channel receive",
			rule:       "le-to-lt",
			lines:      []string{"\tif <-ch && a <= b {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "an immediately called function literal",
			rule:       "le-to-lt",
			lines:      []string{"\tif (func() bool { return true })() && a <= b {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "the length builtin",
			rule:       "le-to-lt",
			lines:      []string{"\tif len(s) <= 3 {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 17, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "the min builtin",
			rule:       "le-to-lt",
			lines:      []string{"\tif min(a, b) <= 3 {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 20, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "a conversion",
			rule:       "le-to-lt",
			lines:      []string{"\tif int64(a) <= 3 {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 19, branchCaseLine+2, 2),
			candidates: 1,
		},
	})
}

// TestBranchProofRefusesAConditionThatMayPanic is the second half of
// inertness. A condition the mutant stops evaluating must not be one whose
// evaluation could have ended the program: a panic the original raises and the
// mutant does not is as observable a difference as any assertion.
func TestBranchProofRefusesAConditionThatMayPanic(t *testing.T) {
	runBranchCases(t, []branchCase{
		{
			name:       "a field through a pointer",
			rule:       "le-to-lt",
			lines:      []string{"\tif p.f <= 3 {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "a field promoted through an embedded pointer",
			rule:       "le-to-lt",
			lines:      []string{"\tif o.f <= 3 {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "an index expression",
			rule:       "le-to-lt",
			lines:      []string{"\tif s[k] <= 3 {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "a slice expression",
			rule:       "le-to-lt",
			lines:      []string{"\tif len(s[1:]) <= 3 {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "a type assertion",
			rule:       "le-to-lt",
			lines:      []string{"\tif v.(int) <= 3 {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "division by a variable",
			rule:       "le-to-lt",
			lines:      []string{"\tif a/b <= 3 {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "a variable shift count",
			rule:       "le-to-lt",
			lines:      []string{"\tif a>>n <= 3 {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "a pointer dereference",
			rule:       "le-to-lt",
			lines:      []string{"\tif *q <= 3 {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "two interfaces compared",
			rule:       "le-to-lt",
			lines:      []string{"\tif i == j && a <= b {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "a map index",
			rule:       "nil-error-branch",
			lines:      []string{"\tif m[mk] != nil {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
		{
			name:       "division by a constant",
			rule:       "le-to-lt",
			lines:      []string{"\tif a/2 <= 3 {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 14, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "a constant shift count",
			rule:       "le-to-lt",
			lines:      []string{"\tif a>>1 <= 3 {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 15, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "an interface compared with nil",
			rule:       "le-to-lt",
			lines:      []string{"\tif i == nil && a <= b {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 24, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "an error compared with nil",
			rule:       "le-to-lt",
			lines:      []string{"\tif e != nil && a <= b {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 24, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "a pointer compared with nil",
			rule:       "le-to-lt",
			lines:      []string{"\tif q == nil && a <= b {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 24, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "two integers compared",
			rule:       "le-to-lt",
			lines:      []string{"\tif a == b && a <= b {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 22, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "two structs of integers compared",
			rule:       "le-to-lt",
			lines:      []string{"\tif pr1 == pr2 && a <= b {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 26, branchCaseLine+2, 2),
			candidates: 1,
		},
		{
			name:       "a field of a value",
			rule:       "le-to-lt",
			lines:      []string{"\tif val.f <= 3 {", "\t\treturn 1", "\t}"},
			want:       proof(branchCaseLine, 16, branchCaseLine+2, 2),
			candidates: 1,
		},
	})
}

// TestBranchProofRefusesAnEmptyBody covers the one refusal that is about the
// consumer rather than about Go: coverage instruments statements, and a body
// with none cannot be observed to have run or not run.
func TestBranchProofRefusesAnEmptyBody(t *testing.T) {
	runBranchCases(t, []branchCase{
		{
			name:       "an if with nothing in it",
			rule:       "le-to-lt",
			lines:      []string{"\tif a <= b {", "\t}"},
			candidates: 1,
		},
	})
}

// TestBranchProofRefusesAFileWithALineDirective covers the coordinates naming
// another file. A `//line` directive is what a generator writes to point at its
// own input, and `cmd/cover` attributes blocks to the name the directive gives
// — so a span measured in this file's own numbering would be compared against
// blocks recorded under a different one.
func TestBranchProofRefusesAFileWithALineDirective(t *testing.T) {
	runBranchCases(t, []branchCase{
		{
			name:       "a redirected if",
			rule:       "le-to-lt",
			lines:      []string{"//line other.go:100", "\tif a <= b {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
	})
}
