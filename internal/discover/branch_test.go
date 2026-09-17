// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

const branchCaseLine = 7

const branchGoMod = "module example.com/branchfix\n\ngo 1.26\n"

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

type branchCase struct {
	name       string
	rule       string
	lines      []string
	want       *BranchProof
	candidates int
}

func proof(startLine, startColumn, endLine, endColumn int) *BranchProof {
	return &BranchProof{
		Direction:       BranchDecreasing,
		BodyStartLine:   startLine,
		BodyStartColumn: startColumn,
		BodyEndLine:     endLine,
		BodyEndColumn:   endColumn,
	}
}

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

func branchCaseFile(index int) string {
	return "case" + strconv.Itoa(index) + "/branch.go"
}

func branchCandidates(candidates []Located, path, rule string) []Located {
	var found []Located
	for _, c := range candidates {
		if c.Path == path && c.Rule.Name == rule {
			found = append(found, c)
		}
	}
	return found
}

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

func formatBranch(b *BranchProof) string {
	if b == nil {
		return "none"
	}
	return b.Direction + " " +
		strconv.Itoa(b.BodyStartLine) + ":" + strconv.Itoa(b.BodyStartColumn) + "," +
		strconv.Itoa(b.BodyEndLine) + ":" + strconv.Itoa(b.BodyEndColumn)
}

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
			name:       "a tagged switch case label",
			rule:       "le-to-lt",
			lines:      []string{"\tswitch a {", "\tcase b:", "\t\treturn 1", "\t}"},
			candidates: 0,
		},
		{
			name:       "a call argument",
			rule:       "le-to-lt",
			lines:      []string{"\t_ = takes(a <= b)"},
			candidates: 1,
		},
		{
			name:       "a condition written as a function literal call",
			rule:       "le-to-lt",
			lines:      []string{"\tif func() bool { return a <= b }() {", "\t\treturn 1", "\t}"},
			candidates: 1,
		},
	})
}

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
