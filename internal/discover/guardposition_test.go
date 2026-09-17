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
		{name: "a map literal's value", body: "\t_ = map[int]bool{1: target}", want: true},
		{name: "an argument of a variadic call", body: "\ttakeAll(target, other)", want: true},
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

func positionFixture(body string) string {
	return "package pkg\n\n" +
		"type wrapper struct{ ok bool }\n\n" +
		"var wrapped wrapper\n\n" +
		"var other bool\n\n" +
		"func take(bool) {}\n\n" +
		"func takeAll(...bool) {}\n\n" +
		"func probe() {\n\tvar target bool\n\t_ = target\n" + body + "\n}\n"
}

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

		g := guardOver(t, "package pkg\n\nconst target = 2\n\nfunc probe() {\n\t_ = [4]int{target: 1}\n}\n")
		if g.wrappablePosition(namedTarget(t, g)) {
			t.Error("wrappablePosition = true for the index of an array literal, want false")
		}
	})
}

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

func TestABodyBlockIsNotAHeaderSlot(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		body string
	}{
		{name: "a for body", body: "\tfor n = 0; n < 3; n++ {\n\t}"},
		{name: "an if body", body: "\tif n > 0 {\n\t}"},
		{name: "a switch body", body: "\tswitch n {\n\t}"},
		{name: "a type switch body", body: "\tswitch v := any(nil).(type) {\n\tdefault:\n\t\t_ = v\n\t}"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := guardOver(t, "package pkg\n\nvar n, m int\n\nfunc probe() {\n"+c.body+"\n}\n")
			var body ast.Stmt
			for node := range g.parent {
				block, isBlock := node.(*ast.BlockStmt)
				if !isBlock {
					continue
				}
				if _, isFunc := g.parent[node].(*ast.FuncDecl); isFunc {
					continue
				}
				body = block
			}
			if body == nil {
				t.Fatal("the fixture holds no body block inside the statement")
			}
			if g.simpleStmtSlot(body) {
				t.Error("simpleStmtSlot = true for a body block, want false")
			}
			if !g.blockIsLegalFor(body) {
				t.Error("blockIsLegalFor = false for a body block, want true")
			}
		})
	}
}

func TestTheSearchForAStatementStopsAtTheFunctionAndAtTheFile(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "an edit inside a statement",
			src:  "package pkg\n\nfunc probe(a, b int) {\n\t_ = a * b\n}\n",
			want: true,
		},
		{
			name: "an edit in a function's signature",
			src:  "package pkg\n\nconst n = 2 * 2\n\nfunc probe(xs [2 * 2]int) {\n\t_ = xs\n}\n",
		},
		{
			name: "an edit in a package-level declaration",
			src:  "package pkg\n\nvar total = 2 * 2\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := guardOver(t, c.src)
			var anchor ast.Expr
			for node := range g.parent {
				binary, isBinary := node.(*ast.BinaryExpr)
				if !isBinary {
					continue
				}
				if anchor == nil || binary.Pos() < anchor.Pos() {
					anchor = binary
				}
			}
			if anchor == nil {
				t.Fatalf("the fixture holds no binary expression:\n%s", c.src)
			}
			guard, ok := g.statementSite(anchor)
			if ok != c.want {
				t.Fatalf("statementSite = (%+v, %v), want %v", guard, ok, c.want)
			}
			if !ok && guard.Form != "" {
				t.Errorf("a refusal carries the form %q, want none", guard.Form)
			}
			if ok && guard.Form == "" {
				t.Error("statementSite answered with a guard of no form")
			}
		})
	}
}

func TestThePositionsThatNeedMoreThanAValue(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		body string
		find func(ast.Stmt) ast.Expr
		want bool
	}{
		{
			name: "an operand of an increment",
			body: "\tn++",
			find: func(s ast.Stmt) ast.Expr {
				inc, ok := s.(*ast.IncDecStmt)
				if !ok {
					return nil
				}
				return inc.X
			},
		},
		{
			name: "a range clause's key",
			body: "\tfor n = range xs {\n\t}",
			find: func(s ast.Stmt) ast.Expr {
				rng, ok := s.(*ast.RangeStmt)
				if !ok {
					return nil
				}
				return rng.Key
			},
		},
		{
			name: "a range clause's value",
			body: "\tfor _, m = range xs {\n\t}",
			find: func(s ast.Stmt) ast.Expr {
				rng, ok := s.(*ast.RangeStmt)
				if !ok {
					return nil
				}
				return rng.Value
			},
		},
		{
			name: "the thing being ranged over",
			body: "\tfor range xs {\n\t}",
			find: func(s ast.Stmt) ast.Expr {
				rng, ok := s.(*ast.RangeStmt)
				if !ok {
					return nil
				}
				return rng.X
			},
			want: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := guardOver(t, "package pkg\n\nvar n, m int\n\nvar xs []int\n\nfunc probe() {\n"+c.body+"\n}\n")
			var target ast.Expr
			for node := range g.parent {
				stmt, isStmt := node.(ast.Stmt)
				if !isStmt {
					continue
				}
				if found := c.find(stmt); found != nil {
					target = found
				}
			}
			if target == nil {
				t.Fatalf("the fixture holds no expression of the kind the test asked for:\n%s", c.body)
			}
			if got := g.wrappablePosition(target); got != c.want {
				t.Errorf("wrappablePosition = %v, want %v", got, c.want)
			}
		})
	}
}

func TestWhichStatementsABlockMayBeWrappedAround(t *testing.T) {
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
		{name: "a return", stmt: &ast.ReturnStmt{}, want: true},
		{name: "a deferred call", stmt: &ast.DeferStmt{Call: &ast.CallExpr{Fun: ast.NewIdent("f")}}, want: true},
		{name: "a goroutine", stmt: &ast.GoStmt{Call: &ast.CallExpr{Fun: ast.NewIdent("f")}}, want: true},
		{name: "a break", stmt: &ast.BranchStmt{Tok: token.BREAK}, want: true},
		{name: "a continue", stmt: &ast.BranchStmt{Tok: token.CONTINUE}, want: true},
		{name: "a goto", stmt: &ast.BranchStmt{Tok: token.GOTO}, want: true},
		{name: "a fallthrough", stmt: &ast.BranchStmt{Tok: token.FALLTHROUGH}},
		{name: "a block", stmt: &ast.BlockStmt{}},
		{name: "an if", stmt: &ast.IfStmt{}},
		{name: "a declaration", stmt: &ast.DeclStmt{}},
		{name: "nothing at all", stmt: nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := FormSStatement(c.stmt); got != c.want {
				t.Errorf("FormSStatement(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

func TestANameASiblingFileBindsIsTakenToo(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	parse := func(name, src string) *ast.File {
		t.Helper()
		file, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		return file
	}
	rewritten := parse("scan.go", "package pkg\n\nfunc probe() int {\n\treturn 1\n}\n")
	sibling := parse("sibling.go", "package pkg\n\nvar carrier = 2\n\nfunc helper() {}\n")

	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("example.com/m/pkg", fset, []*ast.File{rewritten, sibling}, info)
	if err != nil {
		t.Fatalf("the fixture does not type-check: %v", err)
	}

	g := newGuardResolver(rewritten, info, pkg, fset.File(rewritten.Package), nil)
	for _, name := range []string{"carrier", "helper"} {
		if !g.taken[name] {
			t.Errorf("the index does not hold %q, which a sibling file binds in the package block", name)
		}
	}

	partial := newGuardResolver(rewritten, nil, nil, nil, nil)
	for _, name := range []string{"error", "true", "append", "recover"} {
		if partial.taken[name] {
			t.Errorf("without package information the index holds the universe name %q", name)
		}
	}
	if !partial.taken["probe"] {
		t.Error("without package information the index lost the names the file itself binds")
	}
}

func TestWhichImportsTheResolverCanReadAtAll(t *testing.T) {
	t.Parallel()

	file := &ast.File{
		Name: ast.NewIdent("pkg"),
		Imports: []*ast.ImportSpec{
			{Path: &ast.BasicLit{Kind: token.STRING, Value: `"time"`}},
			{Path: &ast.BasicLit{Kind: token.STRING, Value: "not a quoted string"}},
			{Path: &ast.BasicLit{Kind: token.STRING, Value: `""`}},
			{Path: nil},
		},
	}
	g := newGuardResolver(file, nil, nil, nil, nil)
	if _, indexed := g.imports["time"]; !indexed {
		t.Errorf("the index does not hold the one path that is one: %v", g.imports)
	}
	if len(g.imports) != 1 {
		t.Errorf("the index holds %v, want only the path that unquotes to something", g.imports)
	}
}
