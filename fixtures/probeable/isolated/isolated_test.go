// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package isolated

import "testing"

// TestNameIsTheFixturesOwn gives the package a test binary to be probed, and
// asserts the one thing it can: a package with nothing to mutate still has to
// be a package a run builds and runs.
func TestNameIsTheFixturesOwn(t *testing.T) {
	if Name != "isolated" {
		t.Errorf("Name = %q, want %q", Name, "isolated")
	}
}
