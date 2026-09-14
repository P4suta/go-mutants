// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package split

import "testing"

// TestWidest kills the mutant that only exists because the import was
// completed. A fixture whose new site is uncovered would prove the site was
// catalogued and nothing about whether it compiles and runs.
func TestWidest(t *testing.T) {
	if got := Widest(2, 3); got != 2 {
		t.Errorf("Widest(2, 3) = %d, want 2", got)
	}
}

// TestWidestOffTheCase reaches the other branch, so that the whole of the file
// the completion is written into is measured rather than half of it.
func TestWidestOffTheCase(t *testing.T) {
	if got := Widest(1, 1); got != 1 {
		t.Errorf("Widest(1, 1) = %d, want 1", got)
	}
}

// TestTotal covers the file that does the importing.
func TestTotal(t *testing.T) {
	if got := Total(2, 3); got != 5 {
		t.Errorf("Total(2, 3) = %d, want 5", got)
	}
}
