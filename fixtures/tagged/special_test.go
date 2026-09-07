// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build special

package tagged

import "testing"

// TestSpecial kills the tagged candidate, and carries the same constraint as
// the file it is about: a test compiled without its subject is a build error,
// and the whole point of the fixture is that both halves come and go together.
func TestSpecial(t *testing.T) {
	if Special() {
		t.Error("Special() = true, want false")
	}
}
