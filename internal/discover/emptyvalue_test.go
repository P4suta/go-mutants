// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"testing"
)

func TestAResultThatIsAlreadyTheEmptyValue(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name  string
		decls string
		expr  string
		want  bool
	}{
		{name: "an empty slice literal", expr: "[]int{}", want: true},
		{name: "an empty map literal", expr: "map[string]int{}", want: true},
		{name: "a slice literal with an element", expr: "[]int{1}"},
		{name: "a map literal with an entry", expr: "map[string]int{\"a\": 1}"},
		{name: "make with a zero length", expr: "make([]int, 0)", want: true},
		{name: "make with a zero length and capacity", expr: "make([]int, 0, 0)", want: true},
		{
			name: "make with a length that folds to zero",
			expr: "make([]int, 1-1)", want: true,
		},
		{
			name:  "make with a named zero",
			decls: "const none = 0",
			expr:  "make([]int, none)", want: true,
		},
		{name: "make with a non-zero length", expr: "make([]int, 1)"},
		{name: "make with a zero length and a capacity", expr: "make([]int, 0, 4)"},
		{name: "make with a length nobody folded", decls: "var n int", expr: "make([]int, n)"},
		{name: "make with only a type", expr: "make(chan int)"},
		{name: "a name", decls: "var xs []int", expr: "xs"},
		{name: "a call that is not make", decls: "func build() []int { return nil }", expr: "build()"},
		{
			name:  "a call of a function the package named make",
			decls: "func make2() []int { return nil }\n\nfunc make() []int { return nil }",
			expr:  "make()",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			p := probeSource(t, c.decls, c.expr)
			s := &fileScan{info: p.info}
			if got := s.alreadyEmpty(p.expr); got != c.want {
				t.Errorf("alreadyEmpty(%s) = %v, want %v", c.expr, got, c.want)
			}
		})
	}
}

func TestAResultBesideANonNilErrorIsLeftAlone(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "beside a non-nil error",
			src:  "package pkg\n\nfunc probe(err error) ([]int, error) {\n\treturn nil, err\n}\n",
			want: true,
		},
		{
			name: "beside a nil error, which is the success path",
			src:  "package pkg\n\nfunc probe(xs []int) ([]int, error) {\n\treturn xs, nil\n}\n",
		},
		{
			name: "beside a value that is not an error",
			src:  "package pkg\n\nfunc probe(xs []int, n int) ([]int, int) {\n\treturn xs, n\n}\n",
		},
		{
			name: "alone",
			src:  "package pkg\n\nfunc probe(xs []int) []int {\n\treturn xs\n}\n",
		},
		{
			name: "beside a concrete error value",
			src: "package pkg\n\ntype myErr struct{}\n\nfunc (myErr) Error() string { return \"\" }\n\n" +
				"func probe(xs []int) ([]int, error) {\n\treturn xs, myErr{}\n}\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			p := parsedAt(t, "scan.go", c.src)
			s := &fileScan{
				info:  p.info,
				guard: newGuardResolver(p.file, p.info, p.pkg, p.fset.File(p.file.Package), nil),
			}
			var first ast.Expr
			for node := range s.guard.parent {
				if ret, isReturn := node.(*ast.ReturnStmt); isReturn && len(ret.Results) > 0 {
					first = ret.Results[0]
				}
			}
			if first == nil {
				t.Fatal("the fixture holds no return with results")
			}
			if got := s.returnsBesideAnError(first); got != c.want {
				t.Errorf("returnsBesideAnError = %v, want %v", got, c.want)
			}
		})
	}

	blind := &fileScan{}
	if blind.returnsBesideAnError(ast.NewIdent("xs")) {
		t.Error("returnsBesideAnError answered without a parent index")
	}
}

func TestAConstantThatIsAlreadyTheReplacement(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name        string
		decls       string
		expr        string
		replacement string
		want        bool
	}{
		{name: "a literal zero", expr: "0", replacement: "0", want: true},
		{name: "a folded zero", expr: "1 - 1", replacement: "0", want: true},
		{name: "the first name of an iota block", decls: "const (\n\tDisjoint = iota\n\tOther\n)", expr: "Disjoint", replacement: "0", want: true},
		{name: "the second name of an iota block", decls: "const (\n\tDisjoint = iota\n\tOther\n)", expr: "Other", replacement: "0"},
		{name: "a floating zero", expr: "0.0", replacement: "0", want: true},
		{name: "a non-zero literal", expr: "1", replacement: "0"},
		{name: "a name nobody folded", decls: "var n int", expr: "n", replacement: "0"},
		{name: "an empty string", expr: `""`, replacement: `""`, want: true},
		{name: "a string that is not empty", expr: `"a"`, replacement: `""`},
		{name: "true", expr: "true", replacement: "true", want: true},
		{name: "false against true", expr: "false", replacement: "true"},
		{name: "false", expr: "false", replacement: "false", want: true},
		{
			name: "an empty string against zero",
			expr: `""`, replacement: "0",
		},
		{name: "zero against the empty string", expr: "0", replacement: `""`},
		{name: "false against zero", expr: "false", replacement: "0"},
		{name: "zero against false", expr: "0", replacement: "false"},
		{
			name: "a replacement that is not a constant",
			expr: "0", replacement: "nil",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			p := probeSource(t, c.decls, c.expr)
			s := &fileScan{info: p.info}
			if got := s.spellsTheSameConstant(p.expr, c.replacement); got != c.want {
				t.Errorf("spellsTheSameConstant(%s, %q) = %v, want %v", c.expr, c.replacement, got, c.want)
			}
		})
	}

	blind := &fileScan{}
	if blind.spellsTheSameConstant(ast.NewIdent("zero"), "0") {
		t.Error("spellsTheSameConstant answered without the checker's record")
	}
	if (&fileScan{info: probeSource(t, "", "0").info}).spellsTheSameConstant(nil, "0") {
		t.Error("spellsTheSameConstant answered about an expression that is not there")
	}
}

func TestALabelThatNamesTheNearestTargetIsNotWorthDropping(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "a break naming the loop it is directly in",
			src:  "package pkg\n\nfunc probe() {\nouter:\n\tfor {\n\t\tbreak outer\n\t}\n}\n",
			want: true,
		},
		{
			name: "a break naming a loop outside a switch",
			src: "package pkg\n\nfunc probe() {\nouter:\n\tfor {\n\t\tswitch {\n" +
				"\t\tdefault:\n\t\t\tbreak outer\n\t\t}\n\t}\n}\n",
		},
		{
			name: "a continue naming a loop outside a switch",
			src: "package pkg\n\nfunc probe() {\nouter:\n\tfor {\n\t\tswitch {\n" +
				"\t\tdefault:\n\t\t\tcontinue outer\n\t\t}\n\t}\n}\n",
			want: true,
		},
		{
			name: "a break naming the outer of two loops",
			src: "package pkg\n\nfunc probe() {\nouter:\n\tfor {\n\t\tfor {\n" +
				"\t\t\tbreak outer\n\t\t}\n\t}\n}\n",
		},
		{
			name: "a continue naming the outer of two loops",
			src: "package pkg\n\nfunc probe() {\nouter:\n\tfor {\n\t\tfor {\n" +
				"\t\t\tcontinue outer\n\t\t}\n\t}\n}\n",
		},
		{
			name: "a break naming a loop outside a select",
			src: "package pkg\n\nvar ch chan int\n\nfunc probe() {\nouter:\n\tfor {\n\t\tselect {\n" +
				"\t\tcase <-ch:\n\t\t\tbreak outer\n\t\t}\n\t}\n}\n",
		},
		{
			name: "a break naming a range loop it is directly in",
			src: "package pkg\n\nfunc probe(xs []int) {\nouter:\n\tfor range xs {\n" +
				"\t\tbreak outer\n\t}\n}\n",
			want: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			p := parsedAt(t, "scan.go", c.src)
			s := &fileScan{
				info:  p.info,
				guard: newGuardResolver(p.file, p.info, p.pkg, p.fset.File(p.file.Package), nil),
			}
			var branch *ast.BranchStmt
			for node := range s.guard.parent {
				if b, isBranch := node.(*ast.BranchStmt); isBranch && b.Label != nil {
					branch = b
				}
			}
			if branch == nil {
				t.Fatal("the fixture holds no labelled branch")
			}
			if got := s.labelNamesTheNearestTarget(branch); got != c.want {
				t.Errorf("labelNamesTheNearestTarget = %v, want %v", got, c.want)
			}
		})
	}

	labelled := &ast.BranchStmt{Tok: token.BREAK, Label: ast.NewIdent("outer")}
	if !(&fileScan{}).labelNamesTheNearestTarget(labelled) {
		t.Error("a branch this phase cannot resolve was not refused")
	}
	p := parsedAt(t, "scan.go", "package pkg\n\nfunc probe() {\nouter:\n\tfor {\n\t\tbreak outer\n\t}\n}\n")
	unresolved := &fileScan{
		info:  p.info,
		guard: newGuardResolver(p.file, p.info, p.pkg, p.fset.File(p.file.Package), nil),
	}
	if !unresolved.labelNamesTheNearestTarget(labelled) {
		t.Error("a label the checker resolved to nothing was not refused")
	}
}

func TestWhichResultsGetTheNeutralValueThatIsNotNil(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		want string
	}{
		{
			name: "a slice",
			src:  "package pkg\n\nfunc probe(xs []int) []int {\n\treturn xs\n}\n",
			want: "[]int{}",
		},
		{
			name: "a map",
			src:  "package pkg\n\nfunc probe(m map[string]int) map[string]int {\n\treturn m\n}\n",
			want: "map[string]int{}",
		},
		{
			name: "a named slice type",
			src:  "package pkg\n\ntype lines []string\n\nfunc probe(l lines) lines {\n\treturn l\n}\n",
			want: "lines{}",
		},
		{
			name: "a slice of a named element type",
			src: "package pkg\n\ntype row struct{ n int }\n\n" +
				"func probe(xs []row) []row {\n\treturn xs\n}\n",
			want: "[]row{}",
		},
		{
			name: "a channel",
			src:  "package pkg\n\nfunc probe(ch chan int) chan int {\n\treturn ch\n}\n",
		},
		{
			name: "a pointer",
			src:  "package pkg\n\nfunc probe(p *int) *int {\n\treturn p\n}\n",
		},
		{
			name: "a string",
			src:  "package pkg\n\nfunc probe(s string) string {\n\treturn s\n}\n",
		},
		{
			name: "an interface",
			src:  "package pkg\n\nfunc probe(v any) any {\n\treturn v\n}\n",
		},
		{
			name: "a slice already spelled empty",
			src:  "package pkg\n\nfunc probe() []int {\n\treturn []int{}\n}\n",
		},
		{
			name: "a slice already made empty",
			src:  "package pkg\n\nfunc probe() []int {\n\treturn make([]int, 0)\n}\n",
		},
		{
			name: "a slice beside a non-nil error",
			src:  "package pkg\n\nfunc probe(err error) ([]int, error) {\n\treturn nil, err\n}\n",
		},
		{
			name: "a slice beside a nil error",
			src:  "package pkg\n\nfunc probe(xs []int) ([]int, error) {\n\treturn xs, nil\n}\n",
			want: "[]int{}",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			var got []string
			for _, candidate := range scanSource(t, c.src).candidates {
				if candidate.Rule.Name == ruleReturnEmptySlice || candidate.Rule.Name == ruleReturnEmptyMap {
					got = append(got, candidate.Replacement)
				}
			}
			if c.want == "" {
				if len(got) != 0 {
					t.Errorf("%s produced the neutral values %v, want none", c.name, got)
				}
				return
			}
			if len(got) != 1 || got[0] != c.want {
				t.Errorf("%s produced %v, want [%s]", c.name, got, c.want)
			}
		})
	}
}

func TestASliceOfATypeThisFileCannotSpellIsRecordedRatherThanSkipped(t *testing.T) {
	t.Parallel()

	got := scanSource(t, "package pkg\n\nimport \"unsafe\"\n\n"+
		"func probe(xs []unsafe.Pointer) []unsafe.Pointer {\n\treturn xs\n}\n")

	for _, candidate := range got.candidates {
		if candidate.Rule.Name == ruleReturnEmptySlice {
			t.Errorf("a slice of a type this file cannot spell produced %q", candidate.Replacement)
		}
	}
	recorded := false
	for _, site := range got.sites {
		if site.Reason == SkipUnnameableDeclType && site.Rule == ruleReturnEmptySlice {
			recorded = true
		}
	}
	if !recorded {
		t.Errorf("the refusal was not recorded as %s; the sites are %+v", SkipUnnameableDeclType, got.sites)
	}
}

func TestABranchWhoseTargetThisPhaseCannotFindIsRefused(t *testing.T) {
	t.Parallel()

	const src = "package pkg\n\nfunc probe() {\nouter:\n\tfor {\n\t\tswitch {\n" +
		"\t\tdefault:\n\t\t\tbreak outer\n\t\t}\n\t}\n}\n"
	p := parsedAt(t, "scan.go", src)
	full := newGuardResolver(p.file, p.info, p.pkg, p.fset.File(p.file.Package), nil)

	var branch *ast.BranchStmt
	for node := range full.parent {
		if b, isBranch := node.(*ast.BranchStmt); isBranch && b.Label != nil {
			branch = b
		}
	}
	if branch == nil {
		t.Fatal("the fixture holds no labelled branch")
	}

	if (&fileScan{info: p.info, guard: full}).labelNamesTheNearestTarget(branch) {
		t.Fatal("the fixture's branch names the nearest target, which the rest of this test relies on it not doing")
	}

	adrift := &fileScan{info: p.info, guard: &guardResolver{parent: map[ast.Node]ast.Node{}}}
	if !adrift.labelNamesTheNearestTarget(branch) {
		t.Error("a branch with no enclosing construct was not refused")
	}
}

func TestTheTypeCheckersRecordIsAskedBeforeItIsRead(t *testing.T) {
	t.Parallel()

	p := probeSource(t, "var xs []int", "xs[0]")
	if isTypeExpr(p.info, nil) {
		t.Error("isTypeExpr accepted an expression that is not there")
	}
	if isTypeExpr(nil, p.expr) {
		t.Error("isTypeExpr answered without the checker's record")
	}
	generic := probeSource(t, "func pair[A any, B any](a A, b B) int { return 0 }", "pair[int, string]")
	index, isIndex := generic.expr.(*ast.IndexListExpr)
	if !isIndex {
		t.Fatalf("the fixture yielded %T, want a generic instantiation", generic.expr)
	}
	if !isTypeExpr(generic.info, index.Indices[0]) {
		t.Error("isTypeExpr refused an explicit type argument")
	}
	if isTypeExpr(p.info, p.expr) {
		t.Error("isTypeExpr accepted an index expression, which is a value")
	}
}

func TestAResultIsProbedOnlyWhereThereIsAResolverToAsk(t *testing.T) {
	t.Parallel()

	p := probeSource(t, "var n int", "n")
	blind := &fileScan{info: p.info}
	if blind.probesResult(p.expr, nil) {
		t.Error("probesResult answered without a resolver")
	}
	if got := blind.probeSite(&ast.ReturnStmt{}, nil); got != nil {
		t.Errorf("probeSite = %+v without a resolver, want none", got)
	}
}
