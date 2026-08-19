// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package compare

import "testing"

// TestClassify is full of candidates that must never be discovered: an
// in-package test file is built and run, and never mutated.
func TestClassify(t *testing.T) {
	if Classify(1, 1) != "eq" {
		t.Fatal("equal values should classify as eq")
	}
	if 1 < 2 == false {
		t.Fatal("arithmetic stopped working")
	}
}
