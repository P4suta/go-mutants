// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package cgopkg_test

import (
	"testing"

	"example.com/mini/cgopkg"
)

// TestSame exists so that the cgo package has test variants: an external test
// package holds no non-test file to recognise the cgo import in, so it is the
// variant that proves the gate's cgo exemption covers a whole package and not
// just the one variant that happens to own the file.
func TestSame(t *testing.T) {
	if !cgopkg.Same(1, 1) {
		t.Fatal("equal values should compare equal")
	}
}
