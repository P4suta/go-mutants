// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"testing"
)

func declFixture(t *testing.T, decls, stmt string) *guardResolver {
	t.Helper()

	return guardOver(t, "package pkg\n\nfunc sum(xs ...int) int { return 0 }\n\n"+decls+"\n\nfunc probe(n int) {\n"+stmt+"\n}\n")
}

func firstOfKind(t *testing.T, g *guardResolver, want func(ast.Stmt) bool) ast.Stmt {
	t.Helper()

	var found ast.Stmt
	for node := range g.parent {
		stmt, isStmt := node.(ast.Stmt)
		if !isStmt || !want(stmt) {
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

func TestWhichDeclarationsFormDCanHoist(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name     string
		decls    string
		stmt     string
		form     GuardForm
		declared int
	}{
		{name: "a compound assignment", stmt: "\tn += 1", form: GuardFormS},
		{name: "a plain assignment", stmt: "\tn = 1", form: GuardFormS},
		{name: "a short declaration", stmt: "\tx := n + 1\n\t_ = x", form: GuardFormD, declared: 1},
		{name: "a var with an initialiser", stmt: "\tvar x = n + 1\n\t_ = x", form: GuardFormD, declared: 1},
		{name: "a var with a type and an initialiser", stmt: "\tvar x int = n + 1\n\t_ = x", form: GuardFormD, declared: 1},
		{
			name: "a short declaration of two names",
			stmt: "\tx, y := n+1, n+2\n\t_, _ = x, y", form: GuardFormD, declared: 2,
		},
		{
			name: "a short declaration with a blank",
			stmt: "\t_, y := n+1, n+2\n\t_ = y", form: GuardFormD, declared: 1,
		},
		{
			name: "a short declaration that redeclares",
			stmt: "\tfirst := n + 1\n\tfirst, second := n+2, n+3\n\t_, _ = first, second",
			form: GuardFormD, declared: 1,
		},
		{
			name:  "an initialiser that mentions the name being declared",
			decls: "var total = 1",
			stmt:  "\ttotal := total + n\n\t_ = total",
		},
		{
			name:  "a const declaration in a body",
			decls: "",
			stmt:  "\tconst limit = 2 + 2\n\t_ = limit",
		},
		{
			name: "a type declaration in a body",
			stmt: "\ttype pair struct{ a, b int }\n\t_ = pair{}",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := declFixture(t, c.decls, c.stmt)
			stmt := firstOfKind(t, g, func(s ast.Stmt) bool {
				switch s.(type) {
				case *ast.AssignStmt, *ast.DeclStmt:
					return true
				}
				return false
			})
			guard, ok := g.statementGuard(stmt)
			if ok != (c.form != "") {
				t.Fatalf("statementGuard = (%q, %v), want form %q", guard.Form, ok, c.form)
			}
			if !ok {
				return
			}
			if guard.Form != c.form {
				t.Errorf("statementGuard chose %q, want %q", guard.Form, c.form)
			}
			if len(guard.DeclTypes) != c.declared {
				t.Errorf("the site declares %v, want %d names", guard.DeclTypes, c.declared)
			}
			for _, decl := range guard.DeclTypes {
				if decl.Name == "" || decl.Type == "" {
					t.Errorf("a declaration is %+v, and both halves have to be writable", decl)
				}
			}
		})
	}
}

func TestADeclarationOfATypeThisFileCannotSpellIsRefused(t *testing.T) {
	t.Parallel()

	g := guardOver(t, "package pkg\n\ntype hidden struct{ n int }\n\n"+
		"func make2() *hidden { return nil }\n\n"+
		"func probe(n int) {\n\tc := make2()\n\t_ = c\n}\n")
	stmt := firstOfKind(t, g, func(s ast.Stmt) bool {
		assign, isAssign := s.(*ast.AssignStmt)
		return isAssign && assign.Tok == token.DEFINE
	})
	guard, ok := g.statementGuard(stmt)
	if !ok {
		t.Fatal("statementGuard refused a declaration of this package's own type")
	}
	if guard.Form != GuardFormD || len(guard.DeclTypes) != 1 {
		t.Fatalf("statementGuard = %q with %v", guard.Form, guard.DeclTypes)
	}
	if want := "*hidden"; guard.DeclTypes[0].Type != want {
		t.Errorf("the declared type is %q, want %q", guard.DeclTypes[0].Type, want)
	}
}

func TestAVarSpecWhoseCutWouldSwallowALineBreak(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		stmt string
		want bool
	}{
		{name: "a spec on one line", stmt: "\tvar x = n + 1\n\t_ = x", want: true},
		{name: "a spec with a type on one line", stmt: "\tvar x int = n + 1\n\t_ = x", want: true},
		{
			name: "a spec with no initialiser on one line",
			stmt: "\tvar x int\n\t_ = x", want: true,
		},
		{
			name: "a spec with no initialiser spread over lines",
			stmt: "\tvar x struct {\n\t\tn int\n\t}\n\t_ = x",
		},
		{
			name: "a spec whose type is spread over lines",
			stmt: "\tvar f func(\n\t\tv int,\n\t) int = nil\n\t_ = f",
		},
		{
			name: "a spec whose initialiser is spread over lines",
			stmt: "\tvar x int = sum(\n\t\t1,\n\t\t2,\n\t)\n\t_ = x",
			want: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := declFixture(t, "", c.stmt)
			var spec *ast.ValueSpec
			for node := range g.parent {
				if value, isValue := node.(*ast.ValueSpec); isValue && spec == nil {
					spec = value
				}
			}
			if spec == nil {
				t.Fatalf("the fixture holds no value specification:\n%s", c.stmt)
			}
			if got := g.cutIsLineFree(spec); got != c.want {
				t.Errorf("cutIsLineFree = %v, want %v", got, c.want)
			}
		})
	}

	g := declFixture(t, "", "\tvar x = n + 1\n\t_ = x")
	if g.sameLine(token.NoPos, token.NoPos) {
		t.Error("sameLine answered about two positions that are not in the file")
	}
	if g.sameLine(token.Pos(g.tokFile.Base()), token.NoPos) {
		t.Error("sameLine answered about an end that is not in the file")
	}
}

func TestADeclarationWhoseInitialiserNamesItself(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		stmt string
		want bool
	}{
		{
			name: "an initialiser naming the name it declares",
			stmt: "\ttotal := 1\n\t{\n\t\ttotal := total + n\n\t\t_ = total\n\t}\n\t_ = total",
			want: true,
		},
		{
			name: "one of two initialisers naming one of two names",
			stmt: "\ttotal := 1\n\t{\n\t\tother, total := n, total+1\n\t\t_, _ = other, total\n\t}\n\t_ = total",
			want: true,
		},
		{
			name: "an initialiser naming something else",
			stmt: "\ttotal := n + 1\n\t_ = total",
		},
		{
			name: "an initialiser naming a name declared earlier",
			stmt: "\tfirst := n\n\tsecond := first + 1\n\t_ = second",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := declFixture(t, "", c.stmt)
			var inner *ast.AssignStmt
			for node := range g.parent {
				assign, isAssign := node.(*ast.AssignStmt)
				if !isAssign || assign.Tok != token.DEFINE {
					continue
				}
				if inner == nil || assign.Pos() > inner.Pos() {
					inner = assign
				}
			}
			if inner == nil {
				t.Fatalf("the fixture holds no short declaration:\n%s", c.stmt)
			}
			names := map[string]bool{}
			for _, lhs := range inner.Lhs {
				if ident, isIdent := lhs.(*ast.Ident); isIdent {
					names[ident.Name] = true
				}
			}
			if got := g.rebindsOwnInitialiser(names, inner.Rhs); got != c.want {
				t.Errorf("rebindsOwnInitialiser(%v) = %v, want %v", names, got, c.want)
			}
		})
	}
}

func TestADeclaredNameIsTypedFromTheCheckersOwnRecord(t *testing.T) {
	t.Parallel()

	g := declFixture(t, "", "\tx := n + 1\n\t_ = x")
	var declared *ast.Ident
	for node := range g.parent {
		assign, isAssign := node.(*ast.AssignStmt)
		if !isAssign || assign.Tok != token.DEFINE {
			continue
		}
		declared = assign.Lhs[0].(*ast.Ident)
	}
	if declared == nil {
		t.Fatal("the fixture holds no short declaration")
	}

	decl, needs, ok := g.declTypeOf(declared)
	if !ok {
		t.Fatal("declTypeOf refused a name of an int")
	}
	if decl.Name != "x" || decl.Type != "int" {
		t.Errorf("declTypeOf = %+v, want x int", decl)
	}
	if len(needs) != 0 {
		t.Errorf("declaring an int needs the imports %v", needs)
	}

	if _, _, ok := g.declTypeOf(ast.NewIdent("stranger")); ok {
		t.Error("declTypeOf answered for an identifier the checker did not define")
	}
	blind := &guardResolver{}
	if _, _, ok := blind.declTypeOf(declared); ok {
		t.Error("declTypeOf answered without the checker's record")
	}
}

func TestTheFormsAreTriedInOrder(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		src  string
		find func(ast.Node) bool
		want GuardForm
	}{
		{
			name: "a universe bool takes Form C",
			src:  "package pkg\n\nfunc probe(a, b int) {\n\tif a < b {\n\t}\n}\n",
			find: binaryOp(token.LSS),
			want: GuardFormC,
		},
		{
			name: "a statement takes Form S",
			src:  "package pkg\n\nfunc probe(a, b int) {\n\tn := 0\n\tn = a * b\n\t_ = n\n}\n",
			find: binaryOp(token.MUL),
			want: GuardFormS,
		},
		{
			name: "a named boolean takes Form C'",
			src:  "package pkg\n\ntype flag bool\n\nfunc probe(f flag) {\n\tswitch f && f {\n\t}\n}\n",
			find: binaryOp(token.LAND),
			want: GuardFormCPrime,
		},
		{
			name: "a switch tag takes Form E",
			src:  "package pkg\n\nfunc probe(a, b int) {\n\tswitch a * b {\n\t}\n}\n",
			find: binaryOp(token.MUL),
			want: GuardFormE,
		},
		{
			name: "a for post statement takes Form F",
			src:  "package pkg\n\nfunc probe(n int) {\n\tfor i := 0; i < n; i = i + 2 {\n\t}\n}\n",
			find: func(node ast.Node) bool {
				assign, isAssign := node.(*ast.AssignStmt)
				return isAssign && assign.Tok == token.ASSIGN
			},
			want: GuardFormF,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := guardOver(t, c.src)
			var anchor ast.Node
			for node := range g.parent {
				if !c.find(node) {
					continue
				}
				if anchor == nil || node.Pos() < anchor.Pos() {
					anchor = node
				}
			}
			if anchor == nil {
				t.Fatalf("the fixture holds no anchor:\n%s", c.src)
			}
			guard, ok := g.guardFor(anchor)
			if !ok {
				t.Fatalf("guardFor found no form for:\n%s", c.src)
			}
			if guard.Form != c.want {
				t.Errorf("guardFor chose %q, want %q", guard.Form, c.want)
			}
			if guard.SiteSpan.EndByte <= guard.SiteSpan.StartByte {
				t.Errorf("the site spans %s, which covers nothing", guard.SiteSpan)
			}
		})
	}
}

func binaryOp(op token.Token) func(ast.Node) bool {
	return func(node ast.Node) bool {
		binary, isBinary := node.(*ast.BinaryExpr)
		return isBinary && binary.Op == op
	}
}

func TestWhatFormDRefusesToReadAtAll(t *testing.T) {
	t.Parallel()

	g := declFixture(t, "", "\tx := n + 1\n\t_ = x")

	t.Run("a short declaration whose left side is not a name", func(t *testing.T) {
		t.Parallel()

		assign := &ast.AssignStmt{
			Lhs: []ast.Expr{&ast.IndexExpr{X: ast.NewIdent("xs"), Index: ast.NewIdent("i")}},
			Tok: token.DEFINE,
			Rhs: []ast.Expr{ast.NewIdent("n")},
		}
		if _, _, ok := g.defineTypes(assign); ok {
			t.Error("defineTypes answered for a left side that declares nothing")
		}
	})

	t.Run("a short declaration of a name the checker did not define", func(t *testing.T) {
		t.Parallel()

		assign := &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent("stranger")},
			Tok: token.DEFINE,
			Rhs: []ast.Expr{ast.NewIdent("n")},
		}
		if _, _, ok := g.defineTypes(assign); ok {
			t.Error("defineTypes answered for a name with no type")
		}
	})

	t.Run("a declaration that is not a var", func(t *testing.T) {
		t.Parallel()

		for _, tok := range []token.Token{token.CONST, token.TYPE, token.IMPORT} {
			decl := &ast.DeclStmt{Decl: &ast.GenDecl{Tok: tok}}
			if _, _, ok := g.declTypes(decl); ok {
				t.Errorf("declTypes answered for a %s declaration", tok)
			}
		}
	})

	t.Run("a declaration that is not a general one", func(t *testing.T) {
		t.Parallel()

		decl := &ast.DeclStmt{Decl: &ast.FuncDecl{Name: ast.NewIdent("inner")}}
		if _, _, ok := g.declTypes(decl); ok {
			t.Error("declTypes answered for a declaration that declares no values")
		}
	})

	t.Run("a var block holding something that is not a value specification", func(t *testing.T) {
		t.Parallel()

		decl := &ast.DeclStmt{Decl: &ast.GenDecl{
			Tok:   token.VAR,
			Specs: []ast.Spec{&ast.ImportSpec{Name: ast.NewIdent("x")}},
		}}
		if _, _, ok := g.declTypes(decl); ok {
			t.Error("declTypes answered for a specification that declares no variable")
		}
	})

	t.Run("a var of a name the checker did not define", func(t *testing.T) {
		t.Parallel()

		decl := &ast.DeclStmt{Decl: &ast.GenDecl{
			Tok: token.VAR,
			Specs: []ast.Spec{&ast.ValueSpec{
				Names:  []*ast.Ident{ast.NewIdent("stranger")},
				Values: []ast.Expr{ast.NewIdent("n")},
			}},
		}}
		if _, _, ok := g.declTypes(decl); ok {
			t.Error("declTypes answered for a name with no type")
		}
	})

	t.Run("a declaration of nothing", func(t *testing.T) {
		t.Parallel()

		if g.rebindsOwnInitialiser(nil, nil) {
			t.Error("rebindsOwnInitialiser answered yes about a declaration of nothing")
		}
		if g.rebindsOwnInitialiser(map[string]bool{"x": true}, nil) {
			t.Error("rebindsOwnInitialiser answered yes about a declaration with no initialiser")
		}
		if g.rebindsOwnInitialiser(nil, []ast.Expr{ast.NewIdent("n")}) {
			t.Error("rebindsOwnInitialiser answered yes about an initialiser with nothing to rebind")
		}
	})

	t.Run("a composite literal the checker recorded nothing for", func(t *testing.T) {
		t.Parallel()

		literal := &ast.CompositeLit{Type: ast.NewIdent("wrapper")}
		blind := &guardResolver{}
		if blind.fieldKeyed(literal) {
			t.Error("fieldKeyed answered without the checker's record")
		}
		if g.fieldKeyed(literal) {
			t.Error("fieldKeyed answered for a literal the checker did not record")
		}
	})
}
