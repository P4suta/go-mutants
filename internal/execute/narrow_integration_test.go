// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package execute_test

import (
	"path/filepath"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// TestAMutantIsDecidedByTheTestsItIsNarrowedTo is narrowing against a real
// binary: the same mutant, the same binary, and two selections — the test
// that reaches the mutated line kills it, and the test that does not lets it
// live. Together they say the selection reached the process: a `-test.run`
// the binary ignored would kill in both cases, and one that selected nothing
// would let it live in both.
func TestAMutantIsDecidedByTheTestsItIsNarrowedTo(t *testing.T) {
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
		want mutation.Outcome
	}{
		{name: "the test that reaches the line", test: "TestPositive", want: mutation.OutcomeKilled},
		{name: "a test that does not", test: "TestNegative", want: mutation.OutcomeSurvived},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempt := execute.RunOne(t.Context(), opts, execute.MutantRun{
				ID:      mutant.ID,
				Timeout: runTimeout,
				Tests:   map[string][]string{perTestModule: {test.test}},
			}, bins)
			if attempt.Outcome != test.want {
				t.Fatalf("narrowed to %s: outcome = %s, want %s (%v)\n%s",
					test.test, attempt.Outcome, test.want, attempt.Err, attempt.OutputTail)
			}
			if got := attempt.Tests[perTestModule]; len(got) != 1 || got[0] != test.test {
				t.Errorf("the attempt reports tests %v, want [%s]", attempt.Tests, test.test)
			}
		})
	}
}
