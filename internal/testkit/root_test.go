// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestRootFindsTheModuleFromAnyPackageDirectory pins the one property that
// makes every path helper here usable from a test anywhere in the tree.
//
// The walk cannot stop at the first go.mod it meets. A fixture is a module of
// its own — that is what keeps this repository's `./...` from ever picking one
// up — so a test whose working directory is a fixture would otherwise resolve
// the corpus root as the repository root and look for `fixtures/fixtures/…`.
// The walk is for the go.mod that names *this* module, and nothing else.
func TestRootFindsTheModuleFromAnyPackageDirectory(t *testing.T) {
	root := Root(t)
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("Root returned %s, which has no go.mod: %v", root, err)
	}

	for _, from := range []string{root, filepath.Join(root, "internal", "testkit"), Fixture(t, "simple")} {
		t.Run(filepath.Base(from), func(t *testing.T) {
			t.Chdir(from)
			if got := Root(t); !SamePath(got, root) {
				t.Errorf("Root from %s = %s, want %s", from, got, root)
			}
		})
	}
}

// TestRootReportsAWorkingDirectoryOutsideTheModule keeps the failure legible.
//
// A test binary run from somewhere else entirely — a t.TempDir, a `go test` in
// another checkout — has no answer to give, and the useful diagnostic names the
// module it was looking for and the directory it started from rather than
// returning an empty string that fails later somewhere else.
func TestRootReportsAWorkingDirectoryOutsideTheModule(t *testing.T) {
	t.Parallel()

	if _, err := moduleRoot(t.TempDir()); err == nil {
		t.Fatal("moduleRoot from a temporary directory succeeded, want an error")
	} else if !strings.Contains(err.Error(), ModulePath) {
		t.Errorf("the error does not name the module it was looking for: %v", err)
	}
}

// TestFixtureRefusesANameWithoutGoMod is the corpus half of the same rule: a
// fixture is a whole module, and a name that does not resolve to one is a typo
// or a deleted directory rather than something to hand to the engine and watch
// fail three phases later.
func TestFixtureRefusesANameWithoutGoMod(t *testing.T) {
	t.Parallel()

	root := Root(t)
	for _, name := range []string{"", "does-not-exist", "README.md"} {
		if got, err := fixturePath(root, name); err == nil {
			t.Errorf("fixturePath(%q) = %s, want an error", name, got)
		}
	}

	got, err := fixturePath(root, "simple")
	if err != nil {
		t.Fatalf("fixturePath(%q): %v", "simple", err)
	}
	if want := filepath.Join(root, "fixtures", "simple"); got != want {
		t.Errorf("fixturePath(%q) = %s, want %s", "simple", got, want)
	}
}

// TestFixtureRefusesANameThatLeavesTheCorpus stops a caller from reaching the
// rest of the repository through the corpus. `Fixture(t, "../internal")` is not
// a fixture, and a helper that resolved it would let a test mutate, copy or
// snapshot the working tree it is running in — which is the one thing this
// project promises never to do.
func TestFixtureRefusesANameThatLeavesTheCorpus(t *testing.T) {
	t.Parallel()

	root := Root(t)
	for _, name := range []string{"..", "../internal", "simple/..", "simple/inner", "/etc"} {
		if got, err := fixturePath(root, name); err == nil {
			t.Errorf("fixturePath(%q) = %s, want an error", name, got)
		}
	}
}

// TestFixtureNamesListsEveryModuleInTheCorpus proves the listing is the corpus
// rather than a directory listing: `README.md` lives beside the fixtures and is
// not one, and a test that iterates the corpus must not be handed it.
func TestFixtureNamesListsEveryModuleInTheCorpus(t *testing.T) {
	t.Parallel()

	names := FixtureNames(t)
	if !slices.IsSorted(names) {
		t.Errorf("FixtureNames is not sorted: %q", names)
	}
	for _, want := range []string{"killable", "simple"} {
		if !slices.Contains(names, want) {
			t.Errorf("FixtureNames = %q, which does not include %q", names, want)
		}
	}
	if slices.Contains(names, "README.md") {
		t.Errorf("FixtureNames = %q, which includes a file that is not a module", names)
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(Fixture(t, name), "go.mod")); err != nil {
			t.Errorf("FixtureNames returned %q, which is not a module: %v", name, err)
		}
	}
}
