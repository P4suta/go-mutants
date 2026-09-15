// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package instrument_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// TestACountedLoopStopsTheMutantThatDoesNotReturn is ADR 0013 end to end, and
// it is the one claim no unit tier can make: that the counter the rewrite
// writes, the ceiling the generated runtime reads, and the census the original
// program leaves behind are the same three numbers.
//
// The fixture is the one written for a runaway. `Countdown` counts down from n,
// and negating its loop condition makes it true exactly where the original
// stopped — so the row that asks for nothing to be counted, which the fixture
// deliberately runs first, is a loop that never stops appending. Until now the
// only things that could end it were a clock and a memory bound. Here it is
// ended by arithmetic: the original went round that loop three times at most
// anywhere in the suite, this one has gone round a thousand and one, and the
// process says which loop it was and both counts.
//
// The control matters as much as the divergence. A second mutant of the same
// comparison runs one iteration too many and is caught by an assertion, with
// the same ceiling in force — which is what says the ceiling stopped a loop
// that does not stop rather than stopping loops.
func TestACountedLoopStopsTheMutantThatDoesNotReturn(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	snap := mutantkit.Snapshot(t, "runaway")

	found := mutantkit.DiscoverWith(t, toolchain, snap, env)
	catalog := mutantkit.Catalog(t, found)
	result, err := instrument.Instrument(instrument.Options{
		SnapshotRoot: snap.Root,
		ModulePath:   found.ModulePath,
		Catalog:      catalog,
		Hints:        mutantkit.Hints(t, found),
	})
	if err != nil {
		t.Fatalf("instrumenting the snapshot: %v", err)
	}
	// One `for` in the one file that carries a guard, which is what makes the
	// site index below a fact rather than a guess.
	if result.Loops != 1 {
		t.Fatalf("the tree counted %d loops, want 1: %v", result.Loops, result.LoopBase)
	}

	binary := filepath.Join(testkit.Scratch(t), "runaway.test")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	// Compiled once and run four times. Straight to the binary rather than
	// through `go test`, which reports a child's exit status as its own 1 and
	// loses the one this whole mechanism turns on.
	compile := mutantkit.RunGo(t, toolchain, snap.Root, env, "test", "-c", "-o", binary, ".")
	mutantkit.RequireExit(t, compile, 0, "compiling the fixture's test binary")

	run := func(t *testing.T, env []string) runner.Result {
		t.Helper()
		return runner.Run(t.Context(), runner.Spec{
			// -test.v so that the assertions below can name a test rather than
			// read a bare PASS, which is what a binary started directly prints.
			Argv:    []string{binary, "-test.v"},
			Dir:     snap.Root,
			Env:     env,
			Timeout: mutantkit.StepTimeout,
		})
	}

	// The path the environment names and the path a module's runtime writes are
	// not the same: a workspace has one generated package per module, each
	// numbering its own loops, and the two variables name one path. The suffix
	// is what keeps them apart, and it is computed the same way at both ends.
	suffix := instrument.LoopFileSuffix(found.ModulePath)
	censusPath := filepath.Join(testkit.Scratch(t), "census.txt")
	t.Run("the original program leaves a census", func(t *testing.T) {
		census := run(t, append(testkit.Compose(t, testkit.Scratch(t)),
			instrument.LoopCensusEnv+"="+censusPath))
		mutantkit.RequireExit(t, census, 0, "the instrumented baseline taking a census")

		file, openErr := os.Open(censusPath + suffix)
		if openErr != nil {
			t.Fatalf("the census was not written: %v", openErr)
		}
		defer file.Close()
		counts, readErr := instrument.ReadLoopCensus(file, result.Loops)
		if readErr != nil {
			t.Fatalf("reading the census: %v", readErr)
		}
		// The census records a site's count every time its running maximum
		// rises and doubles the ladder each time, so what it holds is within a
		// factor of two below the true maximum — three, here, for the row that
		// counts down from three. What matters is that the loop was seen at
		// all: a site with no count is a site whose ceiling is the floor.
		if counts[0] == 0 {
			t.Errorf("the census recorded nothing for the fixture's one loop: %v", counts)
		}
	})

	limitsPath := filepath.Join(testkit.Scratch(t), "limits.txt")
	const ceiling = 1000
	limits, err := os.Create(limitsPath + suffix)
	if err != nil {
		t.Fatalf("creating the limit table: %v", err)
	}
	if err := instrument.WriteLoopLimits(limits, []uint64{ceiling}); err != nil {
		t.Fatalf("writing the limit table: %v", err)
	}
	if err := limits.Close(); err != nil {
		t.Fatalf("closing the limit table: %v", err)
	}

	bounded := func(id string) []string {
		return append(mutantkit.Activate(testkit.Compose(t, testkit.Scratch(t)), id),
			instrument.LoopLimitsEnv+"="+limitsPath)
	}

	t.Run("the mutant that does not return says so", func(t *testing.T) {
		runaway := mutantkit.MutantAt(t, catalog, "countdown.go", "negate-loop-condition")
		diverged := run(t, bounded(runaway.ID))
		what := "the suite with " + runaway.DisplayID + " active under a ceiling of 1000"
		mutantkit.RequireExit(t, diverged, instrument.DivergedExit, what)
		mutantkit.RequireOutput(t, diverged, what,
			"countdown.go:", "1001", "1000", "does not return")

		// And it said so before the testing framework could report a pass:
		// the loop never returns, so a run that got as far as printing one is a
		// run that counted the wrong thing.
		if strings.Contains(string(diverged.Output), "--- PASS:") {
			t.Errorf("%s reported a pass:\n%s", what, diverged.Output)
		}
	})

	t.Run("a mutant that returns is left alone", func(t *testing.T) {
		// The same comparison, one iteration too many, under the same ceiling.
		// It is caught by the fixture's own assertion, which is exit 1 and not
		// the divergence status: a ceiling that stopped this one would be a
		// ceiling that stops loops rather than runaways.
		control := mutantkit.MutantAt(t, catalog, "countdown.go", "gt-to-ge")
		red := run(t, bounded(control.ID))
		what := "the suite with " + control.DisplayID + " active under the same ceiling"
		mutantkit.RequireExit(t, red, 1, what)
		mutantkit.RequireOutput(t, red, what, "--- FAIL: TestCountdown")
	})

	t.Run("a tree with no table is bounded in time alone", func(t *testing.T) {
		// No ceilings, so every loop is unlimited and the counters decide
		// nothing: the fixture's own suite passes exactly as it does without
		// them. This is the shape every run of an instrumented tree has that
		// was not asked to count — the drift gate, a developer's own `go test`
		// in a kept snapshot — and it has to be the shape it always was.
		green := run(t, testkit.Compose(t, testkit.Scratch(t)))
		mutantkit.RequireExit(t, green, 0, "the instrumented suite with no ceilings at all")
		mutantkit.RequireOutput(t, green, "the instrumented suite with no ceilings at all",
			"--- PASS: TestCountdown")
	})
}
