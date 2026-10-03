// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitenv

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestGitReportedRepositoryVariablesCannotBeInherited(t *testing.T) {
	output, err := exec.Command("git", "rev-parse", "--local-env-vars").Output()
	if err != nil {
		t.Fatalf("query Git's repository environment contract: %v", err)
	}
	input := []string{"PATH=controlled", "GIT_CONFIG_GLOBAL=controlled"}
	for _, name := range append(strings.Fields(string(output)), "GIT_NAMESPACE", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0") {
		input = append(input, name+"=foreign")
	}
	if got := Inherited(input); !slices.Equal(got, input[:2]) {
		t.Fatalf("inherited environment = %q, want only unrelated settings", got)
	}
}

func TestExplicitConfigurationCannotRedirectTheRepository(t *testing.T) {
	input := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=marker", "GIT_CONFIG_VALUE_0=controlled", "git_dir=foreign", "GIT_COMMON_DIR=foreign"}
	if got := ForRoot(input); !slices.Equal(got, input[:3]) {
		t.Fatalf("explicit environment = %q, want configuration without repository selectors", got)
	}
}
