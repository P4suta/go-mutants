// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

func TestTheWorkerCeilingIsOneNumberInTwoTrees(t *testing.T) {
	t.Parallel()

	engine, found := engineRoot(t)
	if !found {
		return
	}
	want := constantValue(t, filepath.Join(engine, "internal", "config", "config.go"), "DefaultJobCap")
	got := constantValue(t, filepath.Join(moduleRoot(t), "internal", "assure", "run.go"), "defaultMutationJobCap")
	if got != want {
		t.Fatalf("the runner caps its derived worker count at %d and the engine at %d;"+
			" one question, and whichever is wrong is wrong on every machine both run on", got, want)
	}
}

func constantValue(t *testing.T, path, name string) int {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, ident := range value.Names {
				if ident.Name != name || index >= len(value.Values) {
					continue
				}
				literal, ok := value.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.INT {
					t.Fatalf("%s in %s is not an integer literal", name, path)
				}
				parsed, err := strconv.Atoi(literal.Value)
				if err != nil {
					t.Fatalf("%s in %s: %v", name, path, err)
				}
				return parsed
			}
		}
	}
	t.Fatalf("no constant %s in %s", name, path)
	return 0
}
