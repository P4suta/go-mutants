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

// TestANarrowedMutantIsKilledDirectlyOrByConfirmation is narrowing against a
// real binary: the same mutant, the same binary, and two selections. Narrowed
// to the test that reaches the mutated line, it is killed by that test alone
// and the attempt names it. Narrowed to a test that does *not* reach the line,
// the narrowed run survives — but RunOne confirms a narrowed survivor against
// the whole binary, where the covering test kills it, so it is killed all the
// same and the attempt names no test because it ran the whole binary. The
// second half is the soundness [RunOne]'s confirmation exists for: narrowing
// to the wrong tests cannot turn a kill into a survivor.
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
	// The `>` of Positive, which TestPositive's zero row is the only thing
	// that tells from `>=`.
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
		name string
		test string
		// wantTests is what the returned attempt names: the covering test when
		// it killed directly, and none when the whole-binary confirmation did.
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
