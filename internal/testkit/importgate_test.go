// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	TestkitImportPath     = ModulePath + "/internal/testkit"
	TestSupportImportPath = ModulePath + "/internal/testsupport"
)

var skippedDirectories = []string{"testdata", FixturesDir, "vendor-assets", ".git"}

const ForwarderException = "internal/testsupport/cache.go"

const HarnessDir = "internal/testkit"

func TestImportGateAllowsTheHarnessToImportItself(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/gate")
	m.Source(HarnessDir+"/mutantkit/toolchain.go", "package mutantkit\n\nimport _ \""+TestkitImportPath+"\"\n")
	m.Source("internal/engine/engine.go", "package engine\n\nimport _ \""+TestkitImportPath+"/mutantkit\"\n")

	got, err := offendingImports(m.Root())
	if err != nil {
		t.Fatalf("scanning the synthesized module: %v", err)
	}
	want := []string{"internal/engine/engine.go imports " + TestkitImportPath + "/mutantkit"}
	if !slices.Equal(got, want) {
		t.Errorf("offendingImports = %q, want %q", got, want)
	}
}

func TestProductionCodeDoesNotImportTestkit(t *testing.T) {
	t.Parallel()

	root := Root(t)
	offenders, err := offendingImports(root)
	if err != nil {
		t.Fatalf("scanning %s for imports of the test harness: %v", root, err)
	}
	if len(offenders) != 0 {
		t.Errorf("%d production file(s) import the test harness, which would link the testing "+
			"package into go-mutants:\n\t%s", len(offenders), strings.Join(offenders, "\n\t"))
	}
}

func TestImportGateNamesTheOffendingFile(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/gate")
	m.Source("pkg/offender.go", "package pkg\n\nimport _ \""+TestkitImportPath+"\"\n")
	m.Source("pkg/innocent.go", "package pkg\n\nimport _ \"strings\"\n")
	m.Source("pkg/offender_test.go", "package pkg\n\nimport _ \""+TestkitImportPath+"/mutantkit\"\n")
	m.Source("testdata/ignored.go", "package ignored\n\nimport _ \""+TestkitImportPath+"\"\n")
	m.Source(FixturesDir+"/mod/ignored.go", "package ignored\n\nimport _ \""+TestkitImportPath+"\"\n")

	got, err := offendingImports(m.Root())
	if err != nil {
		t.Fatalf("scanning the synthesized module: %v", err)
	}
	if want := []string{"pkg/offender.go imports " + TestkitImportPath}; !slices.Equal(got, want) {
		t.Errorf("offendingImports = %q, want %q", got, want)
	}
}

func TestImportGateNamesADeeperImportOfTheHarness(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/gate")
	m.Source("deep.go", "package gate\n\nimport _ \""+TestkitImportPath+"/mutantkit\"\n")

	got, err := offendingImports(m.Root())
	if err != nil {
		t.Fatalf("scanning the synthesized module: %v", err)
	}
	if want := []string{"deep.go imports " + TestkitImportPath + "/mutantkit"}; !slices.Equal(got, want) {
		t.Errorf("offendingImports = %q, want %q", got, want)
	}
}

func TestImportGateNamesASecondFileInTheForwardersPackage(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/gate")
	m.Source(ForwarderException, "package testsupport\n\nimport _ \""+TestkitImportPath+"\"\n")
	m.Source("internal/testsupport/extra.go", "package testsupport\n\nimport _ \""+TestkitImportPath+"\"\n")

	got, err := offendingImports(m.Root())
	if err != nil {
		t.Fatalf("scanning the synthesized module: %v", err)
	}
	want := []string{"internal/testsupport/extra.go imports " + TestkitImportPath}
	if !slices.Equal(got, want) {
		t.Errorf("offendingImports = %q, want %q", got, want)
	}
}

func TestImportGateNamesAProductionImportOfTheForwarder(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/gate")
	m.Source("cli/run.go", "package cli\n\nimport _ \""+TestSupportImportPath+"\"\n")
	m.Source("cli/run_test.go", "package cli\n\nimport _ \""+TestSupportImportPath+"\"\n")

	got, err := offendingImports(m.Root())
	if err != nil {
		t.Fatalf("scanning the synthesized module: %v", err)
	}
	want := []string{"cli/run.go imports " + TestSupportImportPath}
	if !slices.Equal(got, want) {
		t.Errorf("offendingImports = %q, want %q", got, want)
	}
}

func offendingImports(root string) ([]string, error) {
	var offenders []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && slices.Contains(skippedDirectories, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative := filepath.ToSlash(rel)
		if relative == ForwarderException || strings.HasPrefix(relative, HarnessDir+"/") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parsing the imports of %s: %w", path, err)
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("reading the import path %s in %s: %w", spec.Path.Value, path, err)
			}
			if !forbiddenImport(imported) {
				continue
			}
			offenders = append(offenders, relative+" imports "+imported)
			break
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(offenders)
	return offenders, nil
}

func forbiddenImport(path string) bool {
	for _, forbidden := range []string{TestkitImportPath, TestSupportImportPath} {
		if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
			return true
		}
	}
	return false
}
