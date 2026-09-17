// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"testing"
)

func TestTruthyReadsTheSpellingsAPersonTypes(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", " ", "0", "0 ", "false", "FALSE", "no", "off", "  Off  "} {
		if truthy(value) {
			t.Errorf("truthy(%q) = true, want false", value)
		}
	}
	for _, value := range []string{"1", "true", "yes", "on", "please", "00"} {
		if !truthy(value) {
			t.Errorf("truthy(%q) = false, want true", value)
		}
	}
}

func TestRequireToolsPrefersTheLiveEnvironment(t *testing.T) {
	t.Setenv(RequireToolsEnv, "1")
	if !RequireTools() {
		t.Fatal("RequireTools() = false with the variable set to 1")
	}
	t.Setenv(RequireToolsEnv, "0")
	if RequireTools() {
		t.Fatal("RequireTools() = true with the variable set to 0")
	}
}

func TestRequireToolsFallsBackToThePinnedValue(t *testing.T) {
	previous := pinnedRequireTools
	t.Cleanup(func() { pinnedRequireTools = previous })

	t.Setenv(RequireToolsEnv, "1")
	if err := os.Unsetenv(RequireToolsEnv); err != nil {
		t.Fatalf("unset %s: %v", RequireToolsEnv, err)
	}

	pinnedRequireTools = "1"
	if !RequireTools() {
		t.Error("RequireTools() = false with the variable absent and pinned to 1")
	}
	pinnedRequireTools = ""
	if RequireTools() {
		t.Error("RequireTools() = true with the variable absent and nothing pinned")
	}
}
