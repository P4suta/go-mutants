// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetectWorkspaceReadsTheModulesAWorkspaceJoins is the success path, and
// the one every refusal below is measured against.
//
// What the detector answers is not "is this a workspace" -- os.Stat answers
// that -- but "which modules is it, and where are they". Both halves are
// needed before anything else can happen: the directories decide what is
// snapshotted and what each baseline runs in, and the module paths are the
// tenth field of a workspace mutant's identity, which is the only thing
// keeping two modules' `app.go` apart.
func TestDetectWorkspaceReadsTheModulesAWorkspaceJoins(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeWorkspace(t, root, "go 1.26\n\nuse (\n\t./app\n\t./lib\n)\n")
	writeModuleAt(t, filepath.Join(root, "app"), "example.com/ws/app")
	writeModuleAt(t, filepath.Join(root, "lib"), "example.com/ws/lib")

	ws, err := DetectWorkspace(root)
	if err != nil {
		t.Fatalf("DetectWorkspace: %v", err)
	}
	if ws == nil {
		t.Fatal("a directory holding a go.work was read as a single module")
	}
	if ws.Root != root {
		t.Errorf("Root = %q, want %q", ws.Root, root)
	}
	want := []WorkspaceModule{
		{Dir: "app", Path: "example.com/ws/app"},
		{Dir: "lib", Path: "example.com/ws/lib"},
	}
	if len(ws.Modules) != len(want) {
		t.Fatalf("Modules = %+v, want %+v", ws.Modules, want)
	}
	for i, module := range ws.Modules {
		if module != want[i] {
			t.Errorf("Modules[%d] = %+v, want %+v", i, module, want[i])
		}
	}
}

// TestDetectWorkspaceReadsTheFixtureTheRefusalWasWrittenFor points the reader
// at a real tree rather than a constructed one.
//
// testdata/workspace is the workspace the loader tests use, and everything
// written above is worth exactly as much as its answer here: two modules, the
// directories the file names, and the module paths their go.mod files declare.
// A detector that agreed with its own fixtures and disagreed with this one
// would be a detector that agreed with itself.
func TestDetectWorkspaceReadsTheFixtureTheRefusalWasWrittenFor(t *testing.T) {
	t.Parallel()

	root := fixture(t, "workspace")
	ws, err := DetectWorkspace(root)
	if err != nil {
		t.Fatalf("DetectWorkspace: %v", err)
	}
	want := []WorkspaceModule{
		{Dir: "first", Path: "example.com/first"},
		{Dir: "second", Path: "example.com/second"},
	}
	if len(ws.Modules) != len(want) {
		t.Fatalf("Modules = %+v, want %+v", ws.Modules, want)
	}
	for i, module := range ws.Modules {
		if module != want[i] {
			t.Errorf("Modules[%d] = %+v, want %+v", i, module, want[i])
		}
	}
	// And the refusal the pipeline still makes is the same refusal, reached
	// through the same read: a workspace this build cannot yet measure.
	if CodeOf(CheckWorkspace(root)) != CodeWorkspace {
		t.Errorf("CheckWorkspace no longer refuses a workspace it can read")
	}
}

// TestDetectWorkspaceOrdersTheModulesByDirectory keeps the answer the same
// whichever order the file names them.
//
// The order is not a presentation detail. It decides which module's baseline
// runs first and which module a partial run got through, and a run whose order
// came from the order somebody happened to type `use` lines in would reorder
// itself on an unrelated edit.
func TestDetectWorkspaceOrdersTheModulesByDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeWorkspace(t, root, "go 1.26\n\nuse ./zeta\n\nuse ./alpha\n")
	writeModuleAt(t, filepath.Join(root, "zeta"), "example.com/ws/zeta")
	writeModuleAt(t, filepath.Join(root, "alpha"), "example.com/ws/alpha")

	ws, err := DetectWorkspace(root)
	if err != nil {
		t.Fatalf("DetectWorkspace: %v", err)
	}
	var dirs []string
	for _, module := range ws.Modules {
		dirs = append(dirs, module.Dir)
	}
	if strings.Join(dirs, " ") != "alpha zeta" {
		t.Errorf("directories = %v, want [alpha zeta]", dirs)
	}
}

// TestDetectWorkspaceAcceptsTheRootItself pins the one-module workspace.
//
// `use .` is legal and not pointless: it is what a repository with one module
// and a `go.work` for its tooling looks like. Refusing it would refuse a tree
// that has nothing multi-module about it.
func TestDetectWorkspaceAcceptsTheRootItself(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeWorkspace(t, root, "go 1.26\n\nuse .\n")
	writeModuleAt(t, root, "example.com/only")

	ws, err := DetectWorkspace(root)
	if err != nil {
		t.Fatalf("DetectWorkspace: %v", err)
	}
	if len(ws.Modules) != 1 || ws.Modules[0].Dir != "." || ws.Modules[0].Path != "example.com/only" {
		t.Errorf("Modules = %+v, want one module `.` at example.com/only", ws.Modules)
	}
}

// TestDetectWorkspaceSaysADirectoryIsAModule is the negative of all of it: a
// directory with no go.work is not a workspace, and saying so is not an error.
func TestDetectWorkspaceSaysADirectoryIsAModule(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeModuleAt(t, root, "example.com/plain")

	ws, err := DetectWorkspace(root)
	if err != nil {
		t.Fatalf("DetectWorkspace on a module: %v", err)
	}
	if ws != nil {
		t.Errorf("a module was read as the workspace %+v", ws)
	}
}

// TestDetectWorkspaceRefusesWhatItCannotMeasure is the whole refusal surface in
// one table, and every row is a tree go-mutants must not proceed on.
//
// They divide into three kinds, and the division is why they are refusals
// rather than warnings. A file that does not parse, or names no module, leaves
// nothing to measure. A `use` that does not resolve to a module leaves a
// module path missing from the identity table, and a mutant with no module
// path is a mutant minted under the single-module recipe -- silently sharing
// an identity with a mutant of another module. And anything reaching outside
// the root reaches outside the snapshot, which is the one thing the whole run
// is keyed on.
func TestDetectWorkspaceRefusesWhatItCannotMeasure(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		build func(t *testing.T, root string)
		says  string
	}{
		{
			name: "a file the go command cannot parse",
			build: func(t *testing.T, root string) {
				writeWorkspace(t, root, "go 1.26\n\nuse (\n\t./app\n")
			},
			says: "go.work",
		},
		{
			name: "a file that uses nothing",
			build: func(t *testing.T, root string) {
				writeWorkspace(t, root, "go 1.26\n\nuse ()\n")
			},
			says: "no module",
		},
		{
			name: "a use that points at nothing",
			build: func(t *testing.T, root string) {
				writeWorkspace(t, root, "go 1.26\n\nuse ./absent\n")
			},
			says: "absent",
		},
		{
			name: "a use that points at a directory holding no go.mod",
			build: func(t *testing.T, root string) {
				writeWorkspace(t, root, "go 1.26\n\nuse ./plain\n")
				if err := os.MkdirAll(filepath.Join(root, "plain"), 0o755); err != nil {
					t.Fatalf("making the directory: %v", err)
				}
			},
			says: "go.mod",
		},
		{
			name: "a use that points outside the root",
			build: func(t *testing.T, root string) {
				writeWorkspace(t, root, "go 1.26\n\nuse ../sibling\n")
				writeModuleAt(t, filepath.Join(filepath.Dir(root), "sibling"), "example.com/sibling")
			},
			says: "outside",
		},
		{
			name: "a use given as an absolute path",
			build: func(t *testing.T, root string) {
				elsewhere := t.TempDir()
				writeModuleAt(t, elsewhere, "example.com/elsewhere")
				writeWorkspace(t, root, "go 1.26\n\nuse "+filepath.ToSlash(elsewhere)+"\n")
			},
			says: "outside",
		},
		{
			// Two, with a third between them in directory order: the collision
			// is a property of the set and not of two adjacent rows, and a
			// check that compared neighbours would pass this.
			name: "two modules declaring one path",
			build: func(t *testing.T, root string) {
				writeWorkspace(t, root, "go 1.26\n\nuse (\n\t./a\n\t./b\n\t./c\n)\n")
				writeModuleAt(t, filepath.Join(root, "a"), "example.com/twice")
				writeModuleAt(t, filepath.Join(root, "b"), "example.com/once")
				writeModuleAt(t, filepath.Join(root, "c"), "example.com/twice")
			},
			says: "example.com/twice",
		},
		{
			name: "one directory used twice",
			build: func(t *testing.T, root string) {
				writeWorkspace(t, root, "go 1.26\n\nuse (\n\t./app\n\tapp\n)\n")
				writeModuleAt(t, filepath.Join(root, "app"), "example.com/ws/app")
			},
			says: "more than once",
		},
		{
			name: "a module whose go.mod declares no path",
			build: func(t *testing.T, root string) {
				writeWorkspace(t, root, "go 1.26\n\nuse ./app\n")
				dir := filepath.Join(root, "app")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("making the directory: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("go 1.26\n"), 0o644); err != nil {
					t.Fatalf("writing go.mod: %v", err)
				}
			},
			says: "module path",
		},
		{
			name: "a replace reaching outside the root",
			build: func(t *testing.T, root string) {
				writeWorkspace(t, root,
					"go 1.26\n\nuse ./app\n\nreplace example.com/dep => ../dep\n")
				writeModuleAt(t, filepath.Join(root, "app"), "example.com/ws/app")
			},
			says: "outside",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			tc.build(t, root)
			ws, err := DetectWorkspace(root)
			if err == nil {
				t.Fatalf("DetectWorkspace accepted it as %+v", ws)
			}
			if ws != nil {
				t.Errorf("a refusal also returned a workspace: %+v", ws)
			}
			if CodeOf(err) != CodeWorkspace {
				t.Errorf("code = %q, want %s (err %v)", CodeOf(err), CodeWorkspace, err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the message does not say %q: %v", tc.says, err)
			}
		})
	}
}

// TestDetectWorkspaceAcceptsAReplaceThatStaysInside is the other side of the
// escaping-replace refusal, and it is what keeps that refusal from being a ban
// on `replace`.
//
// A workspace that redirects a dependency at a directory it holds is
// self-contained: the snapshot carries the replacement, and the build inside
// it resolves exactly as the build outside did. So is a replacement by module
// version, which names no directory at all.
func TestDetectWorkspaceAcceptsAReplaceThatStaysInside(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeWorkspace(t, root, "go 1.26\n\nuse ./app\n\n"+
		"replace example.com/dep => ./vendored/dep\n"+
		"replace example.com/other => example.com/other/v2 v2.1.0\n")
	writeModuleAt(t, filepath.Join(root, "app"), "example.com/ws/app")
	writeModuleAt(t, filepath.Join(root, "vendored", "dep"), "example.com/dep")

	ws, err := DetectWorkspace(root)
	if err != nil {
		t.Fatalf("DetectWorkspace: %v", err)
	}
	if len(ws.Modules) != 1 || ws.Modules[0].Dir != "app" {
		t.Errorf("Modules = %+v, want the one used module", ws.Modules)
	}
}

// writeWorkspace puts a go.work at the root of a tree under test.
func writeWorkspace(t *testing.T, root, body string) {
	t.Helper()

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("making %s: %v", root, err)
	}
	if err := os.WriteFile(filepath.Join(root, WorkspaceFile), []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", WorkspaceFile, err)
	}
}

// writeModuleAt puts the smallest go.mod that declares a module path at dir.
func writeModuleAt(t *testing.T, dir, path string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("making %s: %v", dir, err)
	}
	body := "module " + path + "\n\ngo 1.26\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
}
