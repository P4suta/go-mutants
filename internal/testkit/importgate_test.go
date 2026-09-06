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

// TestkitImportPath is the prefix no production file may import.
const TestkitImportPath = ModulePath + "/internal/testkit"

// skippedDirectories are the trees that hold Go files which are not this
// module's production code: the corpus is a set of modules of their own, the
// golden and helper trees under testdata/ are inputs rather than programs, and
// vendor-assets holds somebody else's code.
var skippedDirectories = []string{"testdata", FixturesDir, "vendor-assets", ".git"}

// testOnlyPackages may import testkit from a file that is not a _test.go.
//
// internal/testsupport is one: it is itself test-only support — imported from
// _test files and nowhere else — and its exported helper is now a forwarder into
// this package so that its call sites keep compiling while the suites move over.
// It disappears when they have, and this list with it. A production package
// added here would be the defect the gate exists to catch, which is why it is a
// list of one with a reason attached rather than a pattern.
var testOnlyPackages = []string{"internal/testsupport"}

// TestProductionCodeDoesNotImportTestkit is the layering rule as a test.
//
// A production package that imported testkit would link `testing` into
// `go-mutants`: the testing package registers flags in its init, so a released
// binary would grow `-test.v` and friends, and a public API would be able to
// fail a test that does not exist. `.golangci.yml` has no depguard, and this
// repository's precedent for a rule like this is a go/ast scan in a test, so
// that is what this is.
//
// The scan is over files rather than over the import graph on purpose: `go list`
// would need a toolchain, which the unit tier does not require, and a parse of
// every non-test file needs nothing but the standard library.
func TestProductionCodeDoesNotImportTestkit(t *testing.T) {
	t.Parallel()

	root := Root(t)
	offenders, err := offendingImports(root)
	if err != nil {
		t.Fatalf("scanning %s for imports of the test harness: %v", root, err)
	}
	offenders = slices.DeleteFunc(offenders, func(path string) bool {
		return slices.ContainsFunc(testOnlyPackages, func(allowed string) bool {
			return strings.HasPrefix(path, allowed+"/")
		})
	})
	if len(offenders) != 0 {
		t.Errorf("%d production file(s) import %s, which would link the testing package into "+
			"go-mutants:\n\t%s", len(offenders), TestkitImportPath, strings.Join(offenders, "\n\t"))
	}
}

// TestImportGateNamesTheOffendingFile proves the gate can fail, and that its
// failure is actionable.
//
// A gate that only ever passes is indistinguishable from a gate that cannot see
// anything, and the way this one would break is by skipping too much — a walk
// that ignored a directory it should not, or an import list read from a parse
// that failed silently. So the scan is pointed at a module built to offend, and
// the assertion is the path in the report rather than the count.
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
	if want := []string{"pkg/offender.go"}; !slices.Equal(got, want) {
		t.Errorf("offendingImports = %q, want %q", got, want)
	}
}

// TestImportGateNamesADeeperImportOfTheHarness covers the sub-package: the rule
// is about the tree, so internal/testkit/mutantkit — which imports the engine —
// is just as forbidden, and matching the exact path would have missed it.
func TestImportGateNamesADeeperImportOfTheHarness(t *testing.T) {
	t.Parallel()

	m := NewModule(t).Module("fixture.example/gate")
	m.Source("deep.go", "package gate\n\nimport _ \""+TestkitImportPath+"/mutantkit\"\n")

	got, err := offendingImports(m.Root())
	if err != nil {
		t.Fatalf("scanning the synthesized module: %v", err)
	}
	if want := []string{"deep.go"}; !slices.Equal(got, want) {
		t.Errorf("offendingImports = %q, want %q", got, want)
	}
}

// offendingImports returns every non-test Go file under root that imports the
// test harness, as slash-separated paths relative to root, sorted.
//
// Only the import declarations are parsed, which is both the cheapest read of a
// Go file and the one that cannot be wrong about anything else: a file that does
// not compile still has an import list, and a rule about imports should not
// depend on the rest of the file being valid.
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
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parsing the imports of %s: %w", path, err)
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("reading the import path %s in %s: %w", spec.Path.Value, path, err)
			}
			if imported != TestkitImportPath && !strings.HasPrefix(imported, TestkitImportPath+"/") {
				continue
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			offenders = append(offenders, filepath.ToSlash(rel))
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
