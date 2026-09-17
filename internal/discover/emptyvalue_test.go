// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"testing"
)

// The three refusals that keep a rule from proposing the program it is already
// looking at.
//
// A mutant equal to its original is not a mutant. The catalogue refuses one
// anyway, so none of these is about correctness -- they are about what a
// refusal *is*: recording a skip would tell a user go-mutants declined to
// mutate their code, and refusing loudly would turn every `return nil` in a
// tree into a failed run. So each of these is silent, and each is a claim about
// the source that has to be right in both directions.

// TestAResultThatIsAlreadyTheEmptyValue pins [fileScan.alreadyEmpty], which is
// what keeps `neutral-value` from proposing `[]int{}` where the source already
// says `[]int{}`.
//
// The `make` arm is the one worth stating. `make([]int, 0)` and `make([]int, 0, 0)`
// are the same slice as `[]int{}` -- empty, non-nil -- and `make([]int, n)` is
// not, whatever n turns out to be. The length is read through the checker's
// folding rather than off the spelling, so a constant that folds to zero counts
// however it was written.
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

// TestAResultBesideANonNilErrorIsLeftAlone pins
// [fileScan.returnsBesideAnError].
//
// `if err != nil { return nil, err }` is the universal Go convention, and a
// caller that has seen a non-nil error does not look at the other results. A
// mutant that emptied one of them there is one no honest test can kill, and
// without this gate most of what the neutral-value family proposed would be
// exactly that shape. It is an argument from convention rather than a proof,
// which is why it is narrow: the value beside a *nil* error is the success
// path, and that is where the mutant is worth having.
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

	// And with no resolver, which is the fail-closed direction the other way
	// round: without the parent index there is no statement to look along, and
	// answering "no error beside it" would licence the mutant rather than
	// decline it.
	blind := &fileScan{}
	if blind.returnsBesideAnError(ast.NewIdent("xs")) {
		t.Error("returnsBesideAnError answered without a parent index")
	}
}

// TestAConstantThatIsAlreadyTheReplacement pins
// [fileScan.spellsTheSameConstant], which is the refusal this repository found
// by running its own gate against itself.
//
// `return Disjoint`, where `Disjoint` is the first name of an iota block, writes
// different bytes for the same constant: go/types folds both readings to 0, the
// `return` converts both by the same rule, and the two trees compile to one
// program. The comparison is of *values* rather than of spellings for exactly
// that reason -- and it is between values of one kind, because `0` and `""` are
// both constants and neither is the other.
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
			// Two constants of different kinds are not equal, and comparing
			// them is not a question go/constant answers -- it is one that
			// panics. The empty string is not zero.
			name: "an empty string against zero",
			expr: `""`, replacement: "0",
		},
		{name: "zero against the empty string", expr: "0", replacement: `""`},
		{name: "false against zero", expr: "false", replacement: "0"},
		{name: "zero against false", expr: "0", replacement: "false"},
		{
			// A replacement this build has no constant for is never refused on
			// these grounds: `nil` is not a constant and go/types folds no
			// value for it.
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

	// And without the checker's record there is no folded value to compare, so
	// nothing is refused: the rule is "this mutant is the original", and with
	// no evidence it is not.
	blind := &fileScan{}
	if blind.spellsTheSameConstant(ast.NewIdent("zero"), "0") {
		t.Error("spellsTheSameConstant answered without the checker's record")
	}
	if (&fileScan{info: probeSource(t, "", "0").info}).spellsTheSameConstant(nil, "0") {
		t.Error("spellsTheSameConstant answered about an expression that is not there")
	}
}

// TestALabelThatNamesTheNearestTargetIsNotWorthDropping pins
// [fileScan.labelNamesTheNearestTarget], which is what separates a real
// `drop-break-label` mutant from the program that was already there.
//
// `L: for { break L }` and `L: for { break }` are one program, because the
// label names the construct a bare `break` would leave anyway. What makes the
// rule worth having is that the *same* position answers differently for the two
// branch kinds: inside a `switch` inside a labelled `for`, a bare `break` leaves
// the switch and `break L` leaves the loop -- while `continue L` and `continue`
// both reach the loop, because a switch is not something `continue` binds to.
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

	// Two ways the question cannot be answered, and both answer *true*, which
	// refuses the candidate. An unresolvable label is one this code does not
	// understand, and emitting a mutant on the strength of not understanding it
	// is how an equivalent mutant becomes a survivor somebody has to argue
	// about.
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

// TestWhichResultsGetTheNeutralValueThatIsNotNil is
// [fileScan.replaceEmptyNeutral], the family that exists because `len(x) == 0`
// is true of both readings.
//
// A function that returns nil where it meant to return an empty slice passes
// every `len` check a suite routinely makes, and `encoding/json` writes `null`
// where the caller expected `[]`. That is the difference the rule is about, and
// `return-nil` beside it cannot express it -- which is also why the line is
// drawn where it is: a slice and a map are the only types the standard library
// distinguishes two neutral values of.
func TestWhichResultsGetTheNeutralValueThatIsNotNil(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		// want is the replacement text, or empty for no candidate.
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
			// Neither of the two: a channel's `make` is not neutral and blocks,
			// a pointer's `new` hides a nil dereference, and a string has no
			// nil to be distinguished from.
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
			// The success path, where the rule is worth the most.
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

// TestASliceOfATypeThisFileCannotSpellIsRecordedRatherThanSkipped is the one
// refusal in the family that is *not* silent.
//
// Every other refusal here is a mutant equal to its original or an equivalence
// by convention, and a user has no use for being told about either. This one is
// the same fact Form D records when it cannot spell a declared type:
// go-mutants knows what it would like to write and cannot say it in Go, which
// is exactly what `unnameable-decl-type` is for. A dot import is one of the two
// ways to reach it.
func TestASliceOfATypeThisFileCannotSpellIsRecordedRatherThanSkipped(t *testing.T) {
	t.Parallel()

	// unsafe.Pointer is a basic type that still needs an import and has no
	// source form this rewrite may write; a dot import is the other way, and
	// needs a package on disk to import.
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

// TestABranchWhoseTargetThisPhaseCannotFindIsRefused is the fail-closed end of
// [fileScan.labelNamesTheNearestTarget]'s walk.
//
// The walk goes outward from the branch to the first construct its bare form
// would bind to, and the type checker has already refused a label that binds to
// nothing -- so arriving at the top without finding one is a shape no program
// has. It answers *true*, which refuses the candidate, because emitting a
// mutant on the strength of not understanding a construct is how an equivalent
// mutant becomes a survivor somebody has to argue about.
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

	// The control: with the parent index the walk finds the switch, which a
	// bare `break` would leave and the label does not.
	if (&fileScan{info: p.info, guard: full}).labelNamesTheNearestTarget(branch) {
		t.Fatal("the fixture's branch names the nearest target, which the rest of this test relies on it not doing")
	}

	// The same branch, resolved by the same checker, with nothing above it: the
	// label is a real one and there is no construct to compare it against.
	adrift := &fileScan{info: p.info, guard: &guardResolver{parent: map[ast.Node]ast.Node{}}}
	if !adrift.labelNamesTheNearestTarget(branch) {
		t.Error("a branch with no enclosing construct was not refused")
	}
}

// TestTheTypeCheckersRecordIsAskedBeforeItIsRead is the fail-closed answer two
// package-level predicates give, and the one shape the walk never hands them.
//
// A scan always holds the checker's record. Both of these are asked by
// [collectSuppressions], which runs before anything else and decides which
// regions of a file hold no mutable expression -- so an answer of "yes, that is
// a type" with nothing to read it from would suppress every index expression in
// the file and take a whole family of sites away silently.
func TestTheTypeCheckersRecordIsAskedBeforeItIsRead(t *testing.T) {
	t.Parallel()

	p := probeSource(t, "var xs []int", "xs[0]")
	if isTypeExpr(p.info, nil) {
		t.Error("isTypeExpr accepted an expression that is not there")
	}
	if isTypeExpr(nil, p.expr) {
		t.Error("isTypeExpr answered without the checker's record")
	}
	// And the control: an index that really is a type, which is what the
	// suppression exists for.
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

// TestAResultIsProbedOnlyWhereThereIsAResolverToAsk pins the two per-result
// questions the walk cannot answer without one.
//
// Both answer the safe way: no hint rather than a hint nothing computed. A
// probe hint licenses skipping an execution, so one attached on the strength of
// an absent resolver would be a mutant reported as unkillable because this
// phase could not look.
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
