//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/testkit"
)

func TestGitMetadataCannotSelectAnInheritedRepository(t *testing.T) {
	foreign := testkit.NewRepo(t).File("foreign.txt", "foreign").Git().Root()
	root := testkit.NewRepo(t).File("selected.txt", "selected").Git().Root()
	t.Setenv("GIT_DIR", filepath.Join(foreign, ".git"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(foreign, ".git"))
	output, err := gitOutputBytes(t.Context(), root, "rev-parse", "--show-toplevel")
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
