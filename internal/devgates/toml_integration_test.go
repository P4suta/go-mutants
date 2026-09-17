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

var tomlGates = map[string]string{
	"fmt":  "taplo fmt --check ",
	"lint": "taplo check ",
}

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

func tomlGateArguments(t testing.TB, root, task, prefix string) []string {
	t.Helper()
	text := readFile(t, filepath.Join(root, "mise.toml"))
	var file struct {
		Tasks map[string]struct {
			Run        any `toml:"run"`
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

type panicsOnFatal struct{ testing.TB }

func (panicsOnFatal) Fatalf(format string, args ...any) { panic(format) }

func (panicsOnFatal) Helper() {}
