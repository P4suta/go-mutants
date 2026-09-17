// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

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
	if CodeOf(CheckWorkspace(root)) != CodeWorkspace {
		t.Errorf("CheckWorkspace no longer refuses a workspace it can read")
	}
}

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

func writeWorkspace(t *testing.T, root, body string) {
	t.Helper()

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("making %s: %v", root, err)
	}
	if err := os.WriteFile(filepath.Join(root, WorkspaceFile), []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", WorkspaceFile, err)
	}
}

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

func TestDiscoverWorkspaceMeasuresEveryModuleUnderItsOwnPath(t *testing.T) {
	root := fixture(t, "workspace")
	results, err := DiscoverWorkspace(context.Background(), Options{
		SnapshotRoot: root,
		Toolchain:    toolchain(t),
	})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("DiscoverWorkspace returned %d modules, want the two the go.work uses", len(results))
	}
	want := []WorkspaceModule{
		{Dir: "first", Path: "example.com/first"},
		{Dir: "second", Path: "example.com/second"},
	}
	for i, got := range results {
		if got.Module != want[i] {
			t.Errorf("module %d is %+v, want %+v", i, got.Module, want[i])
		}
		if got.Result.ModulePath != want[i].Path {
			t.Errorf("module %d discovered %q, want %q", i, got.Result.ModulePath, want[i].Path)
		}
		if len(got.Result.Candidates) == 0 {
			t.Errorf("%s produced no candidate; for `first` that means the workspace file was not obeyed",
				want[i].Path)
		}
		for _, candidate := range got.Result.Candidates {
			if candidate.ModulePath != want[i].Path {
				t.Errorf("a candidate of %s is stamped %q", want[i].Path, candidate.ModulePath)
			}
			if strings.HasPrefix(candidate.Path, want[i].Dir+"/") {
				t.Errorf("%s is workspace-relative; a candidate's path is relative to its own module",
					candidate.Path)
			}
		}
	}
	builder := mutation.NewBuilder()
	for _, result := range results {
		for _, located := range result.Result.Candidates {
			if addErr := builder.Add(located.Candidate); addErr != nil {
				t.Fatalf("cataloguing %s: %v", located.Where(), addErr)
			}
		}
	}
	catalog, err := builder.Build()
	if err != nil {
		t.Fatalf("building a catalogue of both modules: %v", err)
	}
	if catalog.Empty() {
		t.Fatal("the catalogue of both modules is empty")
	}
}

func TestDiscoverWorkspaceRefusesADirectoryThatIsNotOne(t *testing.T) {
	t.Parallel()

	results, err := DiscoverWorkspace(context.Background(), Options{
		SnapshotRoot: fixture(t, "mainmod"),
		Toolchain:    toolchain(t),
	})
	if err == nil {
		t.Fatalf("DiscoverWorkspace on a module returned %d results", len(results))
	}
	if CodeOf(err) != CodeSnapshotRoot {
		t.Errorf("code = %q, want %s (err %v)", CodeOf(err), CodeSnapshotRoot, err)
	}
	if !strings.Contains(err.Error(), WorkspaceFile) {
		t.Errorf("the message does not name %s: %v", WorkspaceFile, err)
	}
}

func TestAWorkspacePatternIsWrittenAgainstTheWorkspaceRoot(t *testing.T) {
	root := fixture(t, "workspace")
	results, err := DiscoverWorkspace(context.Background(), Options{
		SnapshotRoot: root,
		Toolchain:    toolchain(t),
		Include:      patterns(t, "second/*.go"),
	})
	if err != nil {
		t.Fatalf("DiscoverWorkspace: %v", err)
	}
	for _, result := range results {
		switch result.Module.Dir {
		case "second":
			if len(result.Result.Candidates) == 0 {
				t.Errorf("`second/*.go` selected nothing in %s", result.Module.Path)
			}
		default:
			if len(result.Result.Candidates) != 0 {
				t.Errorf("`second/*.go` selected %d candidates in %s",
					len(result.Result.Candidates), result.Module.Path)
			}
		}
	}
}

func TestAWorkspaceFileThatCannotBeReadIsNotAMissingOne(t *testing.T) {
	t.Parallel()

	t.Run("no workspace file at all", func(t *testing.T) {
		t.Parallel()

		workspace, err := DetectWorkspace(t.TempDir())
		if err != nil {
			t.Fatalf("DetectWorkspace over a directory with no %s: %v", WorkspaceFile, err)
		}
		if workspace != nil {
			t.Errorf("DetectWorkspace = %+v, want nothing", workspace)
		}
	})

	t.Run("a workspace file that cannot be read", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, WorkspaceFile), 0o755); err != nil {
			t.Fatalf("making a directory in the workspace file's place: %v", err)
		}

		_, err := DetectWorkspace(root)
		if err == nil {
			t.Fatal("DetectWorkspace over an unreadable workspace file succeeded, want a refusal")
		}
		if code := CodeOf(err); code != CodeWorkspace {
			t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodeWorkspace)
		}
		if !strings.Contains(err.Error(), "could not be read") {
			t.Errorf("the refusal %q does not say what went wrong", err)
		}

		if err := CheckWorkspace(root); CodeOf(err) != CodeWorkspace {
			t.Errorf("CheckWorkspace = %v, want the workspace refusal", err)
		}
		if _, err := DiscoverWorkspace(t.Context(), Options{SnapshotRoot: root}); CodeOf(err) != CodeWorkspace {
			t.Errorf("DiscoverWorkspace = %v, want the workspace refusal", err)
		}
	})

	t.Run("a module file that cannot be read", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeWorkspace(t, root, "go 1.26\n\nuse ./app\n")
		if err := os.MkdirAll(filepath.Join(root, "app", "go.mod"), 0o755); err != nil {
			t.Fatalf("making a directory in the module file's place: %v", err)
		}

		_, err := DetectWorkspace(root)
		if err == nil {
			t.Fatal("DetectWorkspace over an unreadable go.mod succeeded, want a refusal")
		}
		if !strings.Contains(err.Error(), "could not be read") {
			t.Errorf("the refusal %q does not say the file could not be read", err)
		}
	})

	t.Run("a module file that declares no module path", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeWorkspace(t, root, "go 1.26\n\nuse ./app\n")
		if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
			t.Fatalf("making the module directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "app", "go.mod"), []byte("go 1.26\n"), 0o644); err != nil {
			t.Fatalf("writing go.mod: %v", err)
		}

		_, err := DetectWorkspace(root)
		if err == nil {
			t.Fatal("DetectWorkspace over a go.mod with no module line succeeded, want a refusal")
		}
		if !strings.Contains(err.Error(), "declares no module path") {
			t.Errorf("the refusal %q does not say the file declares no module path", err)
		}
	})
}

func TestAUseLineIsResolvedAgainstTheRootItWasGiven(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name     string
		root     string
		declared string
		want     string
		fails    bool
	}{
		{name: "a relative use", root: "/work", declared: "./app", want: "app"},
		{name: "a nested use", root: "/work", declared: "./one/two", want: "one/two"},
		{name: "the root itself", root: "/work", declared: ".", want: "."},
		{name: "a use that climbs out", root: "/work", declared: "../sibling", fails: true},
		{name: "an absolute use inside the root", root: "/work", declared: "/work/app", want: "app"},
		{name: "an absolute use outside the root", root: "/work", declared: "/elsewhere", fails: true},
		{name: "an absolute use against a relative root", root: "work", declared: "/elsewhere", fails: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if runtime.GOOS == "windows" {
				t.Skip("the paths in this table are POSIX ones; the rule they state is not platform-specific")
			}
			got, err := containedIn(c.root, c.declared)
			if c.fails {
				if err == nil {
					t.Fatalf("containedIn(%q, %q) = %q, want a refusal", c.root, c.declared, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("containedIn(%q, %q): %v", c.root, c.declared, err)
			}
			if got != c.want {
				t.Errorf("containedIn(%q, %q) = %q, want %q", c.root, c.declared, got, c.want)
			}
		})
	}
}

func TestAWorkspaceRunCarriesOneModulesRefusalOut(t *testing.T) {
	t.Parallel()

	t.Run("a root that is not there", func(t *testing.T) {
		t.Parallel()

		_, err := DiscoverWorkspace(t.Context(), Options{SnapshotRoot: filepath.Join(t.TempDir(), "absent")})
		if code := CodeOf(err); code != CodeSnapshotRoot {
			t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodeSnapshotRoot)
		}
	})

	t.Run("a module that is itself a workspace", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeWorkspace(t, root, "go 1.26\n\nuse ./app\n")
		app := filepath.Join(root, "app")
		writeModuleAt(t, app, "example.com/app")
		writeWorkspace(t, app, "go 1.26\n\nuse .\n")

		_, err := DiscoverWorkspace(t.Context(), Options{SnapshotRoot: root})
		if code := CodeOf(err); code != CodeWorkspace {
			t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodeWorkspace)
		}
		if !strings.Contains(err.Error(), filepath.Join("app", WorkspaceFile)) {
			t.Errorf("the refusal %q does not name the module it is about", err)
		}
	})
}
