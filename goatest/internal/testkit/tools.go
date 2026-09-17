// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const RequireToolsEnv = "GOATEST_TEST_REQUIRE_TOOLS"

var pinnedRequireTools = os.Getenv(RequireToolsEnv)

func RequireTools() bool {
	if value, ok := os.LookupEnv(RequireToolsEnv); ok {
		return truthy(value)
	}
	return truthy(pinnedRequireTools)
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

func toolPath(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err == nil {
		return path
	}
	if RequireTools() {
		t.Fatalf("%s is not on PATH and %s is set, so this test may not be skipped: %v",
			name, RequireToolsEnv, err)
	} else {
		t.Skipf("%s is not on PATH, so this test cannot run here (set %s=1 to make this a failure): %v",
			name, RequireToolsEnv, err)
	}
	return ""
}
