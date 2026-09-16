// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package devgates

import (
	"os"
	"testing"
)

// readFile reads a file a gate is about, and fails rather than returning an
// error nobody up the call reads differently.
func readFile(t testing.TB, path string) string {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(text)
}

// writeFile writes a sample a counterpart test feeds its reader.
func writeFile(t testing.TB, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
