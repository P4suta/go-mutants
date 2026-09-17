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

func TestBothPhasesAgreeOnWhatFormFCanClose(t *testing.T) {
	t.Parallel()

	for name, source := range everyStatementKind {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			stmt := parseStatement(t, source)
			discovered := discover.FormFStatement(stmt)
			closurable := instrument.ClosurableStatement(stmt)
			switch {
			case discovered && !closurable:
				t.Errorf("discovery would hint Form F for %q and the instrumenter refuses it;\n"+
					"\tevery such hint fails its run at a site conflict", source)
			case !discovered && closurable:
				t.Errorf("the instrumenter would close over %q and discovery never hints it;\n"+
					"\tthat arm is unreachable, and unreachable code that looks like a feature\n"+
					"\tis how the next widening gets made in one place only", source)
			}
		})
	}
}

func TestEveryFormFStatementIsAlsoAFormSStatement(t *testing.T) {
	t.Parallel()

	for name, source := range everyStatementKind {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			stmt := parseStatement(t, source)
			if discover.FormFStatement(stmt) && !discover.FormSStatement(stmt) {
				t.Errorf("Form F would close over %q and Form S would not wrap it, "+
					"which is a guard holding a statement the guard inside it cannot", source)
			}
		})
	}
}

func TestTheTableCoversEveryStatementTypeGoHas(t *testing.T) {
	t.Parallel()

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

func typeNameOf(stmt ast.Stmt) string {
	return fmt.Sprintf("%T", stmt)
}

func contains(list []string, name string) bool {
	for _, candidate := range list {
		if candidate == name {
			return true
		}
	}
	return false
}
