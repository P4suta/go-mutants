// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ModulePath is this repository's module path, and the only evidence a walk up
// the filesystem has that it has arrived at the repository root.
const ModulePath = "github.com/P4suta/go-mutants"

// FixturesDir is the corpus directory, relative to the module root.
const FixturesDir = "fixtures"

// Root returns the absolute path of this repository's root.
//
// It walks up from the working directory looking for the go.mod that names
// [ModulePath], and — this is the part a `filepath.Join("..", "..")` in a test
// cannot do — it does not stop at the first go.mod it meets. Every fixture is a
// module of its own, which is what keeps this repository's own `./...` from
// picking the corpus up, so a test whose working directory is a fixture walks
// straight past that go.mod. A relative path in a test also breaks the moment
// the test moves to a package one level deeper; this does not.
func Root(t testing.TB) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}
	root, err := moduleRoot(cwd)
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	return root
}

// Fixture returns the absolute path of one corpus module, failing the test
// unless it is one.
//
// The check is the whole point. A fixture is a tree a `go` command can be
// pointed at rather than a package — the engine snapshots a directory, builds
// it and runs its tests — so a name that resolves to a directory holding
// neither a `go.mod` nor a `go.work` is a typo or a deleted fixture, and it is
// worth saying so here rather than three phases into a run that cannot work.
//
// A `go.work` counts because one fixture is a workspace on purpose. `workspace/`
// is the tree a run has to refuse at its root and has to measure one module of
// when pointed inside it, and neither claim can be made about a directory this
// package will not hand out.
func Fixture(t testing.TB, name string) string {
	t.Helper()
	path, err := fixturePath(Root(t), name)
	if err != nil {
		t.Fatalf("resolving a fixture: %v", err)
	}
	rememberFixture(t, path)
	logInputs(t, "fixture="+path)
	return path
}

// FixtureNames lists the corpus, sorted.
//
// Only directories a `go` command could be pointed at are returned, so a test
// that iterates the corpus iterates fixtures: `fixtures/README.md` documents the
// corpus and is not part of it.
func FixtureNames(t testing.TB) []string {
	t.Helper()
	dir := filepath.Join(Root(t), FixturesDir)
	found, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the corpus at %s: %v", dir, err)
	}
	names := make([]string, 0, len(found))
	for _, entry := range found {
		if !entry.IsDir() || !isFixtureRoot(filepath.Join(dir, entry.Name())) {
			continue
		}
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

// isFixtureRoot reports whether a directory is a tree the engine could be
// pointed at: a module, or the workspace fixture's root.
func isFixtureRoot(dir string) bool {
	for _, name := range []string{"go.mod", "go.work"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// moduleRoot walks up from start to the directory whose go.mod names
// [ModulePath].
func moduleRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("making %s absolute: %w", start, err)
	}
	for {
		path, err := modulePathOf(filepath.Join(dir, "go.mod"))
		if err != nil {
			return "", err
		}
		if path == ModulePath {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod naming module %s at or above %s", ModulePath, start)
		}
		dir = parent
	}
}

// modulePathOf reads the module path out of a go.mod, returning the empty
// string when the file is not there.
//
// The parse is deliberately the one line it needs rather than golang.org/x/mod:
// this package's import list holds nothing from this module, and outside the
// standard library only github.com/google/go-cmp, because it is imported by the
// tests of the pure packages and its own weight would otherwise travel with
// them.
func modulePathOf(gomod string) (string, error) {
	data, err := os.ReadFile(gomod)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading %s: %w", gomod, err)
	}
	for line := range strings.Lines(string(data)) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module")
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(rest)
		if trimmed == rest {
			// "modulepath" rather than "module path": a different directive.
			continue
		}
		return strings.Trim(trimmed, "\""), nil
	}
	return "", nil
}

// fixturePath resolves one corpus name, refusing anything that is not a fixture
// directly inside the corpus.
//
// Refusing a name with a separator in it is what keeps the corpus from being a
// route into the rest of the checkout: `Fixture(t, "../internal")` would hand a
// test a copy target, a snapshot root or a mutation workspace pointing at the
// working tree the test is running in, and not modifying the user's tree is
// this project's first invariant.
func fixturePath(root, name string) (string, error) {
	switch {
	case name == "":
		return "", errors.New("a fixture name may not be empty")
	case name != filepath.Base(name), name != filepath.Clean(name):
		return "", fmt.Errorf("fixture %q is not a name directly inside %s/", name, FixturesDir)
	case name == "." || name == "..":
		return "", fmt.Errorf("fixture %q is not a name directly inside %s/", name, FixturesDir)
	}
	path := filepath.Join(root, FixturesDir, name)
	if !isFixtureRoot(path) {
		return "", fmt.Errorf("fixture %q is not a tree a `go` command can be pointed at: "+
			"%s holds neither a go.mod nor a go.work", name, path)
	}
	return path, nil
}

// logInputs records what a constructor resolved, one line per constructor.
//
// A test that fails in CI on a machine nobody can reach is diagnosed from its
// log, and the questions asked first are always the same: which fixture, which
// toolchain, which scratch directory, which build cache. Each is a fact the
// helper knew and the test never printed.
func logInputs(t testing.TB, fields ...string) {
	t.Helper()
	if len(fields) == 0 {
		return
	}
	// The forced-failure hook lives here rather than only in [Scratch], because
	// this is the one line every constructor in the package already writes: a
	// test that resolves a fixture, locates a toolchain or reads a shared
	// session takes no scratch directory of its own, and naming one of those in
	// GO_MUTANTS_TEST_FORCE_FAIL used to do nothing at all.
	ForceFail(t)
	t.Logf("testkit: %s keep=%s", strings.Join(fields, " "), KeepPolicy())
}
