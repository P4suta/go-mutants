// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package instrument_test

import (
	"bytes"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestProbeTreeIsSemanticsPreserving(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	snap := mutantkit.Snapshot(t, "killable")

	pristine := mutantkit.RunSuite(t, toolchain, snap.Root, env)
	mutantkit.RequireExit(t, pristine, 0, "the pristine fixture's suite")
	wantLines := verdictLines(pristine)
	if len(wantLines) == 0 {
		t.Fatalf("the pristine suite printed no per-test verdicts, so there is nothing to compare against:\n%s",
			pristine.Output)
	}

	found := mutantkit.DiscoverWith(t, toolchain, snap, env)
	catalog := mutantkit.Catalog(t, found)

	instrumented, instrumentErr := instrument.Instrument(instrument.Options{
		SnapshotRoot: snap.Root,
		ModulePath:   found.ModulePath,
		Catalog:      catalog,
		Hints:        mutantkit.Hints(t, found),
		Mode:         instrument.ModeProbe,
	})
	if instrumentErr != nil {
		t.Fatalf("instrumenting the snapshot as a probe tree: %v", instrumentErr)
	}

	if want := []string{"clamp.go", "ready.go", "untested.go"}; !slices.Equal(instrumented.FilesInstrumented, want) {
		t.Errorf("probed %q, want %q", instrumented.FilesInstrumented, want)
	}
	if want := map[string]int{"clamp.go": 7, "ready.go": 1, "untested.go": 2}; !maps.Equal(instrumented.GuardsByFile, want) {
		t.Errorf("probe sites by file = %v, want %v", instrumented.GuardsByFile, want)
	}

	t.Run("the probe tree builds", func(t *testing.T) {
		build := mutantkit.RunGo(t, toolchain, snap.Root, env, "build", "./...")
		mutantkit.RequireExit(t, build, 0, "`go build ./...` in the probe tree")
	})

	t.Run("the probe tree's suite passes unprobed", func(t *testing.T) {
		quiet := runProbeSuite(t, toolchain, snap.Root, env, "")
		mutantkit.RequireExit(t, quiet, 0, "the probe tree's suite with no log")
		if got := verdictLines(quiet); !slices.Equal(got, wantLines) {
			t.Errorf("the probe tree's suite reported\n\t%s\nthe pristine tree reported\n\t%s",
				strings.Join(got, "\n\t"), strings.Join(wantLines, "\n\t"))
		}
	})

	t.Run("the probe tree's suite passes while recording", func(t *testing.T) {
		log := filepath.Join(testkit.Scratch(t), "infection.log")
		recording := runProbeSuite(t, toolchain, snap.Root, env, log)
		mutantkit.RequireExit(t, recording, 0, "the probe tree's suite with a log")
		if got := verdictLines(recording); !slices.Equal(got, wantLines) {
			t.Errorf("the recording suite reported\n\t%s\nthe pristine tree reported\n\t%s",
				strings.Join(got, "\n\t"), strings.Join(wantLines, "\n\t"))
		}

		data := testkit.ReadFile(t, log)
		infected, parseErr := instrument.ReadInfectionLog(bytes.NewReader(data), catalog.Digest(), catalog.Len())
		if parseErr != nil {
			t.Fatalf("reading the infection log against the catalogue: %v\n%s", parseErr, data)
		}
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

func runProbeSuite(t *testing.T, toolchain gocmd.Toolchain, root string, env []string, log string) runner.Result {
	t.Helper()

	if log != "" {
		env = append(slices.Clip(env), instrument.ProbeEnv+"="+log)
	}
	return mutantkit.RunSuite(t, toolchain, root, env)
}

func verdictLines(result runner.Result) []string {
	var out []string
	for _, line := range strings.Split(string(result.Output), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--- PASS:") || strings.HasPrefix(trimmed, "--- FAIL:") ||
			strings.HasPrefix(trimmed, "--- SKIP:") {
			out = append(out, withoutDuration(trimmed))
		}
	}
	return out
}

func withoutDuration(verdict string) string {
	open := strings.LastIndex(verdict, " (")
	if open < 0 || !strings.HasSuffix(verdict, "s)") {
		return verdict
	}
	return verdict[:open]
}
