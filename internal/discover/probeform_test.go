// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// Which sites a probe tree can stand in for, and which form it uses.
//
// A probe hint licenses *skipping an execution*: the consumer reads "this
// mutant's site never differed, so no test that ran could have killed it" and
// does not run it. A hint attached where the probe's execution is not the
// original's is therefore a mutant reported as unkillable that a test really
// could have killed -- and there is no diagnostic for that, which is why every
// condition is asked before the hint goes on rather than after.
//
// The forms are asked in order and the order is the whole design: Form B needs
// no type written out and no temporary, so a bool-valued site takes it; the
// value form is the same question one step further out; and the reach form
// records only that a statement ran, which is all a deleted statement can say.

// probeOf runs the form staircase over one anchor and returns the hint.
func probeOf(t *testing.T, src string, find func(ast.Node) bool) (*guardResolver, Guard, bool) {
	t.Helper()

	g := guardOver(t, src)
	var anchor ast.Node
	for node := range g.parent {
		if !find(node) {
			continue
		}
		if anchor == nil || node.Pos() < anchor.Pos() {
			anchor = node
		}
	}
	if anchor == nil {
		t.Fatalf("the fixture holds no anchor:\n%s", src)
	}
	guard, ok := g.guardFor(anchor)
	return g, guard, ok
}

// TestWhichProbeFormASiteGets is the staircase, one row per form and one per
// refusal.
func TestWhichProbeFormASiteGets(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		find func(ast.Node) bool
		want ProbeForm
	}{
		{
			// A comparison is exactly `bool`, so the cheapest form applies: the
			// original and the mutant are both evaluated where they stand and
			// the call records whether they ever disagreed.
			name: "a boolean site",
			src:  "package pkg\n\nfunc probe(a, b int) {\n\tif a < b {\n\t}\n}\n",
			find: binaryOp(token.LSS),
			want: ProbeFormBool,
		},
		{
			// Not a bool, so the value form: a temporary of the site's own type
			// holds the original and the mutant is compared against it.
			name: "an arithmetic site",
			src:  "package pkg\n\nfunc probe(a, b int) {\n\tn := a * b\n\t_ = n\n}\n",
			find: binaryOp(token.MUL),
			want: ProbeFormValue,
		},
		{
			// A call has effects the mutant would not have, so a second
			// evaluation of it is not the original's execution.
			name: "a site whose operand calls",
			src: "package pkg\n\nfunc f() int { return 0 }\n\n" +
				"func probe(a int) {\n\tn := a * f()\n\t_ = n\n}\n",
			find: binaryOp(token.MUL),
		},
		{
			// A float comparison is not an equality: NaN is not equal to
			// itself, so "did it differ" has no answer for one.
			name: "a floating site",
			src:  "package pkg\n\nfunc probe(a, b float64) {\n\tn := a * b\n\t_ = n\n}\n",
			find: binaryOp(token.MUL),
		},
		{
			// Comparing two interface values panics when the dynamic types are
			// not comparable, and a probe that panicked would take the probe
			// run down rather than record a difference.
			name: "a site of interface type",
			src: "package pkg\n\nfunc pick(a, b any) any { return a }\n\n" +
				"func probe(a, b any) {\n\tv := pick(a, b)\n\t_ = v\n}\n",
			find: func(node ast.Node) bool { _, ok := node.(*ast.CallExpr); return ok },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			_, guard, ok := probeOf(t, c.src, c.find)
			if !ok {
				t.Fatalf("guardFor found no form for:\n%s", c.src)
			}
			if c.want == "" {
				if guard.Probe != nil {
					t.Errorf("the site carries a %s hint, want none", guard.Probe.Form)
				}
				return
			}
			if guard.Probe == nil {
				t.Fatalf("the site carries no probe hint, want %s", c.want)
			}
			if guard.Probe.Form != c.want {
				t.Errorf("the hint is %s, want %s", guard.Probe.Form, c.want)
			}
			if !guard.Probe.Span.Contains(guard.Probe.Span) {
				t.Error("the hint's span does not contain itself")
			}
			if c.want == ProbeFormValue && len(guard.Probe.Types) != 1 {
				t.Errorf("a value hint carries the types %v, want exactly one", guard.Probe.Types)
			}
			if c.want != ProbeFormValue && len(guard.Probe.Types) != 0 {
				t.Errorf("a %s hint carries the types %v, want none", c.want, guard.Probe.Types)
			}
		})
	}
}

// TestOnlyAStatementFormCarriesAReachabilityHint pins
// [guardResolver.reachProbe], the weakest form and the one the
// statement-deletion family can have no other of.
//
// A deleted statement's mutant differs by the *absence* of an effect, which
// nothing a probe tree evaluates can see; what such a tree can record is that
// the statement ran at all. The shape is a call in front of the statement, so
// the condition is a Form S guard and nothing else -- an expression site has no
// statement to stand in front of, and Form F has already moved the statement
// into a closure.
func TestOnlyAStatementFormCarriesAReachabilityHint(t *testing.T) {
	t.Parallel()

	g := guardOver(t, "package pkg\n\nfunc probe(n int) {\n\tn = 1\n\t_ = n\n}\n")
	span := mustSpan(t, 0, 4)

	for _, form := range []GuardForm{GuardFormC, GuardFormD, GuardFormE, GuardFormF, GuardFormCPrime} {
		if got := g.reachProbe(Guard{Form: form, SiteSpan: span}); got != nil {
			t.Errorf("a Form %s site carries a %s hint, want none", form, got.Form)
		}
	}
	hint := g.reachProbe(Guard{Form: GuardFormS, SiteSpan: span})
	if hint == nil {
		t.Fatal("a Form S site carries no reachability hint")
	}
	if hint.Form != ProbeFormReach {
		t.Errorf("the hint is %s, want %s", hint.Form, ProbeFormReach)
	}
	if hint.Span != span {
		t.Errorf("the hint spans %s, want the site's %s", hint.Span, span)
	}
	if len(hint.Types) != 0 || len(hint.Imports) != 0 {
		t.Errorf("a reachability hint carries %v and %v, and needs neither", hint.Types, hint.Imports)
	}
}

// mustSpan builds a span for a test, or fails.
func mustSpan(t *testing.T, start, end uint32) mutation.Span {
	t.Helper()

	span, err := mutation.NewSpan(start, end)
	if err != nil {
		t.Fatalf("NewSpan(%d, %d): %v", start, end, err)
	}
	return span
}

// TestAValueProbeWalksOutwardToSomethingComparable is
// [guardResolver.valueProbe]'s search, asked where the anchor itself cannot
// carry one.
//
// The walk is the guard's own, and it continues past an expression it cannot
// use rather than stopping there: the site is the nearest *usable* ancestor,
// not the nearest one. What ends it is a parent that is not an expression,
// because the shape it writes is a temporary beside the statement and there is
// no statement above a statement to put one beside.
func TestAValueProbeWalksOutwardToSomethingComparable(t *testing.T) {
	t.Parallel()

	g := guardOver(t, "package pkg\n\nfunc probe(a, b int) {\n\tn := (a * b) + 1\n\t_ = n\n}\n")
	var inner ast.Node
	for node := range g.parent {
		if binary, isBinary := node.(*ast.BinaryExpr); isBinary && binary.Op == token.MUL {
			inner = binary
		}
	}
	if inner == nil {
		t.Fatal("the fixture holds no multiplication")
	}
	hint := g.valueProbe(inner)
	if hint == nil {
		t.Fatal("valueProbe found nothing for an arithmetic site")
	}
	if hint.Form != ProbeFormValue {
		t.Errorf("the hint is %s, want %s", hint.Form, ProbeFormValue)
	}
	if len(hint.Types) != 1 || hint.Types[0] != "int" {
		t.Errorf("the hint carries the types %v, want [int]", hint.Types)
	}

	// And a package-level initialiser, whose walk ends at a declaration rather
	// than at a statement: the ordering rule the form rests on is about the
	// operands of one statement, and there is none.
	outside := guardOver(t, "package pkg\n\nvar total = 2 * 2\n")
	for node := range outside.parent {
		binary, isBinary := node.(*ast.BinaryExpr)
		if !isBinary {
			continue
		}
		if got := outside.valueProbe(binary); got != nil {
			t.Errorf("valueProbe = %+v for a package-level initialiser, want none", got)
		}
	}
}

// TestABooleanProbeIsRefusedWhereASecondEvaluationWouldDiffer is Form B's own
// conditions, which are stricter than the value form's and stricter for a
// reason.
//
// The bool form evaluates *both* readings where they stand, so the second
// evaluation is of the whole site rather than of one operand -- and the
// operands a connective evaluates are not the same on both readings. `x != nil
// && x.ok` evaluates `x.ok` only when the first half held, and `||` in its
// place evaluates it when the first half did not.
func TestABooleanProbeIsRefusedWhereASecondEvaluationWouldDiffer(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "a comparison of names",
			src:  "package pkg\n\nfunc probe(a, b int) {\n\tif a < b {\n\t}\n}\n",
			want: true,
		},
		{
			name: "a comparison whose operand calls",
			src: "package pkg\n\nfunc f() int { return 0 }\n\n" +
				"func probe(a int) {\n\tif a < f() {\n\t}\n}\n",
		},
		{
			name: "a comparison whose operand may panic",
			src:  "package pkg\n\nfunc probe(a int, p *int) {\n\tif a < *p {\n\t}\n}\n",
		},
		{
			name: "a connective guarding a dereference",
			src:  "package pkg\n\nfunc probe(p *int, n int) {\n\tif p != nil && *p < n {\n\t}\n}\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := guardOver(t, c.src)
			var cond ast.Expr
			for node := range g.parent {
				if ifStmt, isIf := node.(*ast.IfStmt); isIf {
					cond = ifStmt.Cond
				}
			}
			if cond == nil {
				t.Fatalf("the fixture holds no if:\n%s", c.src)
			}
			guard, ok := g.guardFor(cond)
			if !ok {
				t.Fatalf("guardFor found no form for the condition:\n%s", c.src)
			}
			carries := guard.Probe != nil && guard.Probe.Form == ProbeFormBool
			if carries != c.want {
				got := "none"
				if guard.Probe != nil {
					got = string(guard.Probe.Form)
				}
				t.Errorf("the condition carries a %s hint, want a boolean one = %v", got, c.want)
			}
		})
	}
}

// TestWhichReturnsAProbeCanStandInFor is [guardResolver.probeSite], which is
// the one hint computed for a whole statement rather than for one site.
//
// The return form is the strongest evidence there is: it compares the value the
// function would really have returned, after the conversion the `return` itself
// performs. What it costs is that the *whole statement* has to be safe --
// the rewrite declares a temporary per result and evaluates every operand once
// in source order, which is not the order the compiler uses, so one operand
// with an effect makes every reading of the statement a different execution.
func TestWhichReturnsAProbeCanStandInFor(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name  string
		src   string
		want  bool
		types []string
	}{
		{
			name:  "two results of nameable types",
			src:   "package pkg\n\nfunc probe(n int, s string) (int, string) {\n\treturn n, s\n}\n",
			want:  true,
			types: []string{"int", "string"},
		},
		{
			name:  "one result",
			src:   "package pkg\n\nfunc probe(n int) int {\n\treturn n\n}\n",
			want:  true,
			types: []string{"int"},
		},
		{
			name: "a result that calls",
			src: "package pkg\n\nfunc f() int { return 0 }\n\n" +
				"func probe() (int, int) {\n\treturn 1, f()\n}\n",
		},
		{
			name: "a result beside one that calls",
			src: "package pkg\n\nfunc f() int { return 0 }\n\n" +
				"func probe() (int, int) {\n\treturn f(), 1\n}\n",
		},
		{
			name: "a bare return in a function with results",
			src:  "package pkg\n\nfunc probe() (n int) {\n\treturn\n}\n",
		},
		{
			// A single call returning the whole tuple. The statement has one
			// result expression and the signature has two, so there is no
			// operand-per-result rewrite to make.
			name: "a return of a call's whole tuple",
			src: "package pkg\n\nfunc pair() (int, int) { return 0, 0 }\n\n" +
				"func probe() (int, int) {\n\treturn pair()\n}\n",
		},
		{
			// A type parameter has no source form outside the generic
			// declaration it belongs to, and the rewrite declares a temporary
			// of the result's type.
			name: "a result of a type parameter's type",
			src:  "package pkg\n\nfunc probe[T any](v T) T {\n\treturn v\n}\n",
		},
		{
			name: "a result of a type this file cannot spell",
			src: "package pkg\n\nimport \"unsafe\"\n\n" +
				"func probe(p unsafe.Pointer) unsafe.Pointer {\n\treturn p\n}\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := guardOver(t, c.src)
			// The last return in source order, which is `probe`'s: a fixture
			// that needs a helper declares it first, and map iteration is not
			// an order.
			var stmt *ast.ReturnStmt
			for node := range g.parent {
				ret, isReturn := node.(*ast.ReturnStmt)
				if isReturn && (stmt == nil || ret.Pos() > stmt.Pos()) {
					stmt = ret
				}
			}
			if stmt == nil {
				t.Fatalf("the fixture holds no return:\n%s", c.src)
			}
			results := resultsOfProbe(t, g)
			hint := g.probeSite(stmt, results)
			if (hint != nil) != c.want {
				t.Fatalf("probeSite = %+v, want a hint = %v", hint, c.want)
			}
			if !c.want {
				return
			}
			if hint.Form != ProbeFormReturn {
				t.Errorf("the hint is %s, want %s", hint.Form, ProbeFormReturn)
			}
			if len(hint.Types) != len(c.types) {
				t.Fatalf("the hint carries %v, want %v", hint.Types, c.types)
			}
			for i, want := range c.types {
				if hint.Types[i] != want {
					t.Errorf("result %d is typed %q, want %q", i, hint.Types[i], want)
				}
			}
		})
	}

	// And the two shapes the statement is refused for before anything is
	// spelled, which the caller cannot produce and which are the fail-closed
	// answer to a caller that could.
	g := guardOver(t, "package pkg\n\nfunc probe(n int) int {\n\treturn n\n}\n")
	if got := g.probeSite(nil, resultsOfProbe(t, g)); got != nil {
		t.Errorf("probeSite = %+v for no statement, want none", got)
	}
	var stmt *ast.ReturnStmt
	for node := range g.parent {
		ret, isReturn := node.(*ast.ReturnStmt)
		if isReturn && (stmt == nil || ret.Pos() > stmt.Pos()) {
			stmt = ret
		}
	}
	if got := g.probeSite(stmt, nil); got != nil {
		t.Errorf("probeSite = %+v for a function with no results, want none", got)
	}
}

// resultsOfProbe is the declared result tuple of the fixture's `probe`.
func resultsOfProbe(t *testing.T, g *guardResolver) *types.Tuple {
	t.Helper()

	for node := range g.parent {
		fn, isFunc := node.(*ast.FuncDecl)
		if !isFunc || fn.Name.Name != "probe" {
			continue
		}
		obj := g.info.Defs[fn.Name]
		if obj == nil {
			t.Fatal("the checker defined nothing for probe")
		}
		signature, isSignature := obj.Type().(*types.Signature)
		if !isSignature {
			t.Fatalf("probe has the type %s, want a signature", obj.Type())
		}
		return signature.Results()
	}
	t.Fatal("the fixture holds no function named probe")
	return nil
}

// TestWhichResultOfAProbedReturnCarriesTheHint is
// [guardResolver.probesResult], the per-result half of the same question.
//
// Both conditions leave the *other* results of the statement probed, which is
// the whole reason they are asked per result: the rewrite declares a temporary
// per result and writes an `if` per mutant, so dropping one mutant's `if` is a
// rewrite it already knows how to render.
func TestWhichResultOfAProbedReturnCarriesTheHint(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "an int",
			src:  "package pkg\n\nfunc probe(n int) int {\n\treturn n\n}\n",
			want: true,
		},
		{
			// `-0.0 != 0` is false while the two values are distinguishable, so
			// "did it differ" has an answer the comparison cannot give.
			name: "a float",
			src:  "package pkg\n\nfunc probe(f float64) float64 {\n\treturn f\n}\n",
		},
		{
			name: "a complex number",
			src:  "package pkg\n\nfunc probe(c complex128) complex128 {\n\treturn c\n}\n",
		},
		{
			// A panic is a divergence between the original and the mutant that
			// the comparison is never reached to see.
			name: "a dereference",
			src:  "package pkg\n\nfunc probe(p *int) int {\n\treturn *p\n}\n",
		},
		{
			name: "a string",
			src:  "package pkg\n\nfunc probe(s string) string {\n\treturn s\n}\n",
			want: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := guardOver(t, c.src)
			var last *ast.ReturnStmt
			for node := range g.parent {
				ret, isReturn := node.(*ast.ReturnStmt)
				if isReturn && len(ret.Results) > 0 && (last == nil || ret.Pos() > last.Pos()) {
					last = ret
				}
			}
			var value ast.Expr
			if last != nil {
				value = last.Results[0]
			}
			if value == nil {
				t.Fatalf("the fixture holds no returned value:\n%s", c.src)
			}
			declared := resultsOfProbe(t, g).At(0).Type()
			if got := g.probesResult(value, declared); got != c.want {
				t.Errorf("probesResult = %v, want %v", got, c.want)
			}
		})
	}
}
