// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage_test

import (
	"strconv"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
)

// The shape the cost gate below is measured over: one file, many mutants in
// it, many tests, and a coverage relation that is sparse -- test i reaches
// mutant i and nothing else.
//
// Sparse on purpose. The answer a dense relation produces is itself the size
// of the product, so a run over one would be allocating for its output and a
// bound over it would say nothing about the mapping's own cost. Sparse, the
// output is one entry per mutant, and what is left to count is the work the
// mapping does to decide it.
const (
	costMutants = 200
	costTests   = 50
	costFile    = "example.com/m/internal/alpha/alpha.go"
)

func costOptions() coverage.TestOptions {
	mutants := make([]coverage.Mutant, costMutants)
	for i := range mutants {
		mutants[i] = coverage.Mutant{
			ID:        strconv.Itoa(i),
			Path:      "internal/alpha/alpha.go",
			StartLine: i + 1,
			EndLine:   i + 1,
		}
	}
	profiles := make(map[coverage.TestKey]coverage.Profile, costTests)
	for i := range costTests {
		profiles[coverage.TestKey{ImportPath: "example.com/m/internal/alpha", Name: "Test" + strconv.Itoa(i)}] =
			coverage.Profile{
				Mode: "set",
				Blocks: []coverage.Block{
					{File: costFile, StartLine: i + 1, EndLine: i + 1, NumStmt: 1, Count: 1},
				},
			}
	}
	return coverage.TestOptions{ModulePath: "example.com/m", Mutants: mutants, Profiles: profiles}
}

// TestTheTestMappingDoesNotCostAMutantForEveryTest is the mapping's complexity,
// written as the one thing a test can count exactly.
//
// The mapping asks, for every mutant, which tests reach it, so *something*
// about it is a product and always will be. What must not be is the work of
// deciding where a mutant lives: how a profile spells a mutant's file is a fact
// about the mutant, and finding that file in one test's profile is a fact about
// the file -- neither is a fact about the pair. Asked once per pair they were
// both, and on a real run that is the catalogue times the suite: four thousand
// mutants against six hundred tests is two and a half million strings built and
// thrown away to answer a question two hundred and twenty-nine files' worth of
// lookups already settle.
//
// Allocations rather than a stopwatch, because the point is a count. The bound
// is deliberately loose -- four per mutant against the product's fifty -- so
// that it fails only on the thing it is about and never on an extra slice
// somebody had a reason for.
func TestTheTestMappingDoesNotCostAMutantForEveryTest(t *testing.T) {
	opts := costOptions()
	// Warm whatever the first call builds once, so that the measured runs are
	// the mapping and nothing underneath it.
	_ = coverage.MapTests(opts)

	allocs := testing.AllocsPerRun(3, func() { _ = coverage.MapTests(opts) })
	if bound := float64(4 * costMutants); allocs > bound {
		t.Errorf("mapping %d mutants over %d tests allocated %.0f times, want at most %.0f: "+
			"a cost that grows with the product is one paid once per pair",
			costMutants, costTests, allocs, bound)
	}
}
