// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"slices"
	"strings"
	"testing"
)

func TestGitEnvironmentExcludesLegacyRepositorySelectors(t *testing.T) {
	for _, name := range []string{"GIT_INTERNAL_SUPER_PREFIX", "git_internal_super_prefix"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "foreign")
			t.Setenv("GOATEST_GIT_ENV_CONTROL", "keep")
			environment := GitEnvironment()
			for _, entry := range environment {
				key, _, _ := strings.Cut(entry, "=")
				if strings.EqualFold(key, name) {
					t.Fatalf("fixture environment inherited repository selector %q", entry)
				}
			}
			if !slices.Contains(environment, "GOATEST_GIT_ENV_CONTROL=keep") {
				t.Fatal("fixture environment removed an unrelated setting")
			}
		})
	}
}
