// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// The order [Discover] refuses things in, and the fact that each refusal is the
// run's answer rather than a note on the side.
//
// The cheap checks come first on purpose: a snapshot root that is not there, a
// workspace file where a module was expected, and an operator name that names
// nothing are all answerable before a toolchain is located or a package loaded,
// and learning about one of them after several minutes of loading would be a
// poor way to find out. Each of them is carried out of Discover unchanged,
// because a discovery that returned a partial catalogue beside an error would
// be a catalogue of a scope nobody asked for.
//
// None of these needs a toolchain, which is why they are here rather than in
// the fixture-driven suite: every one of them is reached before the loader runs.

// TestDiscoverRefusesBeforeItLoadsAnything is the prefix of the pipeline that
// costs nothing.
func TestDiscoverRefusesBeforeItLoadsAnything(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		opts func(t *testing.T) Options
		code Code
		says string
	}{
		{
			name: "no snapshot root",
			opts: func(*testing.T) Options { return Options{} },
			code: CodeSnapshotRoot,
			says: "no snapshot root was given",
		},
		{
			name: "a snapshot root that is not there",
			opts: func(t *testing.T) Options {
				return Options{SnapshotRoot: filepath.Join(t.TempDir(), "absent")}
			},
			code: CodeSnapshotRoot,
			says: "cannot read",
		},
		{
			name: "a snapshot root that is a file",
			opts: func(t *testing.T) Options {
				root := t.TempDir()
				path := filepath.Join(root, "go.mod")
				if err := os.WriteFile(path, []byte("module example.com/m\n\ngo 1.26\n"), 0o644); err != nil {
					t.Fatalf("writing go.mod: %v", err)
				}
				return Options{SnapshotRoot: path}
			},
			code: CodeSnapshotRoot,
			says: "is not a directory",
		},
		{
			name: "a workspace where a module was expected",
			opts: func(t *testing.T) Options {
				root := t.TempDir()
				writeWorkspace(t, root, "go 1.26\n\nuse ./app\n")
				writeModuleAt(t, filepath.Join(root, "app"), "example.com/app")
				return Options{SnapshotRoot: root}
			},
			code: CodeWorkspace,
			says: "multi-module",
		},
		{
			name: "a workspace file that cannot be read",
			opts: func(t *testing.T) Options {
				root := t.TempDir()
				if err := os.Mkdir(filepath.Join(root, WorkspaceFile), 0o755); err != nil {
					t.Fatalf("making a directory in the workspace file's place: %v", err)
				}
				return Options{SnapshotRoot: root}
			},
			code: CodeWorkspace,
			says: "could not be read",
		},
		{
			name: "an operator that names nothing",
			opts: func(t *testing.T) Options {
				root := t.TempDir()
				return Options{SnapshotRoot: root, Rules: []mutation.Rule{{Name: "no-such-rule"}}}
			},
			code: CodeUnknownRule,
			says: "no-such-rule",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			result, err := Discover(t.Context(), c.opts(t))
			if err == nil {
				t.Fatalf("Discover = %+v, want a refusal", result)
			}
			if code := CodeOf(err); code != c.code {
				t.Fatalf("CodeOf(%v) = %q, want %q", err, code, c.code)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal %q does not say %q", err, c.says)
			}
			if len(result.Candidates) != 0 || len(result.Skips) != 0 {
				t.Errorf("the refusal carries %d candidates and %d skips", len(result.Candidates), len(result.Skips))
			}
		})
	}
}
