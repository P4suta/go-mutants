// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage_test

import (
	"strconv"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
)

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

func TestTheTestMappingDoesNotCostAMutantForEveryTest(t *testing.T) {
	opts := costOptions()
	_ = coverage.MapTests(opts)

	allocs := testing.AllocsPerRun(3, func() { _ = coverage.MapTests(opts) })
	if bound := float64(4 * costMutants); allocs > bound {
		t.Errorf("mapping %d mutants over %d tests allocated %.0f times, want at most %.0f: "+
			"a cost that grows with the product is one paid once per pair",
			costMutants, costTests, allocs, bound)
	}
}
