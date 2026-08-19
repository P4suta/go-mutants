// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package compare_test

import (
	"testing"

	"example.com/mini/compare"
)

// TestFlags is the external test package: loaded and type-checked, never
// mutated.
func TestFlags(t *testing.T) {
	on, off := compare.Flags()
	if on == false || off == true {
		t.Fatal("unexpected flags")
	}
}
