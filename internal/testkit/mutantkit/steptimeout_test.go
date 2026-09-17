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

	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

var timeoutSubjects = map[string]string{
	"TestFakeGoSleepIsCutOffByTheCallersTimeout": "its subject is the deadline, so the deadline has to be short enough to wait for",
}

func TestTheStepAlarmIsOneNumber(t *testing.T) {
	t.Parallel()

	if mutantkit.StepTimeout != testkit.DefaultTimeout {
		t.Errorf("mutantkit.StepTimeout is %s and testkit.DefaultTimeout is %s, want one number",
			mutantkit.StepTimeout, testkit.DefaultTimeout)
	}
}

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

var boundedLiterals = map[string]string{
	"runner": "Spec",
	"gocmd":  "Options",
}

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
