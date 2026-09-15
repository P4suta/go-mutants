// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"testing"
)

// What Form D has to decide before it can hoist a declaration out of the way.
//
// Form D rewrites `x := f(a + b)` as `var x T; if __gm.M[3] { x = f(a - b) }
// else { x = f(a + b) }`, which means writing the declared type out and
// deleting the tokens that made the statement a declaration. Three things can
// stop it, and each of them is a whole class of ordinary Go: a type this file
// cannot spell, an initialiser that mentions a name the same statement
// declares, and a deletion that would take a line break with it.
//
// A wrong answer here is not a wrong verdict, it is a generated tree that does
// not compile -- a run-ending internal error over gofmt-clean source. So each
// refusal is stated, and so is each acceptance: refusing too much turns a
// mutable site into an `unnameable-decl-type` skip nobody can act on.

// declFixture is a probe fixture built around one statement inside a function.
func declFixture(t *testing.T, decls, stmt string) *guardResolver {
	t.Helper()

	return guardOver(t, "package pkg\n\n"+decls+"\n\nfunc probe(n int) {\n"+stmt+"\n}\n")
}

// firstOfKind finds the first statement of the fixture the predicate accepts.
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

// TestWhichDeclarationsFormDCanHoist is [guardResolver.statementGuard]'s
// division, one shape at a time.
//
// The division is exactly "does this statement declare anything". Form S buries
// its site in a block, so a statement that declares a name would take that name
// out of scope for everything after it; Form D exists to hoist those
// declarations back out. A compound assignment declares nothing and is Form S;
// `x := 1` and `var x = 1` declare and are Form D.
func TestWhichDeclarationsFormDCanHoist(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name  string
		decls string
		stmt  string
		// form is the guard form the statement gets, or empty for a refusal.
		form GuardForm
		// declared is how many names a Form D site has to hoist.
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
			// The blank is not a name anything can be hoisted for, and a Form D
			// site that tried would emit `var _ int`, which is not legal Go.
			name: "a short declaration with a blank",
			stmt: "\t_, y := n+1, n+2\n\t_ = y", form: GuardFormD, declared: 1,
		},
		{
			// A `:=` that re-declares a name declared earlier in the same block
			// assigns to it rather than declaring it, so there is nothing to
			// hoist for that half.
			name: "a short declaration that redeclares",
			stmt: "\tfirst := n + 1\n\tfirst, second := n+2, n+3\n\t_, _ = first, second",
			form: GuardFormD, declared: 1,
		},
		{
			// The scope of a name declared here begins at the *end* of the
			// specification, so `total` in the initialiser is the outer one --
			// and Form D's hoisted `var total int` would come *before* the
			// initialiser and shadow it. A different program.
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

// TestADeclarationOfATypeThisFileCannotSpellIsRefused is the other half of Form
// D, and the one the reserved skip reason was named for.
//
// Form D writes the declared type out, so a value whose type has no source form
// in this file has no Form D site -- and the search falls through to Form E,
// which writes the *initialiser's* type instead. That is why the refusal is
// worth so much less than it used to be: the declaration is unspellable and the
// expression inside it usually is not.
func TestADeclarationOfATypeThisFileCannotSpellIsRefused(t *testing.T) {
	t.Parallel()

	g := guardOver(t, "package pkg\n\ntype hidden struct{ n int }\n\n"+
		"func make2() *hidden { return nil }\n\n"+
		"func probe(n int) {\n\tc := make2()\n\t_ = c\n}\n")
	stmt := firstOfKind(t, g, func(s ast.Stmt) bool {
		assign, isAssign := s.(*ast.AssignStmt)
		return isAssign && assign.Tok == token.DEFINE
	})
	// This package's own unexported type is perfectly spellable here, which is
	// the control: the refusal is about the *file*, not about export.
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

// TestAVarSpecWhoseCutWouldSwallowALineBreak pins
// [guardResolver.cutIsLineFree], which is the one refusal about *bytes* rather
// than about types or scopes.
//
// Form D deletes the tokens that make a statement a declaration, in place, and
// every remaining byte has to stay on the line the user put it on. Two of those
// deletions are as long as the source says they are: a spec with no initialiser
// goes whole, and a spec that spells its type out loses the type. A line break
// inside either cannot be padded back -- writing newlines into the replacement
// puts them where the tokens were, and the scanner inserts a semicolon.
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

	// And two positions that are not positions, which is the fail-closed
	// direction: a cut whose extent is unknown is one this rewrite may not
	// make.
	g := declFixture(t, "", "\tvar x = n + 1\n\t_ = x")
	if g.sameLine(token.NoPos, token.NoPos) {
		t.Error("sameLine answered about two positions that are not in the file")
	}
	if g.sameLine(token.Pos(g.tokFile.Base()), token.NoPos) {
		t.Error("sameLine answered about an end that is not in the file")
	}
}

// TestADeclarationWhoseInitialiserNamesItself pins
// [guardResolver.rebindsOwnInitialiser], and the reason it is asked about the
// whole left-hand side at once.
//
// Go's rule is that the scope of a name declared by a short variable
// declaration begins at the *end* of the specification, so `total := total * 2`
// reads the outer `total` and declares a new one. Form D hoists the declaration
// in front of the initialiser, which would make the inner name shadow the outer
// one there -- a different program. One name of several is enough to spoil the
// statement, which is why the question is about the statement rather than about
// each name.
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

// TestADeclaredNameIsTypedFromTheCheckersOwnRecord pins
// [guardResolver.declTypeOf], whose refusals are the two ways there is nothing
// to hoist.
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

	// An identifier the checker defined nothing for: a name that redeclares
	// something already in scope is an assignment target rather than a
	// declaration, and there is nothing for Form D to hoist.
	if _, _, ok := g.declTypeOf(ast.NewIdent("stranger")); ok {
		t.Error("declTypeOf answered for an identifier the checker did not define")
	}
	blind := &guardResolver{}
	if _, _, ok := blind.declTypeOf(declared); ok {
		t.Error("declTypeOf answered without the checker's record")
	}
}

// TestTheFormsAreTriedInOrder pins [guardResolver.chooseForm]'s staircase,
// which is what keeps an existing candidate on the form it already uses.
//
// Each form is tried after the ones before it, so a site an earlier form covers
// is covered by exactly that form. Moving a candidate between forms changes the
// bytes of the instrumented tree for no gain at all, and the order is the only
// thing that stops it.
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
			// A named boolean with no statement around it: the tag of a
			// switch is an expression whose statement is the switch itself,
			// which no statement form covers, and its type is boolean
			// underneath without being the universe bool.
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

// binaryOp finds a binary expression with one operator.
func binaryOp(op token.Token) func(ast.Node) bool {
	return func(node ast.Node) bool {
		binary, isBinary := node.(*ast.BinaryExpr)
		return isBinary && binary.Op == op
	}
}
