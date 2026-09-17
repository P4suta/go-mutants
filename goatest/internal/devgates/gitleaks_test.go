//go:build integration

// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package devgates

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/P4suta/go-mutants/goatest/internal/testkit"
)

const (
	gitleaksConfiguration = ".gitleaks.toml"

	miseManifest = "../mise.toml"

	allowlistedPathPattern = `^\(\^\|/\)((?:[A-Za-z0-9_.-]|\\\.)+)/$`
)

var allowlistedPath = regexp.MustCompile(allowlistedPathPattern)

type gitleaksAllowlists struct {
	Allowlists []struct {
		Description string   `toml:"description"`
		Paths       []string `toml:"paths"`
	} `toml:"allowlists"`
}

func allowlistedDirectories(t *testing.T, root string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, gitleaksConfiguration))
	if err != nil {
		t.Fatalf("read %s: %v", gitleaksConfiguration, err)
	}
	var configuration gitleaksAllowlists
	if err := toml.Unmarshal(data, &configuration); err != nil {
		t.Fatalf("decode %s: %v", gitleaksConfiguration, err)
	}
	directories := make(map[string]string)
	for _, allowlist := range configuration.Allowlists {
		for _, entry := range allowlist.Paths {
			match := allowlistedPath.FindStringSubmatch(entry)
			if match == nil {
				t.Errorf("%s allows path %q, which is not of the form (^|/)name/ this gate can check", gitleaksConfiguration, entry)
				continue
			}
			directories[entry] = strings.ReplaceAll(match[1], `\.`, ".")
		}
	}
	if len(directories) == 0 {
		t.Fatalf("%s yielded no allowlisted path, and the reader of it was told there were some", gitleaksConfiguration)
	}
	return directories
}

func TestEveryScannerAllowlistIsAPathGitIgnores(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for entry, directory := range allowlistedDirectories(t, root) {
		command := exec.CommandContext(t.Context(), testkit.GitBinary(t), "-C", root, "check-ignore", "-q", directory+"/")
		if err := command.Run(); err != nil {
			t.Errorf("%s allows %q, and git does not ignore %s/", gitleaksConfiguration, entry, directory)
		}
	}
}

func TestNoScannerAllowlistCoversAFileThisRepositoryTracks(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	tracked := trackedFiles(t, testkit.GitBinary(t), root)
	for entry := range allowlistedDirectories(t, root) {
		pattern, err := regexp.Compile(entry)
		if err != nil {
			t.Errorf("%s allows %q, which is not a regular expression: %v", gitleaksConfiguration, entry, err)
			continue
		}
		for _, relative := range tracked {
			if pattern.MatchString(relative) {
				t.Errorf("%s allows %q, which covers the tracked file %s", gitleaksConfiguration, entry, relative)
			}
		}
	}
}

func TestTheScannerAllowlistGateSeesAnEntryThatCoversTrackedFiles(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	forged := `(^|/)internal/`
	match := allowlistedPath.FindStringSubmatch(forged)
	if match == nil {
		t.Fatalf("%q is not of the shape the gate accepts, so this test proves nothing about it", forged)
	}
	pattern := regexp.MustCompile(forged)
	var covered int
	for _, relative := range trackedFiles(t, testkit.GitBinary(t), root) {
		if pattern.MatchString(relative) {
			covered++
		}
	}
	if covered == 0 {
		t.Fatal("an entry covering internal/ matched no tracked file, so the check it is meant to fail cannot fail")
	}
}

func TestTheLintTaskUsesTheScannerAllowlist(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, miseManifest))
	if err != nil {
		t.Fatalf("read %s: %v", miseManifest, err)
	}
	var manifest struct {
		Tasks map[string]struct {
			Run any `toml:"run"`
		} `toml:"tasks"`
	}
	if err := toml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode %s: %v", miseManifest, err)
	}
	lint, ok := manifest.Tasks["lint"]
	if !ok {
		t.Fatalf("%s declares no lint task", miseManifest)
	}
	steps := taskSteps(t, lint.Run)
	var found bool
	for _, step := range steps {
		if strings.Contains(step, "gitleaks") && strings.Contains(step, gitleaksConfiguration) {
			found = true
		}
	}
	if !found {
		t.Errorf("the lint task runs %v, none of which hands gitleaks %s", steps, gitleaksConfiguration)
	}
}

func taskSteps(t *testing.T, value any) []string {
	t.Helper()
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		steps := make([]string, 0, len(typed))
		for _, item := range typed {
			step, ok := item.(string)
			if !ok {
				t.Fatalf("a task step is %T rather than a command", item)
			}
			steps = append(steps, step)
		}
		return steps
	default:
		t.Fatalf("a task run is %T rather than a command or a list of them", value)
		return nil
	}
}
