// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package devgates

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/P4suta/go-mutants/internal/testkit"
)

// tomlGates are the steps whose arguments this file holds to the tree, and the
// task each lives in.
//
// There are two because taplo is asked two different questions -- `fmt --check`
// for layout, `check` for the schema -- in two different tasks, and a file
// added to one list and not the other is checked by one and missed by the
// other, silently and in whichever direction the adder happened to look.
var tomlGates = map[string]string{
	"fmt":  "taplo fmt --check ",
	"lint": "taplo check ",
}

// TestTheTomlGateNamesEveryTomlFileGitTracks pins the schema check's argument
// list to the files it is supposed to be about, in both directions.
//
// `taplo check` with no arguments finds its own files, and finding exactly
// these seven is why the enumeration looks redundant. It is not. Discovery
// there is a filesystem walk, which is the mistake the secret scan was making
// until .gitleaks.toml: a run that drops a TOML file anywhere under the tree
// would put it in front of a schema check nobody asked about it, and the gate
// would go red for something outside the repository. The explicit list is also
// what a reviewer reads to know what is checked.
//
// What the list cannot do is stay right on its own, and it already did not. The
// .gitleaks.toml added beside this test is checked by `taplo fmt`, which
// discovers, and would have been missed by `taplo check`, which enumerates. It
// was caught by remembering, which is the thing that does not scale. So the
// enumeration stays, and this is what keeps it equal to `git ls-files`.
func TestTheTomlGateNamesEveryTomlFileGitTracks(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	tracked := trackedToml(t, root)
	if len(tracked) == 0 {
		t.Fatal("git tracks no TOML file here, so this test proved nothing rather than passing")
	}

	for task, prefix := range tomlGates {
		named := tomlGateArguments(t, root, task, prefix)
		if len(named) == 0 {
			t.Fatalf("the %s step names no TOML file, and the reader of mise.toml sees several", task)
		}
		for _, path := range tracked {
			if !slices.Contains(named, path) {
				t.Errorf("git tracks %s and `%s` in the %s task does not name it",
					path, strings.TrimSpace(prefix), task)
			}
		}
		for _, path := range named {
			if !slices.Contains(tracked, path) {
				t.Errorf("`%s` in the %s task names %s and git does not track it",
					strings.TrimSpace(prefix), task, path)
			}
		}
	}
}

// TestTheTomlGateReaderRefusesATaskWithoutIt is the counterpart: a reader that
// returned an empty list instead of refusing would make both loops above agree
// about nothing and report it as agreement.
func TestTheTomlGateReaderRefusesATaskWithoutIt(t *testing.T) {
	t.Parallel()

	const written = "[tasks.lint]\nrun = [\"gofmt -l .\", \"go vet ./...\"]\n"
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mise.toml"), written)

	refused := make(chan string, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				refused <- "refused"
			}
			close(refused)
		}()
		tomlGateArguments(panicsOnFatal{}, dir, "lint", "taplo check ")
	}()
	if <-refused == "" {
		t.Error("a lint task with no taplo step was read as one that had it")
	}
}

// tomlGateArguments is every path one task's taplo step names.
func tomlGateArguments(t testing.TB, root, task, prefix string) []string {
	t.Helper()
	text := readFile(t, filepath.Join(root, "mise.toml"))
	var file struct {
		Tasks map[string]struct {
			// `run` is a string for a one-step task and a list for the rest,
			// which mise accepts either way. Decoding it as a list gives a type
			// error on the first single-step task in the file rather than a
			// missing step, so the shape is taken as it comes and flattened.
			Run any `toml:"run"`
			// A task may carry a second list for Windows, and the fmt task
			// does. Reading only `run` would let the two drift -- the same
			// defect this file is about, one level down, and visible only on
			// the platform nobody develops on.
			RunWindows any `toml:"run_windows"`
		} `toml:"tasks"`
	}
	if err := toml.Unmarshal([]byte(text), &file); err != nil {
		t.Fatalf("decoding mise.toml: %v", err)
	}
	definition := file.Tasks[task]
	var found [][]string
	for _, run := range []any{definition.Run, definition.RunWindows} {
		if run == nil {
			continue
		}
		for _, step := range taskSteps(run) {
			if rest, ok := strings.CutPrefix(step, prefix); ok {
				found = append(found, strings.Fields(rest))
			}
		}
	}
	if len(found) == 0 {
		t.Fatalf("the %s task has no step beginning %q", task, prefix)
	}
	for _, other := range found[1:] {
		if !slices.Equal(found[0], other) {
			t.Errorf("the %s task runs `%s` over %v on one platform and %v on another",
				task, strings.TrimSpace(prefix), found[0], other)
		}
	}
	return found[0]
}

// taskSteps is the commands of one mise task, whichever shape it was written
// in.
func taskSteps(run any) []string {
	switch v := run.(type) {
	case string:
		return []string{v}
	case []any:
		steps := make([]string, 0, len(v))
		for _, step := range v {
			if s, ok := step.(string); ok {
				steps = append(steps, s)
			}
		}
		return steps
	default:
		return nil
	}
}

// trackedToml is every TOML file git holds or would hold, sorted.
func trackedToml(t testing.TB, root string) []string {
	t.Helper()
	_ = testkit.GitBinary(t)
	out := testkit.Git(t, root, "ls-files", "--cached", "--others", "--exclude-standard", "*.toml")
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	slices.Sort(paths)
	return paths
}

// panicsOnFatal is a testing.TB whose Fatalf panics rather than ends a test, so
// that a counterpart can observe a reader refusing instead of being ended by
// the refusal it is checking for.
//
// The embedded nil is deliberate: every method this reader actually calls is
// overridden below, and a call to any other one is a reader that grew a second
// way to report a problem without this counterpart being told.
type panicsOnFatal struct{ testing.TB }

// Fatalf panics with what the reader refused on.
func (panicsOnFatal) Fatalf(format string, args ...any) { panic(format) }

// Helper is a no-op, because there is no test frame here to mark.
func (panicsOnFatal) Helper() {}
