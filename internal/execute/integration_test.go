// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package execute_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

const (
	killableModule = "fixture.example/killable"

	buildTimeout = 5 * time.Minute

	runTimeout = 60 * time.Second
)

func TestExecutesTheKillableFixtureEndToEnd(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	snap := mutantkit.Snapshot(t, "killable")
	catalog := mutantkit.InstrumentWith(t, toolchain, snap, env)

	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		BinDir:       filepath.Join(t.TempDir(), "bin"),
		ScratchDir:   filepath.Join(t.TempDir(), "tmp"),
		Jobs:         2,
		Timeout:      buildTimeout,
		Env:          env,
	}

	bins, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the fixture's test binaries: %v\n%s", err, execute.OutputOf(err))
	}
	if len(bins) != 1 {
		t.Fatalf("built %d test binaries, want 1: %+v", len(bins), bins)
	}
	if bins[0].ImportPath != killableModule {
		t.Errorf("built %q, want %q", bins[0].ImportPath, killableModule)
	}
	if !testkit.SamePath(bins[0].Dir, snap.Root) {
		t.Errorf("the package directory is %q, want the snapshot root %q", bins[0].Dir, snap.Root)
	}
	if info, statErr := os.Stat(bins[0].BinPath); statErr != nil || info.Size() == 0 {
		t.Fatalf("the compiled binary at %s is missing or empty: %v", bins[0].BinPath, statErr)
	}

	want := []struct {
		path, rule string
		outcome    mutation.Outcome
		evidence   string
	}{
		{"clamp.go", "lt-to-le", mutation.OutcomeKilled, "Clamp(10, 0, 10) = 10, want 9"},
		{"clamp.go", "gt-to-ge", mutation.OutcomeKilled, "Clamp(0, 0, 10) = 0, want 1"},
		{"ready.go", "true-to-false", mutation.OutcomeKilled, "IsReady() = false, want true"},
		{"untested.go", "neq-to-eq", mutation.OutcomeSurvived, ""},
	}

	queue := make([]execute.MutantRun, len(want))
	for i, w := range want {
		queue[i] = execute.MutantRun{ID: mutantkit.MutantAt(t, catalog, w.path, w.rule).ID, Timeout: runTimeout}
	}

	var started, finished atomic.Int64
	hooks := execute.Hooks{
		Started:  func(string, int) { started.Add(1) },
		Finished: func(execute.MutantResult) { finished.Add(1) },
	}

	results, err := execute.Schedule(t.Context(), opts, queue, bins, hooks)
	if err != nil {
		t.Fatalf("scheduling the fixture's mutants: %v", err)
	}
	if len(results) != len(want) {
		t.Fatalf("got %d results, want %d", len(results), len(want))
	}
	if started.Load() != int64(len(want)) || finished.Load() != int64(len(want)) {
		t.Errorf("hooks fired %d starts and %d finishes, want %d of each",
			started.Load(), finished.Load(), len(want))
	}

	for i, w := range want {
		got := results[i]
		label := w.rule + " in " + w.path
		if got.ID != queue[i].ID {
			t.Errorf("result %d is for %s, want %s: the results are out of order", i, got.ID, queue[i].ID)
		}
		if got.Final != w.outcome {
			t.Errorf("%s = %s, want %s\n%s", label, got.Final, w.outcome, got.OutputTail)
			continue
		}
		if len(got.Attempts) != 1 {
			t.Errorf("%s took %d attempts, want 1", label, len(got.Attempts))
		}
		if got.Duration <= 0 {
			t.Errorf("%s reported a duration of %s, want the time its children took", label, got.Duration)
		}

		switch w.outcome {
		case mutation.OutcomeKilled:
			if got.KilledBy != killableModule {
				t.Errorf("%s was killed by %q, want %q", label, got.KilledBy, killableModule)
			}
			if !strings.Contains(got.OutputTail, w.evidence) {
				t.Errorf("%s did not print its evidence %q:\n%s", label, w.evidence, got.OutputTail)
			}
		case mutation.OutcomeSurvived:
			if got.KilledBy != "" {
				t.Errorf("%s reported a killer (%q) while surviving", label, got.KilledBy)
			}
		}
	}
}

func TestRefusesAnIdentityTheGeneratedRuntimeDoesNotKnow(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	snap := mutantkit.Snapshot(t, "killable")
	mutantkit.InstrumentWith(t, toolchain, snap, env)

	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		BinDir:       filepath.Join(t.TempDir(), "bin"),
		ScratchDir:   filepath.Join(t.TempDir(), "tmp"),
		Jobs:         1,
		Timeout:      buildTimeout,
		Env:          env,
	}
	bins, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the fixture's test binaries: %v\n%s", err, execute.OutputOf(err))
	}

	unknown := strings.Repeat("0", 64)
	attempt := execute.RunOne(t.Context(), opts,
		execute.MutantRun{ID: unknown, Timeout: runTimeout}, bins)

	if attempt.Outcome != mutation.OutcomeErrored {
		t.Fatalf("outcome = %s, want %s\n%s", attempt.Outcome, mutation.OutcomeErrored, attempt.OutputTail)
	}
	if code := execute.CodeOf(attempt.Err); code != execute.CodeStaleCatalog {
		t.Errorf("code = %q, want %q (%v)", code, execute.CodeStaleCatalog, attempt.Err)
	}
	if attempt.KilledBy != "" {
		t.Errorf("a binary was credited with a detection that did not happen: %q", attempt.KilledBy)
	}
	for _, forbidden := range []string{"PASS", "=== RUN", "--- "} {
		if strings.Contains(attempt.OutputTail, forbidden) {
			t.Errorf("the binary got as far as %q before refusing:\n%s", forbidden, attempt.OutputTail)
		}
	}
}

func TestOnlyTheInstrumentedFilesDriftedDuringExecution(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	snap := mutantkit.Snapshot(t, "killable")
	catalog := mutantkit.InstrumentWith(t, toolchain, snap, env)

	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		BinDir:       filepath.Join(t.TempDir(), "bin"),
		ScratchDir:   filepath.Join(t.TempDir(), "tmp"),
		Jobs:         2,
		Timeout:      buildTimeout,
		Env:          env,
	}
	bins, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the fixture's test binaries: %v\n%s", err, execute.OutputOf(err))
	}

	queue := make([]execute.MutantRun, 0, catalog.Len())
	for _, m := range catalog.Mutants() {
		queue = append(queue, execute.MutantRun{ID: m.ID, Timeout: runTimeout})
	}
	if _, err := execute.Schedule(t.Context(), opts, queue, bins, execute.Hooks{}); err != nil {
		t.Fatalf("scheduling every mutant: %v", err)
	}

	drifts, redigestErr := snap.Redigest()
	if redigestErr != nil {
		t.Fatalf("re-digesting the snapshot: %v", redigestErr)
	}
	want := []string{
		"changed clamp.go",
		"added gomutants_rt/gomutants_rt.go",
		"changed ready.go",
		"changed untested.go",
	}
	got := make([]string, 0, len(drifts))
	for _, drift := range drifts {
		got = append(got, drift.Kind.String()+" "+drift.RelPath)
	}
	if !slices.Equal(got, want) {
		t.Errorf("the snapshot drifted as\n\t%s\nwant\n\t%s",
			strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}

func TestScopedBuildCompilesOnlyTheNamedPackages(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	snap := mutantkit.Snapshot(t, "coverage")

	const (
		corePackage   = "fixture.example/coverage/core"
		callerPackage = "fixture.example/coverage/caller"
	)
	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		BinDir:       filepath.Join(t.TempDir(), "bin"),
		Jobs:         2,
		Timeout:      buildTimeout,
		Env:          env,
	}

	whole, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the whole fixture: %v\n%s", err, execute.OutputOf(err))
	}
	if got := importPathsOf(whole); !slices.Equal(got, []string{callerPackage, corePackage}) {
		t.Fatalf("the unscoped build produced %q, want both of the fixture's test packages", got)
	}

	opts.BinDir = filepath.Join(t.TempDir(), "scoped")
	opts.Packages = []string{"./core/..."}
	scoped, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the scoped fixture: %v\n%s", err, execute.OutputOf(err))
	}
	if got := importPathsOf(scoped); !slices.Equal(got, []string{corePackage}) {
		t.Fatalf("the scoped build produced %q, want only %q", got, corePackage)
	}
	if info, statErr := os.Stat(scoped[0].BinPath); statErr != nil || info.Size() == 0 {
		t.Fatalf("the compiled binary at %s is missing or empty: %v", scoped[0].BinPath, statErr)
	}
	if got := len(testkit.Entries(t, opts.BinDir)); got != 1 {
		t.Errorf("the scoped binary directory holds %d files, want 1", got)
	}
}

func importPathsOf(bins []execute.TestBinary) []string {
	out := make([]string, len(bins))
	for i, bin := range bins {
		out[i] = bin.ImportPath
	}
	return out
}

func TestCoveragePassLeavesNoTraceInTheSnapshot(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	snap := mutantkit.Snapshot(t, "killable")
	catalog := mutantkit.InstrumentWith(t, toolchain, snap, env)

	work := t.TempDir()
	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		BinDir:       filepath.Join(work, "bin"),
		ScratchDir:   filepath.Join(work, "workers"),
		CoverPkg:     killableModule + "/...",
		Jobs:         2,
		Timeout:      buildTimeout,
		Env:          env,
	}
	bins, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the fixture's instrumented test binaries: %v\n%s", err, execute.OutputOf(err))
	}

	collected, err := execute.CollectCoverage(t.Context(), opts, bins, filepath.Join(work, "coverage"))
	if err != nil {
		t.Fatalf("the coverage pass: %v\n%s", err, execute.OutputOf(err))
	}
	if len(collected) != len(bins) {
		t.Fatalf("collected %d profiles for %d binaries", len(collected), len(bins))
	}
	for _, data := range collected {
		written, readErr := os.ReadFile(data.Path)
		if readErr != nil {
			t.Fatalf("reading %s: %v", data.Path, readErr)
		}
		if !strings.HasPrefix(string(written), "mode: ") {
			t.Fatalf("the coverage pass over %s wrote %q into %s, want a textfmt profile",
				data.ImportPath, first(string(written)), data.Path)
		}
	}

	queue := make([]execute.MutantRun, 0, catalog.Len())
	for _, m := range catalog.Mutants() {
		queue = append(queue, execute.MutantRun{ID: m.ID, Timeout: runTimeout})
	}
	if _, err := execute.Schedule(t.Context(), opts, queue, bins, execute.Hooks{}); err != nil {
		t.Fatalf("scheduling every mutant against the instrumented binaries: %v", err)
	}

	drifts, redigestErr := snap.Redigest()
	if redigestErr != nil {
		t.Fatalf("re-digesting the snapshot: %v", redigestErr)
	}
	want := []string{
		"changed clamp.go",
		"added gomutants_rt/gomutants_rt.go",
		"changed ready.go",
		"changed untested.go",
	}
	got := make([]string, 0, len(drifts))
	for _, drift := range drifts {
		got = append(got, drift.Kind.String()+" "+drift.RelPath)
	}
	if !slices.Equal(got, want) {
		t.Errorf("the snapshot drifted as\n\t%s\nwant\n\t%s",
			strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}

func first(document string) string {
	line, _, _ := strings.Cut(document, "\n")
	return line
}

func TestAPassThatOnlyNeedsOneFailureStopsAtIt(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	module := testkit.NewModule(t).
		Module("fixture.example/failfast").
		Source("pair_test.go", `package failfast

import "testing"

func TestAlphaFails(t *testing.T) { t.Error("alpha") }

func TestZuluFails(t *testing.T) { t.Error("zulu") }
`)

	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: module.Root(),
		BinDir:       filepath.Join(t.TempDir(), "bin"),
		ScratchDir:   filepath.Join(t.TempDir(), "tmp"),
		Jobs:         1,
		Timeout:      buildTimeout,
		Env:          env,
	}
	bins, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the module's test binary: %v\n%s", err, execute.OutputOf(err))
	}
	if len(bins) != 1 {
		t.Fatalf("built %d test binaries, want 1: %+v", len(bins), bins)
	}

	attempt := execute.RunControl(t.Context(), opts, execute.ControlRun{Timeout: runTimeout}, bins)
	if attempt.Err != nil {
		t.Fatalf("running the control: %v", attempt.Err)
	}
	output := string(attempt.Output)
	if attempt.ExitCode == 0 {
		t.Fatalf("the control exited 0 although both of its tests fail: %s", output)
	}
	if strings.Contains(output, "flag provided but not defined") {
		t.Fatalf("the test binary refused an argument the scheduler adds: %s", output)
	}
	if !strings.Contains(output, "TestAlphaFails") {
		t.Errorf("the first failing test is not in the output, so nothing ran as expected: %s", output)
	}
	if strings.Contains(output, "TestZuluFails") {
		t.Errorf("the test after the first failure ran anyway: %s", output)
	}
}
