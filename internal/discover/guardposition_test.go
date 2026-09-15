// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// Where a guard may stand, decided by the parent rather than by the node.
//
// Every form this file chooses between rewrites an expression or a statement
// into something of the same shape, and whether that is legal Go is a fact
// about the slot rather than about what is in it. `x` is an ordinary expression
// in `_ = x` and an addressable operand in `x++`; a call is a value in
// `n := f()` and a statement in `defer f()`. A hint the instrumenter cannot use
// is worse than no candidate -- the rewrite fails at compile time, in a
// generated tree, with a message about a program nobody wrote -- so each of
// these refusals is what keeps a candidate from being proposed at all.

// guardOver type-checks a whole file and returns the resolver over it.
func guardOver(t *testing.T, src string) *guardResolver {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "guard.go", src, 0)
	if err != nil {
		t.Fatalf("parsing:\n%s\n%v", src, err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("example.com/m/pkg", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("the fixture does not type-check:\n%s\n%v", src, err)
	}
	return newGuardResolver(file, info, pkg, fset.File(file.Package), nil)
}

// namedTarget is the identifier every position fixture puts in the slot under
// test. Naming it rather than counting nodes is what keeps a fixture readable
// and a failure legible.
func namedTarget(t *testing.T, g *guardResolver) ast.Expr {
	t.Helper()

	var found *ast.Ident
	for node := range g.parent {
		ident, isIdent := node.(*ast.Ident)
		if !isIdent || ident.Name != "target" {
			continue
		}
		if found == nil || ident.Pos() > found.Pos() {
			found = ident
		}
	}
	if found == nil {
		t.Fatal("the fixture holds no identifier named target")
	}
	return found
}

// TestWhereABooleanSelectorMayStand is [guardResolver.wrappablePosition], one
// row per slot.
//
// Form C renders `(M && (MUT) || !M && (ORIG))`, which is a value and nothing
// else. So every slot that needs more than a value refuses it -- an address, an
// assignment target, an operand of `++`, a field name, a struct literal's key --
// and so does every slot that holds no value at all, which is the three
// statement positions a call can appear in.
func TestWhereABooleanSelectorMayStand(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		body string
		want bool
	}{
		{name: "the right-hand side of an assignment", body: "\t_ = target", want: true},
		{name: "an assignment target", body: "\ttarget = true"},
		{name: "one of several assignment targets", body: "\ttarget, other = true, true"},
		{name: "the value of a multiple assignment", body: "\tother, _ = true, target", want: true},
		{name: "an operand of a comparison", body: "\t_ = target == true", want: true},
		{name: "the base of a selection", body: "\t_ = wrapped.ok\n\t_ = target", want: true},
		{name: "an operand of an address", body: "\t_ = &target"},
		{name: "an operand of a negation", body: "\t_ = !target", want: true},
		{name: "an argument", body: "\ttake(target)", want: true},
		{name: "the key of a map literal", body: "\t_ = map[bool]int{target: 1}", want: true},
		{name: "the value of a struct literal's field", body: "\t_ = wrapper{ok: target}", want: true},
		{name: "a condition", body: "\tif target {\n\t}", want: true},
		{name: "a returned value", body: "\t_ = func() bool { return target }()", want: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := guardOver(t, positionFixture(c.body))
			if got := g.wrappablePosition(namedTarget(t, g)); got != c.want {
				t.Errorf("wrappablePosition = %v, want %v", got, c.want)
			}
		})
	}
}

// positionFixture wraps a body in the declarations every row draws on.
func positionFixture(body string) string {
	return "package pkg\n\n" +
		"type wrapper struct{ ok bool }\n\n" +
		"var wrapped wrapper\n\n" +
		"var other bool\n\n" +
		"func take(bool) {}\n\n" +
		"func probe() {\n\tvar target bool\n\t_ = target\n" + body + "\n}\n"
}

// TestWhereABooleanSelectorMayNotStandBecauseTheSlotHoldsNoValue is the second
// kind of refusal, which needs a fixture of its own: the target is a call
// rather than a name, because a bare `bool` is not a statement.
func TestWhereABooleanSelectorMayNotStandBecauseTheSlotHoldsNoValue(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		body string
	}{
		{name: "a call on its own", body: "\ttarget()"},
		{name: "a deferred call", body: "\tdefer target()"},
		{name: "a call started as a goroutine", body: "\tgo target()"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := guardOver(t, "package pkg\n\nfunc target() bool { return true }\n\nfunc probe() {\n"+c.body+"\n}\n")
			call, isCall := g.parent[namedTarget(t, g)].(*ast.CallExpr)
			if !isCall {
				t.Fatalf("the fixture's target has the parent %T, want a call", g.parent[namedTarget(t, g)])
			}
			if g.wrappablePosition(call) {
				t.Error("wrappablePosition = true for a call in a statement slot, want false")
			}
		})
	}
}

// TestAFieldNameIsNotAnExpression is the pair of refusals that look like
// ordinary expressions and are not: the name after the dot, and the key of a
// struct literal.
//
// Both are identifiers the parser records in expression position, and neither
// denotes a value. A guard in either would select from a boolean or name a
// field the struct does not have.
func TestAFieldNameIsNotAnExpression(t *testing.T) {
	t.Parallel()

	t.Run("the name after the dot", func(t *testing.T) {
		t.Parallel()

		g := guardOver(t, "package pkg\n\ntype wrapper struct{ target bool }\n\n"+
			"var wrapped wrapper\n\nfunc probe() {\n\t_ = wrapped.target\n}\n")
		if g.wrappablePosition(namedTarget(t, g)) {
			t.Error("wrappablePosition = true for the name of a field, want false")
		}
	})

	t.Run("the key of a struct literal", func(t *testing.T) {
		t.Parallel()

		g := guardOver(t, "package pkg\n\ntype wrapper struct{ target bool }\n\n"+
			"func probe() {\n\t_ = wrapper{target: true}\n}\n")
		if g.wrappablePosition(namedTarget(t, g)) {
			t.Error("wrappablePosition = true for the key of a struct literal, want false")
		}
	})

	t.Run("the key of an array literal", func(t *testing.T) {
		t.Parallel()

		// An index rather than a field name, and refused for the same reason:
		// it is a constant the compiler reads as a position, not a value the
		// program evaluates.
		g := guardOver(t, "package pkg\n\nconst target = 2\n\nfunc probe() {\n\t_ = [4]int{target: 1}\n}\n")
		if g.wrappablePosition(namedTarget(t, g)) {
			t.Error("wrappablePosition = true for the index of an array literal, want false")
		}
	})
}

// TestWhichSlotsHoldASimpleStatementAndWhichHoldABlock is the pair of
// statement-slot predicates, which decide between Form S and Form F.
//
// They are not each other's negation and the difference is the whole point: a
// `for` post slot holds a simple statement, so a closure call is legal there
// and a block is not; a `select` communication clause holds neither. Asking one
// question and inverting it would put a block in a `for` header.
func TestWhichSlotsHoldASimpleStatementAndWhichHoldABlock(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name   string
		body   string
		find   func(ast.Node) bool
		simple bool
		block  bool
	}{
		{
			name: "a statement in a for body",
			body: "\tfor i := 0; i < 3; i++ {\n\t\tn = i\n\t}",
			find: assignTo("n"), block: true,
		},
		{
			name:   "a for initialiser",
			body:   "\tfor n = 0; n < 3; n++ {\n\t}",
			find:   assignTo("n"),
			simple: true,
		},
		{
			// A different name from the initialiser's, so that the row names
			// the post slot and not the first assignment it happens to find.
			name:   "a for post statement",
			body:   "\tfor n = 0; n < 3; m = m + 1 {\n\t}",
			find:   assignTo("m"),
			simple: true,
		},
		{
			name:   "an if initialiser",
			body:   "\tif n = 1; n > 0 {\n\t}",
			find:   assignTo("n"),
			simple: true,
		},
		{
			name:   "a switch initialiser",
			body:   "\tswitch n = 1; n {\n\t}",
			find:   assignTo("n"),
			simple: true,
		},
		{
			name:   "a type switch initialiser",
			body:   "\tswitch n = 1; v := any(nil).(type) {\n\tdefault:\n\t\t_ = v\n\t}",
			find:   assignTo("n"),
			simple: true,
		},
		{
			name:  "a statement in an if body",
			body:  "\tif n > 0 {\n\t\tn = 2\n\t}",
			find:  assignTo("n"),
			block: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := guardOver(t, "package pkg\n\nvar n, m int\n\nfunc probe() {\n"+c.body+"\n}\n")
			stmt := findStmt(t, g, c.find)
			if got := g.simpleStmtSlot(stmt); got != c.simple {
				t.Errorf("simpleStmtSlot = %v, want %v", got, c.simple)
			}
			if got := g.blockIsLegalFor(stmt); got != c.block {
				t.Errorf("blockIsLegalFor = %v, want %v", got, c.block)
			}
		})
	}
}

// assignTo finds the assignment to a name that sits directly in a header slot.
func assignTo(name string) func(ast.Node) bool {
	return func(node ast.Node) bool {
		assign, isAssign := node.(*ast.AssignStmt)
		if !isAssign || len(assign.Lhs) != 1 {
			return false
		}
		ident, isIdent := assign.Lhs[0].(*ast.Ident)
		return isIdent && ident.Name == name
	}
}

// findStmt returns the first statement of the fixture the predicate accepts.
func findStmt(t *testing.T, g *guardResolver, want func(ast.Node) bool) ast.Stmt {
	t.Helper()

	var found ast.Stmt
	for node := range g.parent {
		stmt, isStmt := node.(ast.Stmt)
		if !isStmt || !want(node) {
			continue
		}
		if found == nil || stmt.Pos() < found.Pos() {
			found = stmt
		}
	}
	if found == nil {
		t.Fatal("the fixture holds no statement of the kind the test asked for")
	}
	return found
}

// TestACommunicationClauseHoldsNeitherAStatementNorABlock is the one slot both
// predicates refuse, and it is refused for a reason neither of them shares with
// the headers: a `select` case must be a send or a receive, so a block is not
// Go there and neither is a call.
func TestACommunicationClauseHoldsNeitherAStatementNorABlock(t *testing.T) {
	t.Parallel()

	g := guardOver(t, "package pkg\n\nvar ch chan int\n\nvar n int\n\n"+
		"func probe() {\n\tselect {\n\tcase n = <-ch:\n\t}\n}\n")
	stmt := findStmt(t, g, assignTo("n"))
	if g.simpleStmtSlot(stmt) {
		t.Error("simpleStmtSlot = true for a communication clause, want false")
	}
	if g.blockIsLegalFor(stmt) {
		t.Error("blockIsLegalFor = true for a communication clause, want false")
	}
}

// TestWhichStatementsAClosureMayHold is [FormFStatement], which is
// [FormSStatement]'s list minus the three that change meaning inside a closure
// and the one that could not reach the slot anyway.
func TestWhichStatementsAClosureMayHold(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		stmt ast.Stmt
		want bool
	}{
		{name: "a call", stmt: &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("f")}}, want: true},
		{name: "a send", stmt: &ast.SendStmt{Chan: ast.NewIdent("ch"), Value: ast.NewIdent("n")}, want: true},
		{name: "an increment", stmt: &ast.IncDecStmt{X: ast.NewIdent("n"), Tok: token.INC}, want: true},
		{
			name: "an assignment",
			stmt: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent("n")}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent("m")}},
			want: true,
		},
		{
			name: "a short variable declaration",
			stmt: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent("n")}, Tok: token.DEFINE, Rhs: []ast.Expr{ast.NewIdent("m")}},
		},
		{name: "a return", stmt: &ast.ReturnStmt{}},
		{name: "a deferred call", stmt: &ast.DeferStmt{Call: &ast.CallExpr{Fun: ast.NewIdent("f")}}},
		{name: "a goroutine", stmt: &ast.GoStmt{Call: &ast.CallExpr{Fun: ast.NewIdent("f")}}},
		{name: "a break", stmt: &ast.BranchStmt{Tok: token.BREAK}},
		{name: "a block", stmt: &ast.BlockStmt{}},
		{name: "nothing at all", stmt: nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := FormFStatement(c.stmt); got != c.want {
				t.Errorf("FormFStatement(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}
