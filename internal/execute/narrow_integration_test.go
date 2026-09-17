// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package execute_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestANarrowedMutantIsKilledDirectlyOrByConfirmation(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	source := testkit.NewModule(t).Module(perTestModule).
		Source("pertest.go", perTestSource).
		Source("pertest_test.go", perTestSuite).
		Root()
	snap := mutantkit.SnapshotOf(t, source)
	found := mutantkit.DiscoverWith(t, toolchain, snap, env)
	catalog := mutantkit.Catalog(t, found)
	if _, err := instrument.Instrument(instrument.Options{
		SnapshotRoot: snap.Root,
		ModulePath:   found.ModulePath,
		Catalog:      catalog,
		Hints:        mutantkit.Hints(t, found),
	}); err != nil {
		t.Fatalf("instrumenting the snapshot: %v", err)
	}
	mutant := mutantkit.ByRule(t, catalog, "gt-to-ge")

	work := t.TempDir()
	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		BinDir:       filepath.Join(work, "bin"),
		ScratchDir:   filepath.Join(work, "workers"),
		Jobs:         1,
		Timeout:      buildTimeout,
		Env:          env,
	}
	bins, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the test binary: %v\n%s", err, execute.OutputOf(err))
	}

	tests := []struct {
		name      string
		test      string
		wantTests []string
	}{
		{name: "the covering test kills it directly", test: "TestPositive", wantTests: []string{"TestPositive"}},
		{name: "a non-covering test still kills it, through confirmation", test: "TestNegative", wantTests: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempt := execute.RunOne(t.Context(), opts, execute.MutantRun{
				ID:      mutant.ID,
				Timeout: runTimeout,
				Tests:   map[string][]string{perTestModule: {test.test}},
			}, bins)
			if attempt.Outcome != mutation.OutcomeKilled {
				t.Fatalf("narrowed to %s: outcome = %s, want killed (%v)\n%s",
					test.test, attempt.Outcome, attempt.Err, attempt.OutputTail)
			}
			if got := attempt.Tests[perTestModule]; !slices.Equal(got, test.wantTests) {
				t.Errorf("narrowed to %s: attempt names tests %v, want %v", test.test, got, test.wantTests)
			}
		})
	}
}
