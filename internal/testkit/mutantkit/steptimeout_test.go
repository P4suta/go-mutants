// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// timeoutSubjects are the tests whose subject is the bound itself, and which
// therefore set one of their own.
//
// It is a ledger rather than a heuristic for the reason every other ledger in
// this repository is one: a test that opts out says so by name, in a list a
// reader can count, and a name that has gone stale fails as loudly as an
// offender. It may shrink and should never grow — a new test that wants a
// bound of its own is a new test that has to argue for it here.
var timeoutSubjects = map[string]string{
	"TestFakeGoSleepIsCutOffByTheCallersTimeout": "its subject is the deadline, so the deadline has to be short enough to wait for",
}

// TestEveryChildThisPackageStartsIsBoundedByStepTimeout holds this package to
// the claim its own documentation makes.
//
// [mutantkit.StepTimeout]'s doc comment says it "bounds every child a test
// starts through this package", and until this test existed nothing checked it.
// Seven literals in fakego_test.go carried a hand-written thirty seconds instead —
// half the stated bound, and enough that copying a forty-megabyte Mach-O and
// exec'ing it, which makes the macOS kernel hash the whole image, timed out on a
// loaded machine while passing in ten seconds on an idle one.
//
// The rule is about the number rather than about the duration: a bound written
// twice is a bound that can disagree with itself, and which of the two a
// particular machine trips is not something a suite should depend on. So the
// check is that every runner.Spec in this package's tests names the constant,
// and the one test whose subject is a deadline is named in a ledger above.
func TestEveryChildThisPackageStartsIsBoundedByStepTimeout(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join(".", "*_test.go"))
	if err != nil {
		t.Fatalf("listing this package's test files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("the scan found no test files at all, so it is not looking where they are")
	}

	seen := map[string]bool{}
	specs := 0
	for _, path := range files {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, source, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			name := fn.Name.Name
			ast.Inspect(fn, func(n ast.Node) bool {
				value, ok := timeoutOf(n)
				if !ok {
					return true
				}
				specs++
				if reason, excused := timeoutSubjects[name]; excused {
					seen[name] = true
					_ = reason
					return true
				}
				if value != "mutantkit.StepTimeout" {
					position := fset.Position(n.Pos())
					t.Errorf("%s: %s builds a bounded literal with Timeout: %s, want mutantkit.StepTimeout\n"+
						"\tStepTimeout's doc says it bounds every child a test starts through this package;\n"+
						"\ta second number here is a second bound that can disagree with it",
						position, name, value)
				}
				return true
			})
		}
	}
	if specs == 0 {
		t.Fatal("the scan found no bounded literals at all, so it is not looking at what it thinks")
	}
	for name := range timeoutSubjects {
		if !seen[name] {
			t.Errorf("the ledger names %s, which builds no bounded literal with a timeout any more;\n"+
				"\tdelete the row — a stale excuse is as wrong as a missing one", name)
		}
	}
}

// boundedLiterals are the composite literals that carry a bound on a child this
// package starts: the spec internal/runner supervises one with, and the options
// internal/gocmd probes a toolchain with. Both start a process; both are
// therefore "a child a test starts through this package".
var boundedLiterals = map[string]string{
	"runner": "Spec",
	"gocmd":  "Options",
}

// timeoutOf reports the source spelling of a bounded literal's Timeout field,
// when the node is one and it sets the field.
func timeoutOf(n ast.Node) (string, bool) {
	composite, ok := n.(*ast.CompositeLit)
	if !ok {
		return "", false
	}
	selector, ok := composite.Type.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || boundedLiterals[pkg.Name] != selector.Sel.Name {
		return "", false
	}
	for _, element := range composite.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := pair.Key.(*ast.Ident)
		if !ok || key.Name != "Timeout" {
			continue
		}
		return spell(pair.Value), true
	}
	return "", false
}

// spell renders an expression the way the source wrote it, for a message a
// reader can grep the file for.
func spell(expr ast.Expr) string {
	var b strings.Builder
	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		switch v := e.(type) {
		case *ast.Ident:
			b.WriteString(v.Name)
		case *ast.BasicLit:
			b.WriteString(v.Value)
		case *ast.SelectorExpr:
			walk(v.X)
			b.WriteString(".")
			b.WriteString(v.Sel.Name)
		case *ast.BinaryExpr:
			walk(v.X)
			b.WriteString(" " + v.Op.String() + " ")
			walk(v.Y)
		default:
			b.WriteString("?")
		}
	}
	walk(expr)
	return b.String()
}
