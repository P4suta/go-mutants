// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/instrument"
)

// Which statements Form S can wrap is one fact held in two places, and until
// this test there was no compile-time or run-time connection between them.
// internal/discover decides it, because it is the phase with a type checker and
// it is what writes the hint; internal/instrument decides it again, because a
// hint is not something to trust -- a statement this package cannot wrap has to
// be refused here rather than spliced wrongly.
//
// The second check is the point, and it is also the hazard. Widen discovery's
// list alone and every hint it newly emits is refused downstream as a site
// conflict, which fails a run rather than losing a mutant; widen the
// instrumenter's alone and it silently accepts a shape discovery will never
// send, which is dead code wearing the look of a feature. Neither shows up in
// any existing test, because the fixtures only carry statements both lists
// already agree about.

// everyStatementKind is one source line per statement Go has, with the shape
// that makes it that statement and nothing more.
//
// It is written as source rather than as hand-built AST nodes so that the table
// is checkable by reading it: `go/parser` decides what each line is, and a line
// that stopped being the statement its name claims would be a parse this test
// can print rather than a node somebody assembled wrongly.
var everyStatementKind = map[string]string{
	"an expression statement":   `f()`,
	"a return":                  `return`,
	"an increment":              `i++`,
	"a decrement":               `i--`,
	"a send":                    `ch <- 1`,
	"a defer":                   `defer f()`,
	"a go":                      `go f()`,
	"a plain assignment":        `x = 1`,
	"a compound assignment":     `x += 1`,
	"a short declaration":       `x := 1`,
	"a var declaration":         `var x = 1`,
	"a const declaration":       `const x = 1`,
	"a type declaration":        `type T int`,
	"a block":                   `{ f() }`,
	"an if":                     `if x { f() }`,
	"a for":                     `for { f() }`,
	"a range":                   `for range xs { f() }`,
	"a switch":                  `switch { default: f() }`,
	"a type switch":             `switch x.(type) { default: f() }`,
	"a select":                  `select { default: f() }`,
	"a labelled statement":      `L: f()`,
	"a break":                   `break`,
	"a continue":                `continue`,
	"a labelled break":          `break L`,
	"a labelled continue":       `continue L`,
	"a goto":                    `goto L`,
	"a fallthrough":             `fallthrough`,
	"an empty statement":        `;`,
	"a bare declaration of two": `var x, y = 1, 2`,
}

// TestBothPhasesAgreeOnWhatFormSCanWrap is the whole subject of this file.
//
// A disagreement in either direction is a bug, and the message says which
// direction it is, because the two fail completely differently: discovery ahead
// of the instrumenter fails runs loudly, and the instrumenter ahead of
// discovery is silent.
func TestBothPhasesAgreeOnWhatFormSCanWrap(t *testing.T) {
	t.Parallel()

	for name, source := range everyStatementKind {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			stmt := parseStatement(t, source)
			discovered := discover.FormSStatement(stmt)
			wrappable := instrument.WrappableStatement(stmt)
			switch {
			case discovered && !wrappable:
				t.Errorf("discovery would hint Form S for %q and the instrumenter refuses it;\n"+
					"\tevery such hint fails its run at a site conflict", source)
			case !discovered && wrappable:
				t.Errorf("the instrumenter would wrap %q and discovery never hints it;\n"+
					"\tthat arm is unreachable, and unreachable code that looks like a feature\n"+
					"\tis how the next widening gets made in one place only", source)
			}
		})
	}
}

// TestTheTableCoversEveryStatementTypeGoHas is the table's own guard.
//
// The agreement above is worth exactly as much as the set it is checked over,
// and a statement type nobody listed is one both lists could be wrong about
// together. go/ast's statement types are a closed set, so the table can be
// required to hit all of them -- and when Go adds one, this fails and says so
// rather than the agreement quietly narrowing.
func TestTheTableCoversEveryStatementTypeGoHas(t *testing.T) {
	t.Parallel()

	// Every concrete type implementing ast.Stmt, by the name go/ast gives it.
	// BadStmt is absent because it is what a *parse error* produces, and no
	// tree either phase sees holds one: discovery refuses a package that does
	// not type-check, and the instrumenter parses a snapshot discovery already
	// read.
	want := []string{
		"*ast.AssignStmt", "*ast.BlockStmt", "*ast.BranchStmt", "*ast.CaseClause",
		"*ast.CommClause", "*ast.DeclStmt", "*ast.DeferStmt", "*ast.EmptyStmt",
		"*ast.ExprStmt", "*ast.ForStmt", "*ast.GoStmt", "*ast.IfStmt",
		"*ast.IncDecStmt", "*ast.LabeledStmt", "*ast.RangeStmt", "*ast.ReturnStmt",
		"*ast.SelectStmt", "*ast.SendStmt", "*ast.SwitchStmt", "*ast.TypeSwitchStmt",
	}
	seen := map[string]bool{}
	for _, source := range everyStatementKind {
		seen[typeNameOf(parseStatement(t, source))] = true
	}
	// A case clause and a comm clause are statements go/ast only ever builds
	// inside a switch or a select, so the table reaches them through the
	// switch and select lines rather than as rows of their own.
	seen["*ast.CaseClause"] = true
	seen["*ast.CommClause"] = true
	for _, name := range want {
		if !seen[name] {
			t.Errorf("no row of everyStatementKind parses to %s, so neither list is checked for it", name)
		}
	}
	for name := range seen {
		if !contains(want, name) {
			t.Errorf("everyStatementKind produces %s, which is not in the closed set this test lists", name)
		}
	}
}

// parseStatement parses one statement by wrapping it in the smallest function
// that can hold it.
//
// The label is declared around the body so that `break L`, `continue L` and
// `goto L` parse; go/parser does not resolve labels, but a `goto` to a label
// that is nowhere in the file is a shape worth not writing into a test fixture.
func parseStatement(t *testing.T, source string) ast.Stmt {
	t.Helper()

	src := "package p\nfunc f() {\nL:\nfor {\n" + source + "\n}\n}\n"
	file, err := parser.ParseFile(token.NewFileSet(), "stmt.go", src, 0)
	if err != nil {
		t.Fatalf("parsing %q: %v", source, err)
	}
	body := file.Decls[0].(*ast.FuncDecl).Body
	loop := body.List[0].(*ast.LabeledStmt).Stmt.(*ast.ForStmt)
	if len(loop.Body.List) != 1 {
		t.Fatalf("%q parsed to %d statements, want exactly 1", source, len(loop.Body.List))
	}
	return loop.Body.List[0]
}

// typeNameOf is the go/ast type name of a node, as `%T` prints it.
func typeNameOf(stmt ast.Stmt) string {
	return fmt.Sprintf("%T", stmt)
}

// contains reports whether a sorted-or-not list holds a name.
func contains(list []string, name string) bool {
	for _, candidate := range list {
		if candidate == name {
			return true
		}
	}
	return false
}
