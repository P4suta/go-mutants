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

func TestRootReportsAWorkingDirectoryOutsideTheModule(t *testing.T) {
	t.Parallel()

	if _, err := moduleRoot(t.TempDir()); err == nil {
		t.Fatal("moduleRoot from a temporary directory succeeded, want an error")
	} else if !strings.Contains(err.Error(), ModulePath) {
		t.Errorf("the error does not name the module it was looking for: %v", err)
	}
}

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

func TestFixtureRefusesANameThatLeavesTheCorpus(t *testing.T) {
	t.Parallel()

	root := Root(t)
	for _, name := range []string{"..", "../internal", "simple/..", "simple/inner", "/etc"} {
		if got, err := fixturePath(root, name); err == nil {
			t.Errorf("fixturePath(%q) = %s, want an error", name, got)
		}
	}
}

func TestFixtureNamesListsEveryModuleInTheCorpus(t *testing.T) {
	t.Parallel()

	names := FixtureNames(t)
	if !slices.IsSorted(names) {
		t.Errorf("FixtureNames is not sorted: %q", names)
	}
	for _, want := range []string{"killable", "simple", "workspace"} {
		if !slices.Contains(names, want) {
			t.Errorf("FixtureNames = %q, which does not include %q", names, want)
		}
	}
	if slices.Contains(names, "README.md") {
		t.Errorf("FixtureNames = %q, which includes a file that is not a module", names)
	}
	for _, name := range names {
		dir := Fixture(t, name)
		_, moduleErr := os.Stat(filepath.Join(dir, "go.mod"))
		_, workspaceErr := os.Stat(filepath.Join(dir, "go.work"))
		if moduleErr != nil && workspaceErr != nil {
			t.Errorf("FixtureNames returned %q, which is neither a module nor a workspace: %v; %v",
				name, moduleErr, workspaceErr)
		}
	}
}
