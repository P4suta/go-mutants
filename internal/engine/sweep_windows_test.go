// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// obstructRemoval holds a file inside the orphan open for the rest of the
// test. Windows refuses to delete a file any process has open, and the
// directory above it with it, whoever asks.
func obstructRemoval(t *testing.T, _, orphan string) {
	t.Helper()
	held, err := os.Create(filepath.Join(orphan, "held-open"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
}
