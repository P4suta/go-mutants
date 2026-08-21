// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

import (
	"slices"
	"testing"
)

// TestRecord pins what was written as well as how much of it.
//
// Comparing the lines rather than counting them is what kills all three of the
// deletion family's mutants at once: a deleted append, a deleted call, and a
// returned `nil` all leave a shorter slice, but a test that only checked the
// length would go on passing against a journal that recorded the wrong strings.
func TestRecord(t *testing.T) {
	want := []string{"1", "2", "3"}
	if got := Record([]int{1, 2, 3}); !slices.Equal(got, want) {
		t.Errorf("Record([1 2 3]) = %q, want %q", got, want)
	}
}
