// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	// enginePrefix is the engine's module path.
	enginePrefix = "github.com/P4suta/go-mutants"
	// runnerPrefix is this module's, which is a path *under* the engine's --
	// which is why every check here compares against the runner's first.
	runnerPrefix = enginePrefix + "/goatest"
	// engineInternal is the half of the engine this module may not reach.
	engineInternal = enginePrefix + "/internal/"
	// sharedHarness is the one package under it that a test may.
	sharedHarness = enginePrefix + "/internal/testkit"
)

// TestTheRunnerReachesTheEngineThroughItsPublicAPI enforces ADR 0036.
//
// Go's `internal/` rule is a path-prefix test, so this module *may* import the
// engine's internals: `github.com/P4suta/go-mutants/goatest/...` sits under
// `github.com/P4suta/go-mutants/`, and the loader asks nothing else. Before the
// two products shared a repository the module boundary refused it, and
// `internal/mutationbridge` exists because of that refusal -- one file, one
// door, the whole contract frozen behind it. The refusal is gone; only the
// habit is left, and a habit is not a rule.
//
// It matters beyond layering. `docs/library.md` is the contract the engine
// publishes and its synthetic consumer is what proves the contract compiles;
// the reason both are worth their cost is that this module is the contract's
// first and largest consumer. A runner that reached past it would leave the
// contract proved only by the test written to prove it.
func TestTheRunnerReachesTheEngineThroughItsPublicAPI(t *testing.T) {
	t.Parallel()

	var offenders []string
	walkGoFiles(t, moduleRoot(t), func(path string, test bool, imports []string) {
		for _, imported := range imports {
			if !strings.HasPrefix(imported, engineInternal) {
				continue
			}
			// A test may share the harness and nothing else. The harness is
			// already test-only and already imports nothing from either
			// product, which is what makes sharing it cost nothing; sharing
			// anything else would make this module depend on the engine's
			// private shape.
			if test && (imported == sharedHarness || strings.HasPrefix(imported, sharedHarness+"/")) {
				continue
			}
			offenders = append(offenders, path+" imports "+imported)
		}
	})
	if len(offenders) != 0 {
		t.Errorf("%d import(s) reach past the engine's public API:\n\t%s\n"+
			"\tGo permits it -- internal/ is a path prefix and this module sits under one --\n"+
			"\tand ADR 0036 does not. Add what is needed to the engine's API, or reach it\n"+
			"\tthrough internal/mutationbridge, which is the door that exists for this.",
			len(offenders), strings.Join(offenders, "\n\t"))
	}
}

// TestTheEngineDoesNotImportTheRunner is the direction the language already
// refuses, written down so that the refusal is a rule rather than a build
// error somebody works around.
//
// A cyclic `require` does not build, so this cannot be violated by accident.
// It can be violated on purpose with a `replace`, and the person reaching for
// one would be reading a message about modules rather than about design.
func TestTheEngineDoesNotImportTheRunner(t *testing.T) {
	t.Parallel()

	engine := filepath.Dir(moduleRoot(t))
	var offenders []string
	walkGoFiles(t, engine, func(path string, _ bool, imports []string) {
		if strings.HasPrefix(path, "goatest"+string(filepath.Separator)) {
			return
		}
		for _, imported := range imports {
			if strings.HasPrefix(imported, runnerPrefix) {
				offenders = append(offenders, path+" imports "+imported)
			}
		}
	})
	if len(offenders) != 0 {
		t.Errorf("%d import(s) point from the engine at the runner:\n\t%s\n"+
			"\tthe require would be cyclic, so this is a replace somebody added",
			len(offenders), strings.Join(offenders, "\n\t"))
	}
}

// moduleRoot is this module's directory, found from this file rather than from
// the working directory.
func moduleRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above this test")
		}
		dir = parent
	}
}

// walkGoFiles visits every Go file under a root, with its import paths and
// whether it is a test file.
//
// The imports are parsed rather than grepped: a path inside a string literal or
// a comment is not an import, and a gate that could not tell the difference
// would be one somebody learns to word around.
func walkGoFiles(t testing.TB, root string, visit func(path string, test bool, imports []string)) {
	t.Helper()
	skip := map[string]bool{".git": true, "testdata": true, "fixtures": true, "dist": true}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skip[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", path, parseErr)
		}
		imports := make([]string, 0, len(file.Imports))
		for _, spec := range file.Imports {
			value, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				t.Fatalf("reading an import of %s: %v", path, unquoteErr)
			}
			imports = append(imports, value)
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			relative = path
		}
		visit(relative, strings.HasSuffix(entry.Name(), "_test.go"), imports)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}
