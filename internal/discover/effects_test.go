// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"testing"
)

// The two grammars a probe hint is decided by, asked as questions about
// expressions.
//
// effects.go argues at length why a probe hint may only be attached where
// evaluating the statement's operands has no effect (grammar E) and where the
// probed operand cannot panic (grammar P). Both are allowlists over the syntax,
// and an allowlist is only as good as its refusals: admitting one expression
// too many attaches a hint to a site where the probe's execution is not the
// original's, and the consequence is a mutant reported as unkillable that a
// test really could have killed. There is no diagnostic for that, so the
// refusals are stated here one shape at a time.

// grammarCase is one expression and the two answers it must get.
type grammarCase struct {
	name  string
	decls string
	// signature is the probe function's, for the shapes only a generic function
	// can hold. Empty means `()`.
	signature  string
	expr       string
	effectFree bool
	panicFree  bool
}

// TestWhichExpressionsAProbeMayStandInFor is grammar E and grammar P over one
// table, because P is a subset of E and a row that claimed otherwise would be a
// row about a contradiction.
func TestWhichExpressionsAProbeMayStandInFor(t *testing.T) {
	t.Parallel()

	for _, c := range []grammarCase{
		// The leaves. A function literal creates a closure and evaluates none of
		// its body, which is why it stands beside a name and a number.
		{name: "a name", decls: "var n int", expr: "n", effectFree: true, panicFree: true},
		{name: "a literal", expr: "42", effectFree: true, panicFree: true},
		{name: "a function literal", expr: "func() int { return 0 }", effectFree: true, panicFree: true},

		// Parentheses change nothing, and are asked twice so that an edit which
		// stopped looking inside them is visible from both directions.
		{name: "a parenthesised name", decls: "var n int", expr: "(n)", effectFree: true, panicFree: true},
		{name: "a parenthesised call", decls: "func f() int { return 0 }", expr: "(f())"},
		{name: "a parenthesised dereference", decls: "var p *int", expr: "(*p)", effectFree: true},

		// Selections.
		{name: "a qualified constant", decls: `import "time"`, expr: "time.Nanosecond", effectFree: true, panicFree: true},
		{name: "a field of a value", decls: "var s struct{ n int }", expr: "s.n", effectFree: true, panicFree: true},
		{name: "a field through a pointer", decls: "var p *struct{ n int }", expr: "p.n", effectFree: true},
		{name: "a field of a call's result", decls: "func f() struct{ n int } { return struct{ n int }{} }", expr: "f().n"},
		{name: "a method value", decls: "import \"time\"\n\nvar d time.Duration", expr: "d.String", effectFree: true},

		// Dereferences and indexing: no effect, but each can panic.
		{name: "a dereference", decls: "var p *int", expr: "*p", effectFree: true},
		{name: "a dereference of a call's result", decls: "func f() *int { return nil }", expr: "*f()"},
		{name: "an index", decls: "var xs []int", expr: "xs[0]", effectFree: true},
		{name: "an index by a call", decls: "var xs []int\n\nfunc f() int { return 0 }", expr: "xs[f()]"},
		{name: "an index into a call's result", decls: "func f() []int { return nil }", expr: "f()[0]"},
		{
			name:  "a generic function instantiated",
			decls: "func pair[A any, B any](a A, b B) int { return 0 }",
			// P has no case for an index of any kind, generic instantiation
			// included: indexing panics, and a hint is declined rather than
			// reasoned about one index expression at a time.
			expr:       "pair[int, string]",
			effectFree: true,
		},

		// Slices, whose three optional bounds are the reason
		// effectFreeOrAbsent exists.
		{name: "a slice with every bound", decls: "var xs []int", expr: "xs[1:2:3]", effectFree: true},
		{name: "a slice with no bounds", decls: "var xs []int", expr: "xs[:]", effectFree: true},
		{name: "a slice of a call's result", decls: "func f() []int { return nil }", expr: "f()[1:]"},
		{name: "a slice whose low bound calls", decls: "var xs []int\n\nfunc f() int { return 0 }", expr: "xs[f():2]"},
		{name: "a slice whose high bound calls", decls: "var xs []int\n\nfunc f() int { return 0 }", expr: "xs[1:f()]"},
		{name: "a slice whose capacity calls", decls: "var xs []int\n\nfunc f() int { return 0 }", expr: "xs[1:2:f()]"},

		// Type assertions.
		{name: "a type assertion", decls: "var v any", expr: "v.(int)", effectFree: true},
		{name: "a type assertion of a call's result", decls: "func f() any { return nil }", expr: "f().(int)"},

		// Unary operators. `<-` receives, which is an effect and a block.
		{name: "a negation", decls: "var n int", expr: "-n", effectFree: true, panicFree: true},
		{name: "a negated dereference", decls: "var p *int", expr: "-(*p)", effectFree: true},
		{name: "an address", decls: "var n int", expr: "&n", effectFree: true, panicFree: true},
		{name: "a receive", decls: "var ch chan int", expr: "<-ch"},

		// Binary operators, where P has more to say than E.
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

		// Composite literals, and the map keys that can panic on insertion.
		{name: "a slice literal", expr: "[]int{1}", effectFree: true, panicFree: true},
		{name: "a slice literal holding a call", decls: "func f() int { return 0 }", expr: "[]int{f()}"},
		{name: "a map literal", expr: "map[int]int{1: 2}", effectFree: true, panicFree: true},
		{name: "a map literal whose key calls", decls: "func f() int { return 0 }", expr: "map[int]int{f(): 2}"},
		{name: "a map literal whose value calls", decls: "func f() int { return 0 }", expr: "map[int]int{1: f()}"},
		{name: "a map literal keyed by an interface", decls: "var v any", expr: "map[any]int{v: 2}", effectFree: true},
		{name: "a struct literal with a named field", expr: "struct{ n int }{n: 1}", effectFree: true, panicFree: true},

		// Calls: a conversion, a builtin, and everything else.
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
			// A builtin, and not one of the ones either table names. append may
			// reallocate and copy, which is an effect the mutant that skipped
			// the operand would not have; make is in E and not in P because a
			// negative length panics. Both are the row that separates "is a
			// builtin" from "is one of these builtins".
			name:  "append",
			decls: "var xs []int",
			expr:  "append(xs, 1)",
		},
		{name: "copy", decls: "var xs, ys []int", expr: "copy(xs, ys)"},
		{name: "recover", expr: "recover()"},
		{
			// The name is the builtin's and the object is not, so the call runs
			// the package's own code.
			name:  "a call of a function the package named after a builtin",
			decls: "func len(xs []int) int { return 0 }\n\nvar xs []int",
			expr:  "len(xs)",
		},
		{
			// A builtin with no universe parent. unsafe's read memory the type
			// system is deliberately not describing.
			name:  "an unsafe builtin",
			decls: "import \"unsafe\"\n\nvar n int64",
			expr:  "unsafe.Sizeof(n)",
		},
		{
			// A parenthesised callee, refused on purpose: the question is about
			// a small set of exact shapes and an indirect spelling costs one
			// unprobed mutant.
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

// TestAnExpressionThatIsNotThereSatisfiesNeitherGrammar is what
// [guardResolver.effectFreeOrAbsent] rests on, from the other side.
//
// A slice expression's bounds are optional, and a missing one is no evaluation
// at all -- which is why the optional form says yes to nil. Both grammars
// themselves say no to it, and the two answers are not in tension: one is about
// an expression that is not written, the other about one that is not there to
// look at. The refusal is pinned because it is the fall-through of an allowlist,
// and a switch gains cases.
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

// TestNeitherGrammarAnswersWithoutTheCheckersRecord is the fail-closed half of
// both, and the reason each predicate asks before it reads.
//
// Every interesting answer here rests on what the checker recorded: which call
// is a conversion, which selector is a package, what a constant folded to. With
// no record there is no evidence, and a grammar that said yes anyway would
// attach a probe hint on the strength of the syntax alone.
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

// TestWhichOperandsAStatementEvaluates pins [statementOperands], which is the
// set grammar E is asked of.
//
// It is a list rather than a walk on purpose: a statement kind nobody has
// thought about yields no operands and therefore no hint, and an empty list is
// the refusal. What makes each row worth stating is that the operands are the
// ones *this statement* evaluates -- a `range` clause evaluates its key, its
// value and the thing ranged over, and a `case` clause its labels.
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

	// A case clause evaluates its labels and not its body, which is the whole
	// distinction this list exists to draw. It is reached from a nested
	// position rather than as a statement of its own, so it is built here.
	clause := &ast.CaseClause{List: []ast.Expr{ast.NewIdent("a"), ast.NewIdent("b")}}
	clause.Body = []ast.Stmt{&ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("f")}}}
	if got := statementOperands(clause); len(got) != 2 {
		t.Errorf("statementOperands(a case clause) yielded %d operands, want its 2 labels", len(got))
	}

	// And the fall-through: a statement kind the list has no case for yields
	// nothing, which is how a hint is declined rather than guessed at.
	if got := statementOperands(&ast.EmptyStmt{}); got != nil {
		t.Errorf("statementOperands(an empty statement) = %v, want nothing", got)
	}
}

// TestTheResolverReadsAValuesTypeAndNotATypesName is
// [guardResolver.typeOf] and [guardResolver.isTypeExpr], the pair every form
// that writes a type down goes through.
//
// The checker records an entry for a type expression as well as for a value
// one -- `int64` in `int64(n)` has a [types.TypeAndValue] of its own -- and the
// two are told apart by [types.TypeAndValue.IsValue] rather than by the
// presence of the entry. Reading the type out of either would hand a form the
// conversion's *target* where it asked for the value's type, and the two differ
// in every conversion that does anything. It is also what tells a conversion
// from a call, which is the difference between "computes a value from bits it
// has" and "runs code this phase cannot see".
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

	// And a constant is read from the checker's folding rather than from the
	// spelling: `2` and `1 + 1` are one value, and a shift by either is
	// admitted for the same reason.
	folded := probeSource(t, "const two = 1 + 1", "two")
	if got := folded.resolver().constantValue(folded.expr); got == nil || got.String() != "2" {
		t.Errorf("constantValue(a folded constant) = %v, want 2", got)
	}
	named := probeSource(t, "var n int", "n")
	if got := named.resolver().constantValue(named.expr); got != nil {
		t.Errorf("constantValue(a variable) = %v, want nothing", got)
	}
}

// TestWhichReplacementsCanIntroduceAPanicOfTheirOwn pins
// [guardResolver.introducesPanic], which is the condition that separates a
// probe hint from a wrong answer.
//
// A probe stands in for a mutant by evaluating the replacement beside the
// original. Almost every replacement is as safe as what it replaces -- swapping
// `+` for `-` cannot fail where `+` did not -- but `/` and `%` introduce an
// operation the original did not have, and a mutant that divides by zero in the
// probe tree takes the probe run down rather than recording a difference.
//
// The refusal of an anchor that is not the binary expression is the fail-closed
// half. It cannot happen for the rules that produce these replacements, and
// "cannot happen" is the wrong thing to spell as "carry on" in a function whose
// answer licenses skipping a test.
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

		// `a * 0` is legal and `a / 0` is not, so the replacement really would
		// not compile -- which is a rejection rather than a panic. The answer
		// here is still "yes", because this function's job is to keep the probe
		// from claiming anything about it.
		g, anchor := binaryOf(t, "var a int", "a * 0")
		if !g.introducesPanic(anchor, "/") {
			t.Error("introducesPanic over a zero constant divisor = false, want true")
		}
	})

	t.Run("a floating division does not", func(t *testing.T) {
		t.Parallel()

		// Division by zero is defined for floating point: it yields an
		// infinity, and `%` is not legal on floats at all.
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

// TestAnExpressionWithNoEnclosingStatementIsNotInContext pins
// [guardResolver.inertContext]'s fall-through.
//
// The ordering rule the two probe forms rest on is about the operands of one
// *statement*, and a package-level declaration's initialiser has none: its
// ordering is the initialisation order, which is a different rule entirely.
// Discovery records such sites as `package-var-init` and never asks, so this is
// the fail-closed answer to a shape that should not arrive -- and a `true` here
// would licence skipping a test on the strength of a rule that does not apply.
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
	// And the ordinary case, so that the refusal above is about the absence of
	// a statement and not about the expression.
	if !g.inertContext(p.expr) {
		t.Error("inertContext = false for an expression inside a statement, want true")
	}
}
