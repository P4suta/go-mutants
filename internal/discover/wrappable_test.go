// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"testing"
)

// The three type gates the form staircase climbs, and the name index a
// completion has to dodge.
//
// Each gate asks a different question of the same expression -- is this a value
// at all, is it the universe bool, is it boolean underneath but not that -- and
// the order they are asked in is what keeps an existing candidate on the form
// it already uses. A gate that widened would move candidates between forms,
// which changes the bytes of the instrumented tree for no gain; one that
// narrowed would turn a site into a skip.

// TestTheThreeTypeGatesAskThreeDifferentQuestions is the staircase's type half,
// asked directly rather than through the form a site came out as.
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

	// A type expression is recorded by the checker and is not a value, which
	// is the one shape all three gates have to refuse for the same reason: a
	// form that wrapped it would be selecting between two types rather than
	// between two values.
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

	// And an expression the checker recorded nothing for, which is what a gate
	// meets when it is asked about a node from another file.
	t.Run("an expression with no entry", func(t *testing.T) {
		t.Parallel()

		stranger := ast.NewIdent("stranger")
		if g.wrappableValue(stranger) || g.wrappableBool(stranger) || g.wrappableNamedBool(stranger) {
			t.Error("a gate accepted an expression the checker recorded nothing for")
		}
	})

	// And with no record at all, which is the fail-closed path every gate
	// spells out for itself.
	t.Run("no record at all", func(t *testing.T) {
		t.Parallel()

		blind := &guardResolver{parent: g.parent}
		expr := rhs(t, "b")
		if blind.wrappableValue(expr) || blind.wrappableBool(expr) || blind.wrappableNamedBool(expr) {
			t.Error("a gate answered without the checker's record")
		}
	})
}

// TestAGateRefusesAValueInAPositionNoFormCanUse is the other half of each
// gate: a type it accepts in a slot it does not.
//
// The two halves are separate questions and both are load-bearing. An
// assignment target is a perfectly ordinary bool and a guard there would be an
// assignment to a parenthesised expression, which is not Go.
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

// TestEveryNameACompletionMayNotBindIsIndexed pins [guardResolver.indexTakenNames].
//
// Three scopes, and each of them is wrong in a different way if it is missed. A
// local variable sharing the name shadows the import for exactly the statements
// a guard sits in. An existing import binds a name no identifier node spells,
// because a plain `import "time"` writes `time` nowhere. And a name in the
// *package* block is not shadowing at all: Go forbids one name appearing in a
// file block and in the package block of the same package, so a `var carrier`
// in a sibling file makes `import carrier "…"` here a hard error.
func TestEveryNameACompletionMayNotBindIsIndexed(t *testing.T) {
	t.Parallel()

	g := guardOver(t, "package pkg\n\n"+
		"import (\n\t\"time\"\n\tclock \"sync\"\n)\n\n"+
		"var packageLevel = 1\n\n"+
		"func probe() {\n\tlocalName := time.Now()\n\t_ = localName\n\t_ = clock.Mutex{}\n}\n")

	for _, name := range []string{
		"localName",    // an identifier in the file
		"probe",        // one the file declares
		"time",         // the implicit name of a plain import, spelled by no node
		"clock",        // an aliased import's name
		"packageLevel", // the package block, which the checker has already built
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

	// And with no package information, which is what a scan over a file the
	// checker refused looks like: the two scopes that can still be read are
	// still read.
	partial := newGuardResolver(parseProbe(t, "package pkg\n\nimport \"time\"\n\nfunc probe() {\n\tlocal := 1\n\t_ = local\n}\n"),
		nil, nil, nil, nil)
	for _, name := range []string{"local", "time", "probe"} {
		if !partial.taken[name] {
			t.Errorf("without package information the index does not hold %q", name)
		}
	}
}

// TestTheParentIndexIsCompleteAndCorrect pins [guardResolver.indexParents],
// which every outward walk in this file reads.
//
// The index is built with a stack that pushes on each node and pops on the nil
// visit that closes it, and that is the only shape that stays balanced:
// ast.Inspect skips the closing visit for a node whose callback returned false,
// so a walk that pruned anywhere would leave the stack short and give every node
// after it the wrong parent. A wrong parent is not a wrong verdict -- it is a
// guard written around the wrong bytes.
func TestTheParentIndexIsCompleteAndCorrect(t *testing.T) {
	t.Parallel()

	const src = "package pkg\n\n" +
		"func probe(a, b int) int {\n" +
		"\tif a < b {\n\t\treturn a * b\n\t}\n" +
		"\tfor i := 0; i < b; i++ {\n\t\ta += i\n\t}\n" +
		"\treturn func() int { return a - b }()\n" +
		"}\n"

	g := guardOver(t, src)

	// Every node but the file itself has a parent, and every parent really
	// encloses its child. Walking the tree a second way -- ast.Inspect's own
	// order -- is what makes this a check rather than a restatement.
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

	// And the chain from the deepest leaf reaches the file, which is what
	// every outward walk in this package relies on to terminate.
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

// deepestLeaf is the last identifier of the fixture in source order, which the
// fixtures here put inside a function literal inside a function body.
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

// TestWhichImportsSupplyANameATypeCanBeWrittenWith pins
// [guardResolver.indexImports].
//
// Three import forms supply no name a type can be written with, and the
// difference between them matters to the answer rather than to the wording: a
// blank import binds nothing, a dot import binds the package's *contents*
// rather than the package, and an aliased one binds the alias. A path imported
// twice keeps the first usable spelling, so that the answer is the file's own
// source order rather than a map iteration.
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
		"time": "",      // no alias: the caller resolves it to the package's own name
		"sync": "clock", // an alias, which is the name to write
		"os":   "first", // the first usable spelling, in source order
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
