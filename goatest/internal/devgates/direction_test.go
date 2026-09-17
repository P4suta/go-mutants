// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates_test

import (
	"errors"
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
	enginePrefix   = "github.com/P4suta/go-mutants"
	runnerPrefix   = enginePrefix + "/goatest"
	engineInternal = enginePrefix + "/internal/"
	sharedHarness  = enginePrefix + "/internal/testkit"
)

func TestTheRunnerReachesTheEngineThroughItsPublicAPI(t *testing.T) {
	t.Parallel()

	var offenders []string
	walkGoFiles(t, moduleRoot(t), func(path string, test bool, imports []string) {
		for _, imported := range imports {
			if !strings.HasPrefix(imported, engineInternal) {
				continue
			}
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

func TestTheEngineDoesNotImportTheRunner(t *testing.T) {
	t.Parallel()

	engine, found := engineRoot(t)
	if !found {
		if _, err := os.Stat(filepath.Join(moduleRoot(t), "go.mod")); err != nil {
			t.Fatalf("this is not a Go module: %v", err)
		}
		return
	}
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

func engineRoot(t testing.TB) (string, bool) {
	t.Helper()
	dir := moduleRoot(t)
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, true
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("looking for the engine above %s: %v", moduleRoot(t), err)
		}
	}
}
