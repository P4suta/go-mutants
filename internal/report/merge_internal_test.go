// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

import "testing"

// TestMergedCoverageTakesTheFinestNarrowing: the merged rows carry whatever the
// finest shard wrote, so the merged block has to be the mode that may state it
// — test over package over off — and the largest of each count, since the
// shards profiled the same set.
func TestMergedCoverageTakesTheFinestNarrowing(t *testing.T) {
	t.Parallel()

	count := func(n int) *int { return &n }
	shards := []*Report{
		{Coverage: Coverage{Mode: CoverageOff}},
		{Coverage: Coverage{Mode: CoverageTest, Binaries: count(2), Tests: count(7), MutantsUncovered: count(0)}},
		{Coverage: Coverage{Mode: CoveragePackage, Binaries: count(3), MutantsUncovered: count(1)}},
	}
	got, err := mergedCoverage(shards, nil)
	if err != nil {
		t.Fatalf("mergedCoverage: %v", err)
	}
	if got.Mode != CoverageTest {
		t.Errorf("mode = %q, want %q", got.Mode, CoverageTest)
	}
	if got.Binaries == nil || *got.Binaries != 3 {
		t.Errorf("binaries = %v, want the largest, 3", got.Binaries)
	}
	if got.Tests == nil || *got.Tests != 7 {
		t.Errorf("tests = %v, want the largest, 7", got.Tests)
	}

	for _, tc := range []struct {
		a, b CoverageMode
		want bool
	}{
		{CoveragePackage, CoverageOff, true},
		{CoverageTest, CoveragePackage, true},
		{CoverageOff, CoverageTest, false},
		{CoveragePackage, CoveragePackage, false},
	} {
		if got := finer(tc.a, tc.b); got != tc.want {
			t.Errorf("finer(%s, %s) = %t, want %t", tc.a, tc.b, got, tc.want)
		}
	}
}
