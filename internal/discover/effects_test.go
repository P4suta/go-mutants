// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"testing"
)

type grammarCase struct {
	name       string
	decls      string
	signature  string
	expr       string
	effectFree bool
	panicFree  bool
}

func TestWhichExpressionsAProbeMayStandInFor(t *testing.T) {
	t.Parallel()

	for _, c := range []grammarCase{
		{name: "a name", decls: "var n int", expr: "n", effectFree: true, panicFree: true},
		{name: "a literal", expr: "42", effectFree: true, panicFree: true},
		{name: "a function literal", expr: "func() int { return 0 }", effectFree: true, panicFree: true},
		{name: "an index of a call's result by a call", decls: "func f() []int { return nil }\n\nfunc g() int { return 0 }", expr: "f()[g()]"},

		{name: "a parenthesised name", decls: "var n int", expr: "(n)", effectFree: true, panicFree: true},
		{name: "a parenthesised call", decls: "func f() int { return 0 }", expr: "(f())"},
		{name: "a parenthesised dereference", decls: "var p *int", expr: "(*p)", effectFree: true},

		{name: "a qualified constant", decls: `import "time"`, expr: "time.Nanosecond", effectFree: true, panicFree: true},
		{name: "a field of a value", decls: "var s struct{ n int }", expr: "s.n", effectFree: true, panicFree: true},
		{name: "a field through a pointer", decls: "var p *struct{ n int }", expr: "p.n", effectFree: true},
		{name: "a field of a call's result", decls: "func f() struct{ n int } { return struct{ n int }{} }", expr: "f().n"},
		{name: "a method value", decls: "import \"time\"\n\nvar d time.Duration", expr: "d.String", effectFree: true},

		{name: "a dereference", decls: "var p *int", expr: "*p", effectFree: true},
		{name: "a dereference of a call's result", decls: "func f() *int { return nil }", expr: "*f()"},
		{name: "an index", decls: "var xs []int", expr: "xs[0]", effectFree: true},
		{name: "an index by a call", decls: "var xs []int\n\nfunc f() int { return 0 }", expr: "xs[f()]"},
		{name: "an index into a call's result", decls: "func f() []int { return nil }", expr: "f()[0]"},
		{
			name:       "a generic function instantiated",
			decls:      "func pair[A any, B any](a A, b B) int { return 0 }",
			expr:       "pair[int, string]",
			effectFree: true,
		},

		{name: "a slice with every bound", decls: "var xs []int", expr: "xs[1:2:3]", effectFree: true},
		{name: "a slice with no bounds", decls: "var xs []int", expr: "xs[:]", effectFree: true},
		{name: "a slice of a call's result", decls: "func f() []int { return nil }", expr: "f()[1:]"},
		{name: "a slice whose low bound calls", decls: "var xs []int\n\nfunc f() int { return 0 }", expr: "xs[f():2]"},
		{name: "a slice whose high bound calls", decls: "var xs []int\n\nfunc f() int { return 0 }", expr: "xs[1:f()]"},
		{name: "a slice whose capacity calls", decls: "var xs []int\n\nfunc f() int { return 0 }", expr: "xs[1:2:f()]"},

		{name: "a type assertion", decls: "var v any", expr: "v.(int)", effectFree: true},
		{name: "a type assertion of a call's result", decls: "func f() any { return nil }", expr: "f().(int)"},

		{name: "a negation", decls: "var n int", expr: "-n", effectFree: true, panicFree: true},
		{name: "a negated dereference", decls: "var p *int", expr: "-(*p)", effectFree: true},
		{name: "an address", decls: "var n int", expr: "&n", effectFree: true, panicFree: true},
		{name: "a receive", decls: "var ch chan int", expr: "<-ch"},

		{name: "addition", decls: "var a, b int", expr: "a + b", effectFree: true, panicFree: true},
		{name: "addition of a call's result", decls: "var a int\n\nfunc f() int { return 0 }", expr: "a + f()"},
		{name: "a call's result plus a name", decls: "var a int\n\nfunc f() int { return 0 }", expr: "f() + a"},
		{name: "division by a non-zero constant", decls: "var a int", expr: "a / 2", effectFree: true, panicFree: true},
		{name: "division by a name", decls: "var a, b int", expr: "a / b", effectFree: true},
		{name: "a remainder by a name", decls: "var a, b int", expr: "a % b", effectFree: true},
		{name: "a shift by a constant", decls: "var a int", expr: "a << 2", effectFree: true, panicFree: true},
		{name: "a shift by a name", decls: "var a int\n\nvar k uint", expr: "a << k", effectFree: true},
		{name: "an ordering comparison", decls: "var a, b int", expr: "a < b", effectFree: true, panicFree: true},
		{name: "a conjunction", decls: "var x, y bool", expr: "x && y", effectFree: true, panicFree: true},
		{name: "a comparison of ints", decls: "var a, b int", expr: "a == b", effectFree: true, panicFree: true},
		{name: "a comparison of interfaces", decls: "var a, b any", expr: "a == b", effectFree: true},
		{name: "an interface against a concrete value", decls: "var v any\n\nvar n int", expr: "v == n", effectFree: true},
		{name: "a concrete value against an interface", decls: "var v any\n\nvar n int", expr: "n == v", effectFree: true},

		{name: "a slice literal", expr: "[]int{1}", effectFree: true, panicFree: true},
		{name: "a slice literal holding a call", decls: "func f() int { return 0 }", expr: "[]int{f()}"},
		{name: "a map literal", expr: "map[int]int{1: 2}", effectFree: true, panicFree: true},
		{name: "a map literal whose key calls", decls: "func f() int { return 0 }", expr: "map[int]int{f(): 2}"},
		{name: "a map literal whose value calls", decls: "func f() int { return 0 }", expr: "map[int]int{1: f()}"},
		{name: "a map literal keyed by an interface", decls: "var v any", expr: "map[any]int{v: 2}", effectFree: true},
		{name: "a struct literal with a named field", expr: "struct{ n int }{n: 1}", effectFree: true, panicFree: true},

		{name: "a conversion", decls: "var n int", expr: "int64(n)", effectFree: true, panicFree: true},
		{name: "a conversion of a dereference", decls: "var p *int", expr: "int64(*p)", effectFree: true},
		{name: "a conversion of a slice to an array", decls: "var xs []int", expr: "[4]int(xs)", effectFree: true},
		{name: "a conversion of a slice to a pointer to an array", decls: "var xs []int", expr: "(*[4]int)(xs)", effectFree: true},
		{name: "a conversion of a slice to a slice", decls: "type ints []int\n\nvar xs []int", expr: "ints(xs)", effectFree: true, panicFree: true},
		{name: "a conversion of a call's result", decls: "func f() int { return 0 }", expr: "int64(f())"},
		{name: "a builtin over a slice", decls: "var xs []int", expr: "len(xs)", effectFree: true, panicFree: true},
		{name: "a builtin over a call's result", decls: "func f() []int { return nil }", expr: "len(f())"},
		{name: "a builtin over a dereference", decls: "var p *[4]int", expr: "min(len(*p), 2)", effectFree: true},
		{name: "new", expr: "new(int)", effectFree: true, panicFree: true},
		{name: "make, which allocates but may also panic", decls: "var n int", expr: "make([]int, n)", effectFree: true},
		{name: "an ordinary call", decls: "func f() int { return 0 }", expr: "f()"},
		{
			name:  "append",
			decls: "var xs []int",
			expr:  "append(xs, 1)",
		},
		{name: "copy", decls: "var xs, ys []int", expr: "copy(xs, ys)"},
		{name: "recover", expr: "recover()"},
		{
			name:  "a call of a function the package named after a builtin",
			decls: "func len(xs []int) int { return 0 }\n\nvar xs []int",
			expr:  "len(xs)",
		},
		{
			name:  "an unsafe builtin",
			decls: "import \"unsafe\"\n\nvar n int64",
			expr:  "unsafe.Sizeof(n)",
		},
		{
			name:  "a builtin called through parentheses",
			decls: "var xs []int",
			expr:  "(len)(xs)",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			signature := c.signature
			if signature == "" {
				signature = "()"
			}
			p := probeIn(t, c.decls, signature, c.expr)
			g := p.resolver()
			if got := g.effectFree(p.expr); got != c.effectFree {
				t.Errorf("effectFree(%s) = %v, want %v", c.expr, got, c.effectFree)
			}
			if got := g.panicFree(p.expr); got != c.panicFree {
				t.Errorf("panicFree(%s) = %v, want %v", c.expr, got, c.panicFree)
			}
			if c.panicFree && !c.effectFree {
				t.Errorf("the row claims %s cannot panic but may have effects, and P is a subset of E", c.expr)
			}
		})
	}
}

func TestAnExpressionThatIsNotThereSatisfiesNeitherGrammar(t *testing.T) {
	t.Parallel()

	g := probeSource(t, "var n int", "n").resolver()
	if g.effectFree(nil) {
		t.Error("effectFree(nil) = true, want false")
	}
	if g.panicFree(nil) {
		t.Error("panicFree(nil) = true, want false")
	}
	if !g.effectFreeOrAbsent(nil) {
		t.Error("effectFreeOrAbsent(nil) = false, want true: an absent slice bound is no evaluation")
	}
}

func TestNeitherGrammarAnswersWithoutTheCheckersRecord(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name  string
		decls string
		expr  string
	}{
		{name: "a qualified constant", decls: `import "time"`, expr: "time.Nanosecond"},
		{name: "a field of a value", decls: "var s struct{ n int }", expr: "s.n"},
		{name: "a conversion", decls: "var n int", expr: "int64(n)"},
		{name: "a builtin", decls: "var xs []int", expr: "len(xs)"},
		{name: "a division by a constant", decls: "var a int", expr: "a / 2"},
		{name: "a conversion of a slice", decls: "var xs []int", expr: "[4]int(xs)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			p := probeSource(t, c.decls, c.expr)
			blind := newGuardResolver(p.file, nil, nil, p.tokFile, nil)
			if blind.panicFree(p.expr) {
				t.Errorf("panicFree(%s) with no record = true, want false", c.expr)
			}
		})
	}
}

func TestWhichOperandsAStatementEvaluates(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		stmt string
		want int
	}{
		{name: "an expression statement", stmt: "f()", want: 1},
		{name: "an assignment", stmt: "a, b = b, a", want: 4},
		{name: "an increment", stmt: "a++", want: 1},
		{name: "a send", stmt: "ch <- a", want: 2},
		{name: "a return", stmt: "return", want: 0},
		{name: "a go statement", stmt: "go f()", want: 1},
		{name: "a defer statement", stmt: "defer f()", want: 1},
		{name: "an if", stmt: "if a > b {\n\t}", want: 1},
		{name: "a for", stmt: "for a > b {\n\t}", want: 1},
		{name: "a range", stmt: "for k, v := range m {\n\t\t_, _ = k, v\n\t}", want: 3},
		{name: "a switch", stmt: "switch a {\n\t}", want: 1},
		{name: "a type switch", stmt: "switch v := any(a).(type) {\n\tdefault:\n\t\t_ = v\n\t}"},
		{name: "a select", stmt: "select {\n\tcase <-ch:\n\t}"},
		{name: "a block", stmt: "{\n\t\ta = b\n\t}"},
		{name: "a label", stmt: "outer:\n\tfor {\n\t\tbreak outer\n\t}"},
		{name: "a declaration", stmt: "var z int\n\t_ = z", want: 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			const decls = "var a, b int\n\nvar ch chan int\n\nvar m map[int]int\n\nfunc f() {}"
			src := "package pkg\n\n" + decls + "\n\nfunc probe() {\n\t" + c.stmt + "\n}\n"
			file := parseProbe(t, src)
			body := probeBody(t, file)
			if len(body) == 0 {
				t.Fatalf("the fixture has no statement:\n%s", src)
			}
			if got := statementOperands(body[0]); len(got) != c.want {
				t.Errorf("statementOperands(%s) yielded %d operands, want %d", c.stmt, len(got), c.want)
			}
		})
	}

	clause := &ast.CaseClause{List: []ast.Expr{ast.NewIdent("a"), ast.NewIdent("b")}}
	clause.Body = []ast.Stmt{&ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("f")}}}
	if got := statementOperands(clause); len(got) != 2 {
		t.Errorf("statementOperands(a case clause) yielded %d operands, want its 2 labels", len(got))
	}

	if got := statementOperands(&ast.EmptyStmt{}); got != nil {
		t.Errorf("statementOperands(an empty statement) = %v, want nothing", got)
	}
}

func TestTheResolverReadsAValuesTypeAndNotATypesName(t *testing.T) {
	t.Parallel()

	p := probeSource(t, "var n int", "int64(n)")
	g := p.resolver()
	call, isCall := p.expr.(*ast.CallExpr)
	if !isCall {
		t.Fatalf("the fixture yielded %T, want a conversion", p.expr)
	}

	if got := g.typeOf(call.Fun); got != nil {
		t.Errorf("typeOf(a type expression) = %v, want nil", got)
	}
	if got := g.typeOf(call); got == nil || got.String() != "int64" {
		t.Errorf("typeOf(a conversion) = %v, want int64", got)
	}
	if !g.isTypeExpr(call.Fun) {
		t.Error("isTypeExpr refused the target of a conversion")
	}
	if g.isTypeExpr(call) {
		t.Error("isTypeExpr accepted a conversion, which is a value")
	}
	if g.isTypeExpr(nil) {
		t.Error("isTypeExpr accepted nothing at all")
	}
	if got := g.typeOf(nil); got != nil {
		t.Errorf("typeOf(nothing at all) = %v, want nil", got)
	}
	if got := g.constantValue(nil); got != nil {
		t.Errorf("constantValue(nothing at all) = %v, want nil", got)
	}

	blind := &guardResolver{parent: g.parent}
	if got := blind.typeOf(call); got != nil {
		t.Errorf("typeOf with no record = %v, want nil", got)
	}
	if blind.isTypeExpr(call.Fun) {
		t.Error("isTypeExpr with no record accepted a type expression")
	}
	if got := blind.constantValue(call); got != nil {
		t.Errorf("constantValue with no record = %v, want nil", got)
	}

	folded := probeSource(t, "const two = 1 + 1", "two")
	if got := folded.resolver().constantValue(folded.expr); got == nil || got.String() != "2" {
		t.Errorf("constantValue(a folded constant) = %v, want 2", got)
	}
	named := probeSource(t, "var n int", "n")
	if got := named.resolver().constantValue(named.expr); got != nil {
		t.Errorf("constantValue(a variable) = %v, want nothing", got)
	}
}

func TestWhichReplacementsCanIntroduceAPanicOfTheirOwn(t *testing.T) {
	t.Parallel()

	binaryOf := func(t *testing.T, decls, expr string) (*guardResolver, ast.Node) {
		t.Helper()
		p := probeSource(t, decls, expr)
		binary, isBinary := p.expr.(*ast.BinaryExpr)
		if !isBinary {
			t.Fatalf("the fixture yielded %T, want a binary expression", p.expr)
		}
		return p.resolver(), binary
	}

	t.Run("a replacement that is not a division never introduces one", func(t *testing.T) {
		t.Parallel()

		g, anchor := binaryOf(t, "var a, b int", "a + b")
		for _, replacement := range []string{"-", "*", "&", "|", "<", "<=", "==", "&&", ""} {
			if g.introducesPanic(anchor, replacement) {
				t.Errorf("introducesPanic(%q) = true, want false", replacement)
			}
		}
	})

	t.Run("a division by a non-zero constant does not", func(t *testing.T) {
		t.Parallel()

		g, anchor := binaryOf(t, "var a int", "a * 2")
		for _, replacement := range []string{"/", "%"} {
			if g.introducesPanic(anchor, replacement) {
				t.Errorf("introducesPanic(%q) over a constant divisor = true, want false", replacement)
			}
		}
	})

	t.Run("a division by a name does", func(t *testing.T) {
		t.Parallel()

		g, anchor := binaryOf(t, "var a, b int", "a * b")
		for _, replacement := range []string{"/", "%"} {
			if !g.introducesPanic(anchor, replacement) {
				t.Errorf("introducesPanic(%q) over a variable divisor = false, want true", replacement)
			}
		}
	})

	t.Run("a division by a zero constant does", func(t *testing.T) {
		t.Parallel()

		g, anchor := binaryOf(t, "var a int", "a * 0")
		if !g.introducesPanic(anchor, "/") {
			t.Error("introducesPanic over a zero constant divisor = false, want true")
		}
	})

	t.Run("a floating division does not", func(t *testing.T) {
		t.Parallel()

		g, anchor := binaryOf(t, "var a, b float64", "a * b")
		if g.introducesPanic(anchor, "/") {
			t.Error("introducesPanic over a floating divisor = true, want false")
		}
	})

	t.Run("an anchor that is not the expression is refused", func(t *testing.T) {
		t.Parallel()

		p := probeSource(t, "var a, b int", "a * b")
		if !p.resolver().introducesPanic(p.file, "/") {
			t.Error("introducesPanic over an anchor that is not a binary expression = false, want true")
		}
	})
}

func TestAnExpressionWithNoEnclosingStatementIsNotInContext(t *testing.T) {
	t.Parallel()

	p := probeSource(t, "var total = 1 + 2", "total")
	g := p.resolver()

	var initialiser ast.Expr
	for node := range g.parent {
		if binary, isBinary := node.(*ast.BinaryExpr); isBinary {
			initialiser = binary
		}
	}
	if initialiser == nil {
		t.Fatal("the fixture holds no package-level initialiser")
	}
	if g.inertContext(initialiser) {
		t.Error("inertContext = true for an expression with no enclosing statement, want false")
	}
	if !g.inertContext(p.expr) {
		t.Error("inertContext = false for an expression inside a statement, want true")
	}
}

func TestTheGrammarsAnswerAboutNodesTheParserCouldNotHaveMade(t *testing.T) {
	t.Parallel()

	g := probeSource(t, "var n int", "n").resolver()
	call := &ast.CallExpr{Fun: ast.NewIdent("f")}
	name := ast.NewIdent("T")

	if g.effectFree(call) {
		t.Fatal("the fixture's stand-in call is effect-free, which the rows below rely on")
	}
	for _, c := range []struct {
		name string
		expr ast.Expr
	}{
		{
			name: "an instantiation whose base has effects",
			expr: &ast.IndexListExpr{X: call, Indices: []ast.Expr{name, name}},
		},
		{
			name: "an instantiation whose index has effects",
			expr: &ast.IndexListExpr{X: ast.NewIdent("pair"), Indices: []ast.Expr{name, call}},
		},
	} {
		if g.effectFree(c.expr) {
			t.Errorf("effectFree(%s) = true, want false", c.name)
		}
	}

	arrow := &ast.BinaryExpr{X: ast.NewIdent("a"), Op: token.ARROW, Y: ast.NewIdent("b")}
	if g.panicFree(arrow) {
		t.Error("panicFree accepted a binary operator the list does not name")
	}
	if !g.effectFree(arrow) {
		t.Error("effectFree refused a binary expression of two names, whatever its operator")
	}
}
