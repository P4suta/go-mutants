// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package brokentests

import "testing"

// TestCount does not compile: Count returns an int and is compared with a
// string. The failure is a test file's alone, which is the point of the
// fixture.
func TestCount(t *testing.T) {
	if Count(1) != "two" {
		t.Fatal("no")
	}
}
