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

// The two import paths no production file may reach.
//
// The second is not a second harness: internal/testsupport is test-only support
// in its entirety, and its one exported helper is now a forwarder into this
// package. It has to be forbidden explicitly because the scan reads *direct*
// imports — a production file that imported the forwarder would pull the
// harness, and `testing` with it, one hop further along, and a gate that watched
// only for the harness would report nothing at all.
const (
	TestkitImportPath     = ModulePath + "/internal/testkit"
	TestSupportImportPath = ModulePath + "/internal/testsupport"
)

// skippedDirectories are the trees that hold Go files which are not this
// module's production code: the corpus is a set of modules of their own, the
// golden and helper trees under testdata/ are inputs rather than programs, and
// vendor-assets holds somebody else's code.
var skippedDirectories = []string{"testdata", FixturesDir, "vendor-assets", ".git"}

// ForwarderException is the one file that may import the harness without being a
// _test.go.
//
// internal/testsupport/cache.go is test-only support whose exported helper is a
// forwarder into this package, kept so that its fourteen call sites go on
// compiling while the suites move over; it disappears when they have, and this
// constant with it.
//
// It is one *file* rather than its package, and the difference is the whole
// point of the exception. Written as a package prefix, it silently extends the
// permission to every file anybody adds beside it — so a new production package
// linking `testing` would be admitted by a rule that was written about a file on
// its way out, and nobody would be told.
const ForwarderException = "internal/testsupport/cache.go"

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
	if len(offenders) != 0 {
		t.Errorf("%d production file(s) import the test harness, which would link the testing "+
			"package into go-mutants:\n\t%s", len(offenders), strings.Join(offenders, "\n\t"))
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
	if want := []string{"pkg/offender.go imports " + TestkitImportPath}; !slices.Equal(got, want) {
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
	if want := []string{"deep.go imports " + TestkitImportPath + "/mutantkit"}; !slices.Equal(got, want) {
		t.Errorf("offendingImports = %q, want %q", got, want)
	}
}

// TestImportGateNamesASecondFileInTheForwardersPackage is the difference between
// an exception and a hole.
//
// One file is allowed to import the harness — internal/testsupport/cache.go, the
// forwarder that keeps its fourteen call sites compiling until they migrate — and
// an exception written as a package prefix quietly extends that permission to
// every file anybody adds beside it. A second file there would be a new
// production package importing `testing`, admitted by a rule that was written
// about a file that is on its way out.
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

// TestImportGateNamesAProductionImportOfTheForwarder closes the way round the
// gate.
//
// The scan reads direct imports, so exempting the forwarder for importing the
// harness would let any production file reach the harness — and `testing`, and
// its flag registrations — one hop further along by importing the forwarder
// instead. internal/testsupport is test-only support in its entirety, so no
// production file may import it either, and the exception stays what it says it
// is: one file, on its way out.
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

// offendingImports returns every non-test Go file under root that imports the
// test harness or its forwarder, as `<path> imports <import path>` lines
// relative to root, sorted.
//
// The import is named as well as the file, because there are two rules and a
// reader has to know which one was broken: importing the harness links `testing`
// directly, and importing the forwarder links it one hop further along.
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
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative := filepath.ToSlash(rel)
		if relative == ForwarderException {
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

// forbiddenImport reports whether a production file may not import a path.
//
// Both trees are matched rather than both exact paths, because the rule is about
// the tree: internal/testkit/mutantkit imports the engine and is just as
// forbidden as its parent, and a sub-package of the forwarder would be no more
// importable than the forwarder.
func forbiddenImport(path string) bool {
	for _, forbidden := range []string{TestkitImportPath, TestSupportImportPath} {
		if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
			return true
		}
	}
	return false
}
