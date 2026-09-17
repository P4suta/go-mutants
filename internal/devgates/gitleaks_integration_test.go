// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package devgates

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/P4suta/go-mutants/internal/testkit"
)

const gitleaksConfig = ".gitleaks.toml"

var allowlistPath = regexp.MustCompile(`^\(\^\|/\)([A-Za-z0-9._/-]+)/$`)

func TestEveryGitleaksAllowlistPathIsAPathGitIgnores(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	git := testkit.GitBinary(t)
	paths := gitleaksAllowlistPaths(t, root)
	if len(paths) == 0 {
		t.Fatal("the allowlist is empty, and the reader of this file was told it was not")
	}

	for _, raw := range paths {
		dir := allowlistDir(t, raw)
		probe := filepath.Join(dir, "probe.jsonl")
		cmd := exec.Command(git, "check-ignore", "--quiet", "--no-index", probe)
		cmd.Dir = root
		if err := cmd.Run(); err != nil {
			t.Errorf("%s excuses %q, and git does not ignore %q: %v", gitleaksConfig, raw, probe, err)
		}
	}
}

func TestNoGitleaksAllowlistPathCoversAFileGitTracks(t *testing.T) {
	t.Parallel()

	root := testkit.Root(t)
	git := testkit.GitBinary(t)
	cmd := exec.Command(git, "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing the tracked files: %v", err)
	}
	tracked := strings.FieldsFunc(string(out), func(r rune) bool { return r == 0 })
	if len(tracked) == 0 {
		t.Fatal("git tracks nothing here, so this test proved nothing rather than passing")
	}

	for _, raw := range gitleaksAllowlistPaths(t, root) {
		pattern, err := regexp.Compile(raw)
		if err != nil {
			t.Errorf("%s holds %q, which is not a regular expression: %v", gitleaksConfig, raw, err)
			continue
		}
		for _, path := range tracked {
			if pattern.MatchString(path) {
				t.Errorf("%s excuses %q, which covers the tracked file %s", gitleaksConfig, raw, path)
			}
		}
	}
}

func TestTheAllowlistReaderSeesAPathTheConfigDoesNotHold(t *testing.T) {
	t.Parallel()

	const written = `
[extend]
useDefault = true

[[allowlists]]
description = "a directory this repository does not have"
paths = [
  '''(^|/)no-such-directory/''',
]
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, gitleaksConfig), []byte(written), 0o600); err != nil {
		t.Fatalf("writing the sample config: %v", err)
	}
	got := gitleaksAllowlistPaths(t, dir)
	if len(got) != 1 || got[0] != `(^|/)no-such-directory/` {
		t.Fatalf("the reader found %q in a config holding one path", got)
	}
	if dir := allowlistDir(t, got[0]); dir != "no-such-directory" {
		t.Errorf("the path %q names the directory %q", got[0], dir)
	}
}

func gitleaksAllowlistPaths(t testing.TB, root string) []string {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(root, gitleaksConfig))
	if err != nil {
		t.Fatalf("reading %s: %v", gitleaksConfig, err)
	}
	var config struct {
		Allowlists []struct {
			Paths []string `toml:"paths"`
		} `toml:"allowlists"`
	}
	if err := toml.Unmarshal(text, &config); err != nil {
		t.Fatalf("decoding %s: %v", gitleaksConfig, err)
	}
	var paths []string
	for _, list := range config.Allowlists {
		paths = append(paths, list.Paths...)
	}
	return paths
}

func allowlistDir(t testing.TB, raw string) string {
	t.Helper()
	m := allowlistPath.FindStringSubmatch(raw)
	if m == nil {
		t.Fatalf("%s holds %q, and an allowlisted path is a directory written as (^|/)dir/", gitleaksConfig, raw)
	}
	return m[1]
}
