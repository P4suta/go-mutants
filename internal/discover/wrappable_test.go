// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"testing"
)

func TestTheThreeTypeGatesAskThreeDifferentQuestions(t *testing.T) {
	t.Parallel()

	const src = "package pkg\n\n" +
		"type flag bool\n\n" +
		"var b bool\n\n" +
		"var f flag\n\n" +
		"var n int\n\n" +
		"func probe() {\n" +
		"\t_ = b\n\t_ = f\n\t_ = n\n\t_ = int64(n)\n" +
		"}\n"

	g := guardOver(t, src)
	rhs := func(t *testing.T, want string) ast.Expr {
		t.Helper()
		for node := range g.parent {
			assign, isAssign := node.(*ast.AssignStmt)
			if !isAssign || len(assign.Rhs) != 1 {
				continue
			}
			if ident, isIdent := assign.Rhs[0].(*ast.Ident); isIdent && ident.Name == want {
				return ident
			}
		}
		t.Fatalf("the fixture has no `_ = %s`", want)
		return nil
	}

	for _, c := range []struct {
		name                           string
		expr                           ast.Expr
		value, universeBool, namedBool bool
	}{
		{name: "a universe bool", expr: rhs(t, "b"), value: true, universeBool: true},
		{name: "a named boolean type", expr: rhs(t, "f"), value: true, namedBool: true},
		{name: "an int", expr: rhs(t, "n"), value: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := g.wrappableValue(c.expr); got != c.value {
				t.Errorf("wrappableValue = %v, want %v", got, c.value)
			}
			if got := g.wrappableBool(c.expr); got != c.universeBool {
				t.Errorf("wrappableBool = %v, want %v", got, c.universeBool)
			}
			if got := g.wrappableNamedBool(c.expr); got != c.namedBool {
				t.Errorf("wrappableNamedBool = %v, want %v", got, c.namedBool)
			}
		})
	}

	t.Run("a type expression", func(t *testing.T) {
		t.Parallel()

		var target ast.Expr
		for node := range g.parent {
			if call, isCall := node.(*ast.CallExpr); isCall {
				target = call.Fun
			}
		}
		if target == nil {
			t.Fatal("the fixture holds no conversion")
		}
		if g.wrappableValue(target) {
			t.Error("wrappableValue accepted a type expression")
		}
		if g.wrappableBool(target) || g.wrappableNamedBool(target) {
			t.Error("a boolean gate accepted a type expression")
		}
	})

	t.Run("an expression with no entry", func(t *testing.T) {
		t.Parallel()

		stranger := ast.NewIdent("stranger")
		if g.wrappableValue(stranger) || g.wrappableBool(stranger) || g.wrappableNamedBool(stranger) {
			t.Error("a gate accepted an expression the checker recorded nothing for")
		}
	})

	t.Run("no record at all", func(t *testing.T) {
		t.Parallel()

		blind := &guardResolver{parent: g.parent}
		expr := rhs(t, "b")
		if blind.wrappableValue(expr) || blind.wrappableBool(expr) || blind.wrappableNamedBool(expr) {
			t.Error("a gate answered without the checker's record")
		}
	})
}

func TestAGateRefusesAValueInAPositionNoFormCanUse(t *testing.T) {
	t.Parallel()

	g := guardOver(t, "package pkg\n\ntype flag bool\n\nvar target bool\n\nvar named flag\n\n"+
		"func probe() {\n\ttarget = true\n\tnamed = false\n}\n")

	var boolTarget, namedTarget ast.Expr
	for node := range g.parent {
		ident, isIdent := node.(*ast.Ident)
		if !isIdent {
			continue
		}
		assign, isAssign := g.parent[node].(*ast.AssignStmt)
		if !isAssign || len(assign.Lhs) != 1 || assign.Lhs[0] != ast.Expr(ident) {
			continue
		}
		switch ident.Name {
		case "target":
			boolTarget = ident
		case "named":
			namedTarget = ident
		}
	}
	if boolTarget == nil || namedTarget == nil {
		t.Fatal("the fixture holds no assignment targets of both kinds")
	}

	if g.wrappableValue(boolTarget) {
		t.Error("wrappableValue accepted an assignment target")
	}
	if g.wrappableBool(boolTarget) {
		t.Error("wrappableBool accepted an assignment target")
	}
	if g.wrappableNamedBool(namedTarget) {
		t.Error("wrappableNamedBool accepted an assignment target")
	}
}

func TestEveryNameACompletionMayNotBindIsIndexed(t *testing.T) {
	t.Parallel()

	g := guardOver(t, "package pkg\n\n"+
		"import (\n\t\"time\"\n\tclock \"sync\"\n)\n\n"+
		"var packageLevel = 1\n\n"+
		"func probe() {\n\tlocalName := time.Now()\n\t_ = localName\n\t_ = clock.Mutex{}\n}\n")

	for _, name := range []string{
		"localName",
		"probe",
		"time",
		"clock",
		"packageLevel",
	} {
		if !g.taken[name] {
			t.Errorf("the index does not hold %q, which a completion must not bind", name)
		}
	}
	if g.taken["sync"] {
		t.Error("the index holds \"sync\", which the alias replaced and nothing binds")
	}
	if g.taken["carrier"] {
		t.Error("the index holds a name nothing in the file binds")
	}

	partial := newGuardResolver(parseProbe(t, "package pkg\n\nimport \"time\"\n\nfunc probe() {\n\tlocal := 1\n\t_ = local\n}\n"),
		nil, nil, nil, nil)
	for _, name := range []string{"local", "time", "probe"} {
		if !partial.taken[name] {
			t.Errorf("without package information the index does not hold %q", name)
		}
	}
}

func TestTheParentIndexIsCompleteAndCorrect(t *testing.T) {
	t.Parallel()

	const src = "package pkg\n\n" +
		"func probe(a, b int) int {\n" +
		"\tif a < b {\n\t\treturn a * b\n\t}\n" +
		"\tfor i := 0; i < b; i++ {\n\t\ta += i\n\t}\n" +
		"\treturn func() int { return a - b }()\n" +
		"}\n"

	g := guardOver(t, src)

	var counted int
	for node, parent := range g.parent {
		counted++
		if parent == nil {
			t.Fatalf("%T at %d has a nil parent", node, node.Pos())
		}
		if node.Pos() < parent.Pos() || node.End() > parent.End() {
			t.Errorf("%T [%d,%d) is recorded under %T [%d,%d), which does not contain it",
				node, node.Pos(), node.End(), parent, parent.Pos(), parent.End())
		}
	}
	if counted == 0 {
		t.Fatal("the parent index is empty")
	}

	depth, reachedFile := 0, false
	for node := ast.Node(deepestLeaf(t, g)); node != nil; node = g.parent[node] {
		depth++
		if _, isFile := node.(*ast.File); isFile {
			reachedFile = true
		}
		if depth > counted+1 {
			t.Fatal("the walk outward from a leaf does not terminate")
		}
	}
	if !reachedFile {
		t.Errorf("the walk outward visited %d nodes without reaching the file", depth)
	}
}

func deepestLeaf(t *testing.T, g *guardResolver) ast.Node {
	t.Helper()

	var found ast.Node
	for node := range g.parent {
		ident, isIdent := node.(*ast.Ident)
		if !isIdent {
			continue
		}
		if found == nil || ident.Pos() > found.Pos() {
			found = ident
		}
	}
	if found == nil {
		t.Fatal("the fixture holds no identifier")
	}
	return found
}

func TestWhichImportsSupplyANameATypeCanBeWrittenWith(t *testing.T) {
	t.Parallel()

	g := newGuardResolver(parseProbe(t, "package pkg\n\n"+
		"import (\n"+
		"\t\"time\"\n"+
		"\tclock \"sync\"\n"+
		"\t_ \"embed\"\n"+
		"\t. \"strings\"\n"+
		"\tfirst \"os\"\n"+
		"\tsecond \"os\"\n"+
		")\n"), nil, nil, nil, nil)

	for path, want := range map[string]string{
		"time": "",
		"sync": "clock",
		"os":   "first",
	} {
		got, indexed := g.imports[path]
		if !indexed {
			t.Errorf("the index does not hold %q", path)
			continue
		}
		if got != want {
			t.Errorf("the index holds %q for %q, want %q", got, path, want)
		}
	}
	for _, path := range []string{"embed", "strings"} {
		if local, indexed := g.imports[path]; indexed {
			t.Errorf("the index holds %q for %q, which binds no name a type can use", local, path)
		}
	}
	if len(g.imports) != 3 {
		t.Errorf("the index holds %v, want exactly the three paths that bind a name", g.imports)
	}
}
