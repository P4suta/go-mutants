// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package engine

import (
	"os"
	"syscall"
	"testing"
)

// obstructRemoval takes the write permission off parent, so that nothing can
// unlink an entry of it: the sweep empties the orphan and then cannot remove
// the directory itself. Root is not bound by a mode, so the test is skipped
// for root rather than made to pass by accident.
func obstructRemoval(t *testing.T, parent, _ string) {
	t.Helper()
	if syscall.Geteuid() == 0 {
		t.Skip("root removes a directory whatever its parent's mode says")
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("Chmod(%s): %v", parent, err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
}
