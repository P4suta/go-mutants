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
	if result.Loops != 1 {
		t.Fatalf("the tree counted %d loops, want 1: %v", result.Loops, result.LoopBase)
	}

	binary := filepath.Join(testkit.Scratch(t), "runaway.test")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	compile := mutantkit.RunGo(t, toolchain, snap.Root, env, "test", "-c", "-o", binary, ".")
	mutantkit.RequireExit(t, compile, 0, "compiling the fixture's test binary")

	run := func(t *testing.T, env []string) runner.Result {
		t.Helper()
		return runner.Run(t.Context(), runner.Spec{
			Argv:    []string{binary, "-test.v"},
			Dir:     snap.Root,
			Env:     env,
			Timeout: mutantkit.StepTimeout,
		})
	}

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

		if strings.Contains(string(diverged.Output), "--- PASS:") {
			t.Errorf("%s reported a pass:\n%s", what, diverged.Output)
		}
	})

	t.Run("a mutant that returns is left alone", func(t *testing.T) {
		control := mutantkit.MutantAt(t, catalog, "countdown.go", "gt-to-ge")
		red := run(t, bounded(control.ID))
		what := "the suite with " + control.DisplayID + " active under the same ceiling"
		mutantkit.RequireExit(t, red, 1, what)
		mutantkit.RequireOutput(t, red, what, "--- FAIL: TestCountdown")
	})

	t.Run("a tree with no table is bounded in time alone", func(t *testing.T) {
		green := run(t, testkit.Compose(t, testkit.Scratch(t)))
		mutantkit.RequireExit(t, green, 0, "the instrumented suite with no ceilings at all")
		mutantkit.RequireOutput(t, green, "the instrumented suite with no ceilings at all",
			"--- PASS: TestCountdown")
	})
}
