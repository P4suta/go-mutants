// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The probe tree end to end, against a real toolchain and a real test suite.
//
// The unit tests in this package prove the rewrite byte for byte and prove that
// it compiles. Neither can answer the question a probe tree exists to answer,
// which is whether the tree still *is* the program: the whole licence a probe
// gives a run — "this test never saw a value that mutant would have changed, so
// it cannot kill it" — rests on the measured program being the measured
// program. So the fixture's own suite is run against the probe tree twice, once
// with nothing in the environment and once recording, and both have to pass
// exactly as they pass in the tree the user wrote.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/instrument/...
package instrument_test

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
)

// TestProbeTreeIsSemanticsPreserving runs the killable fixture's suite against
// its probe tree and watches it behave exactly as the fixture does.
//
// Three claims, in the order they have to be established. The tree builds. Its
// suite passes with nothing in the environment, printing the same per-test
// lines the pristine tree prints — which is what "the original semantics run"
// means where anybody can check it, and it is measured against the pristine run
// rather than against a list written here, so a fixture that grows a test
// cannot quietly stop being compared. And with the log variable set the suite
// still passes and the log is readable against the catalogue that generated it.
//
// The last step is the drift gate, run last so that it covers the builds and
// both suite runs as well as the rewrite: nothing outside the probed files and
// the generated runtime may have changed, or a probe pass would be measuring a
// tree it had itself disturbed.
func TestProbeTreeIsSemanticsPreserving(t *testing.T) {
	toolchain := locateToolchain(t)
	snap := snapshotFixture(t, "killable")

	// The pristine suite first, because it is the thing the probe tree has to
	// agree with. Nothing is written by it: the fixture's tests assert and
	// return, and the drift gate at the end is what says so.
	pristine := runSuite(t, toolchain, snap.Root, "")
	requireExit(t, pristine, 0, "the pristine fixture's suite")
	wantLines := verdictLines(pristine)
	if len(wantLines) == 0 {
		t.Fatalf("the pristine suite printed no per-test verdicts, so there is nothing to compare against:\n%s",
			pristine.Output)
	}

	found, discoverErr := discover.Discover(t.Context(), discover.Options{
		SnapshotRoot: snap.Root,
		Toolchain:    toolchain,
	})
	if discoverErr != nil {
		t.Fatalf("discovering the killable fixture: %v", discoverErr)
	}
	catalog := catalogFrom(t, found)
	hints, hintsErr := instrument.HintsOf(found.Candidates)
	if hintsErr != nil {
		t.Fatalf("indexing the guard hints: %v", hintsErr)
	}

	instrumented, instrumentErr := instrument.Instrument(instrument.Options{
		SnapshotRoot: snap.Root,
		ModulePath:   found.ModulePath,
		Catalog:      catalog,
		Hints:        hints,
		Mode:         instrument.ModeProbe,
	})
	if instrumentErr != nil {
		t.Fatalf("instrumenting the snapshot as a probe tree: %v", instrumentErr)
	}

	// ready.go is absent, and that is the return form's boundary showing
	// through: its mutant is `true-to-false` on the literal, which the
	// catalogue kept over the `return-false` proposing the same edit, and a
	// boolean literal has no probe form yet. An unprobed mutant costs a run
	// nothing but the executions it could have skipped.
	if want := []string{"clamp.go", "untested.go"}; !slices.Equal(instrumented.FilesInstrumented, want) {
		t.Errorf("probed %q, want %q", instrumented.FilesInstrumented, want)
	}
	if want := map[string]int{"clamp.go": 3, "untested.go": 1}; !maps.Equal(instrumented.GuardsByFile, want) {
		t.Errorf("probe sites by file = %v, want %v", instrumented.GuardsByFile, want)
	}

	t.Run("the probe tree builds", func(t *testing.T) {
		build := goInSnapshot(t, toolchain, snap.Root, "", "build", "./...")
		requireExit(t, build, 0, "`go build ./...` in the probe tree")
	})

	t.Run("the probe tree's suite passes unprobed", func(t *testing.T) {
		// No variable in the environment: the runtime is linked in, Infect
		// costs a nil check, and the program is the program. The per-test
		// verdicts are compared with the pristine run's rather than merely
		// counted, because a rewrite that changed one answer would still exit 0
		// if the fixture happened to have a test that tolerated it.
		quiet := runProbeSuite(t, toolchain, snap.Root, "")
		requireExit(t, quiet, 0, "the probe tree's suite with no log")
		if got := verdictLines(quiet); !slices.Equal(got, wantLines) {
			t.Errorf("the probe tree's suite reported\n\t%s\nthe pristine tree reported\n\t%s",
				strings.Join(got, "\n\t"), strings.Join(wantLines, "\n\t"))
		}
	})

	t.Run("the probe tree's suite passes while recording", func(t *testing.T) {
		log := filepath.Join(t.TempDir(), "infection.log")
		recording := runProbeSuite(t, toolchain, snap.Root, log)
		requireExit(t, recording, 0, "the probe tree's suite with a log")
		if got := verdictLines(recording); !slices.Equal(got, wantLines) {
			t.Errorf("the recording suite reported\n\t%s\nthe pristine tree reported\n\t%s",
				strings.Join(got, "\n\t"), strings.Join(wantLines, "\n\t"))
		}

		data, readErr := os.ReadFile(log)
		if readErr != nil {
			t.Fatalf("reading the infection log the suite wrote: %v", readErr)
		}
		infected, parseErr := instrument.ReadInfectionLog(bytes.NewReader(data), catalog.Digest(), catalog.Len())
		if parseErr != nil {
			t.Fatalf("reading the infection log against the catalogue: %v\n%s", parseErr, data)
		}
		// At least one, because Clamp is exercised and every one of its
		// returned values is something other than zero at some input. Which
		// ones exactly is the fixture's business rather than this test's; that
		// a probe pass records anything at all is this one's.
		if len(infected) == 0 {
			t.Errorf("the probe recorded nothing, although the suite exercises every return in clamp.go:\n%s", data)
		}
		for _, index := range infected {
			if uint64(index) >= uint64(catalog.Len()) {
				t.Errorf("the probe recorded index %d, past the catalogue's %d mutants", index, catalog.Len())
			}
		}
	})

	t.Run("only the probed files drifted", func(t *testing.T) {
		drifts, redigestErr := snap.Redigest()
		if redigestErr != nil {
			t.Fatalf("re-digesting the snapshot: %v", redigestErr)
		}
		probed := make(map[string]bool, len(instrumented.FilesInstrumented))
		for _, path := range instrumented.FilesInstrumented {
			probed[path] = true
		}
		runtimePrefix := instrumented.RuntimeDir + "/"
		var unexpected []string
		for _, drift := range drifts {
			switch {
			case drift.Kind == snapshot.DriftChanged && probed[drift.RelPath]:
			case drift.Kind == snapshot.DriftAdded && strings.HasPrefix(drift.RelPath, runtimePrefix):
			default:
				unexpected = append(unexpected, drift.Kind.String()+" "+drift.RelPath)
			}
		}
		if len(unexpected) != 0 {
			t.Errorf("the probe tree drifted in %d way(s) that are neither a probed file nor the generated runtime:\n\t%s",
				len(unexpected), strings.Join(unexpected, "\n\t"))
		}
	})
}

// runProbeSuite runs the fixture's whole suite in the snapshot, recording into
// log when it is not empty.
//
// It is [runSuite] with the other variable, and the environment is composed the
// same way for the same reason: a developer with GO_MUTANTS_PROBE exported in
// their shell must not turn the unprobed run into a probed one, which is
// exactly what [fixtureEnv] strips.
func runProbeSuite(t *testing.T, toolchain gocmd.Toolchain, root, log string) runner.Result {
	t.Helper()

	spec := toolchain.Command("test", "-count=1", "-v", "./...")
	spec.Dir = root
	spec.Env = fixtureEnv("")
	if log != "" {
		spec.Env = append(spec.Env, instrument.ProbeEnv+"="+log)
	}
	spec.Timeout = stepTimeout
	return runner.Run(t.Context(), spec)
}

// verdictLines returns every per-test verdict `go test -v` printed, in order.
//
// It is the comparable form of "the suite behaved the same": exit 0 alone would
// be satisfied by a tree with no tests left in it, and the whole output carries
// timings that differ between runs.
func verdictLines(result runner.Result) []string {
	var out []string
	for _, line := range strings.Split(string(result.Output), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--- PASS:") || strings.HasPrefix(trimmed, "--- FAIL:") ||
			strings.HasPrefix(trimmed, "--- SKIP:") {
			out = append(out, trimmed)
		}
	}
	return out
}
