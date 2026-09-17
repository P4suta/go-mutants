// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"os"
	"strings"
	"testing"
)

func HelperArgv(testName string) []string {
	return []string{os.Args[0], "-test.run=^" + testName + "$"}
}

func HelperEnabled(variable string) bool {
	return os.Getenv(variable) == "1"
}

func RunningAsHelper(t *testing.T, variable, testName string) bool {
	t.Helper()
	return runningAsHelperWith(t, variable, testName)
}

func runningAsHelperWith(t testing.TB, variable, testName string) bool {
	t.Helper()
	if HelperEnabled(variable) {
		return true
	}
	if value, set := os.LookupEnv(variable); set && value != "" && value != "0" {
		t.Fatalf("%s is %q, which is neither the 1 that selects this helper nor an absence that does not",
			variable, value)
	}
	argv := HelperArgv(testName)
	if len(argv) != helperArgvLength || !strings.Contains(argv[1], testName) {
		t.Fatalf("HelperArgv(%q) = %q, which would not select this test", testName, argv)
	}
	return false
}

const helperArgvLength = 2
