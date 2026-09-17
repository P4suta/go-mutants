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

// TestTheWorkerCeilingIsOneNumberInTwoTrees holds the engine and the runner to
// a single answer to a single question: how much of a machine a mutation run is
// allowed to take.
//
// They answered it twice. The engine's answer carries its argument -- a run is a
// background chore and a laptop should stay usable through one -- and the
// runner's was a bare 4 with nothing beside it. The 4 also won every time: a run
// always hands the engine an explicit Jobs, so the zero case the engine's
// ceiling governs was never reached. On an eighteen-core machine the runner took
// four cores and the number that was supposed to decide that never applied.
//
// This reads both trees rather than importing either, and that is the point
// rather than a shortcut. The runner is built with GOWORK=off against the engine
// version go.mod pins, so `gomutants.DefaultJobs()` -- which returns exactly
// this ceiling -- cannot be named from the runner until that pin moves. An
// import would compile under `mise run check`, which goes through go.work, and
// fail under `mise run dogfood-runner`, which does not. A gate that reads files
// is answering about the repository, which is the thing that has to agree.
//
// It is the test two repositories could not hold at all. Neither half was wrong
// on its own; what was wrong is that there were two, and nothing either side
// could reach would have said so.
func TestTheWorkerCeilingIsOneNumberInTwoTrees(t *testing.T) {
	t.Parallel()

	engine, found := engineRoot(t)
	if !found {
		// Out of scope rather than unchecked: a run copies this module into a
		// tree of its own and the engine is not beside the copy, so there is no
		// second number here to disagree with. See TestTheEngineDoesNotImportTheRunner.
		return
	}
	want := constantValue(t, filepath.Join(engine, "internal", "config", "config.go"), "DefaultJobCap")
	got := constantValue(t, filepath.Join(moduleRoot(t), "internal", "assure", "run.go"), "defaultMutationJobCap")
	if got != want {
		t.Fatalf("the runner caps its derived worker count at %d and the engine at %d;"+
			" one question, and whichever is wrong is wrong on every machine both run on", got, want)
	}
}

// constantValue is the integer a named constant is declared with, and it fails
// rather than returning a zero for one that is absent or is not an integer --
// a gate that read a missing constant as 0 would pass the moment either side
// was renamed, which is the failure this whole file exists to refuse.
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
