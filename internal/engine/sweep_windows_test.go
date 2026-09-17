// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func obstructRemoval(t *testing.T, _, orphan string) {
	t.Helper()
	held, err := os.Create(filepath.Join(orphan, "held-open"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
}
