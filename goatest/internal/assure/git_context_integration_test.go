//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/testkit"
)

func TestChangedFilesCannotSelectAnInheritedRepository(t *testing.T) {
	binary := testkit.GitBinary(t)
	repository := func(name string) string {
		t.Helper()
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		env := []string{
			"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot"),
			"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull,
			"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
			"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
		}
		for _, args := range [][]string{{"init", "--quiet"}, {"add", "--all"}, {"commit", "--quiet", "--message", name}} {
			command := exec.CommandContext(t.Context(), binary, args...)
			command.Dir, command.Env = root, env
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("controlled Git fixture: %v\n%s", err, output)
			}
		}
		return root
	}
	foreign := repository("foreign.txt")
	root := repository("selected.txt")
	t.Setenv("GIT_DIR", filepath.Join(foreign, ".git"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(foreign, ".git"))
	output, err := gitNamesOutput(t.Context(), root, []string{"rev-parse", "--show-toplevel"})
	if err != nil {
		t.Fatalf("read the selected repository: %v", err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(strings.TrimSpace(string(output))) != filepath.Clean(want) {
		t.Fatal("Git did not select the explicit root")
	}
}
