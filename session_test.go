// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
)

// macOS exposes its temporary directory through both /var and /private/var.
// Go subprocesses may canonicalise that spelling even though os.MkdirTemp did
// not, so fuzz workspace containment must compare filesystem identities rather
// than the two lexical paths.
func TestPrepareFuzzWorkspaceAcceptsAliasedSnapshotParent(t *testing.T) {
	realParent := t.TempDir()
	realRoot := filepath.Join(realParent, "snapshot")
	packageDir := filepath.Join(realRoot, "subject")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "subject.go"), []byte("package subject\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	aliasRoot := filepath.Join(aliasParent, "snapshot")
	destination := filepath.Join(t.TempDir(), "fuzz-workspace")

	got, err := prepareFuzzWorkspace(aliasRoot, destination, []execute.TestBinary{{
		ImportPath: "fixture.example/subject",
		Dir:        packageDir,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Dir != filepath.Join(destination, "subject") {
		t.Fatalf("fuzz workspace binaries = %+v", got)
	}
}

func TestSelectTestPackagesAcceptsAliasedSnapshotRoot(t *testing.T) {
	realParent := t.TempDir()
	realRoot := filepath.Join(realParent, "snapshot")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	aliasRoot := filepath.Join(aliasParent, "snapshot")

	got, err := selectTestPackages(aliasRoot, []execute.TestBinary{{
		ImportPath: "fixture.example/root",
		Dir:        realRoot,
	}}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("selected package indexes = %v, want [0]", got)
	}
}
