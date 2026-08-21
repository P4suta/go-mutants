// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// The execution phase against a real toolchain and a real test suite.
//
// Every other test in this package injects a runner, which is the only way to
// pin the scheduling policy: a kill, a timeout, a stale-catalogue exit and a
// start failure are four exit statuses, and building fixture programs that
// produce them on two platforms would be testing the fixtures. What injection
// cannot answer is whether the statuses this package interprets are the ones a
// real `go test -c` binary actually produces — whether the environment it
// composes really activates a mutant, whether the working directory it chooses
// really lets a test run, whether exit 97 really comes back from a runtime that
// was handed an identity it does not know.
//
// So this file builds the killable fixture for real: snapshot, discover,
// catalogue, instrument, compile, and then schedule the mutants whose fates the
// fixture was designed to have. It shares one instrumented snapshot and one set
// of binaries across its steps, because that is what a real run does and
// because the compile is the expensive part.
//
// Run it with `mise run test-integration`, or:
//
//	go test -tags integration ./internal/execute/...
package execute_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/snapshot"
)

const (
	// killableModule is the module path of the fixture this file drives.
	killableModule = "fixture.example/killable"

	// buildTimeout bounds each toolchain command. A compile of a three-file
	// module takes well under a second once the build cache is warm, so this is
	// not a budget — it is the point past which something has hung.
	buildTimeout = 5 * time.Minute

	// runTimeout bounds one mutant attempt. The fixture's whole suite runs in
	// milliseconds; the generosity is here so that a loaded CI machine cannot
	// turn a kill into a timeout and make this file flaky about the one thing it
	// exists to assert.
	runTimeout = 60 * time.Second
)

// TestExecutesTheKillableFixtureEndToEnd runs the fixture's four mutants
// through the real pipeline and watches them meet the fates the fixture was
// built to give them.
//
// The kills and the survival are halves of one claim and neither is worth much
// alone. Everything reported killed could be a tree that stopped compiling;
// everything reported survived could be activation that never happened.
// Together — same binaries, same scheduler, one changed environment variable —
// they say this package really is measuring what it claims to measure.
func TestExecutesTheKillableFixtureEndToEnd(t *testing.T) {
	toolchain := locateToolchain(t)
	snap := snapshotFixture(t, "killable")
	catalog := instrumentFixture(t, toolchain, snap)

	// Outside the snapshot, both of them. A test binary written into the tree
	// would show up in the snapshot re-digest as drift indistinguishable from a
	// test that wrote into its own package, and a scratch directory inside it
	// would be deleted by the snapshot cleanup while children were still using
	// it.
	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		BinDir:       filepath.Join(t.TempDir(), "bin"),
		ScratchDir:   filepath.Join(t.TempDir(), "tmp"),
		Jobs:         2,
		Timeout:      buildTimeout,
	}

	bins, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the fixture's test binaries: %v\n%s", err, execute.OutputOf(err))
	}
	// One package with tests, so one binary. The fixture has three source files
	// and two test files in a single package, which is what makes this number a
	// statement about the skip rule rather than a coincidence.
	if len(bins) != 1 {
		t.Fatalf("built %d test binaries, want 1: %+v", len(bins), bins)
	}
	if bins[0].ImportPath != killableModule {
		t.Errorf("built %q, want %q", bins[0].ImportPath, killableModule)
	}
	if bins[0].Dir != snap.Root {
		t.Errorf("the package directory is %q, want the snapshot root %q", bins[0].Dir, snap.Root)
	}
	if info, statErr := os.Stat(bins[0].BinPath); statErr != nil || info.Size() == 0 {
		t.Fatalf("the compiled binary at %s is missing or empty: %v", bins[0].BinPath, statErr)
	}

	// The fixture's own claim about itself, made machine-checkable. Each mutant
	// is named by its file and its rule rather than by its identity: an identity
	// is a digest over the fixture's bytes, so a hard-coded one would turn every
	// edit to a comment in the fixture into a failure here.
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
		queue[i] = execute.MutantRun{ID: mutantAt(t, catalog, w.path, w.rule).ID, Timeout: runTimeout}
	}

	// Atomic rather than plain counters because the hooks really are called
	// concurrently: Jobs is 2 over a queue of 4, so both workers increment these.
	// A plain `int++` from two goroutines is a data race by the Go memory model —
	// it can lose an increment and fail the assertion below for no reason — and
	// it would be a test that violates the very contract execute.Hooks documents.
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
	// No mutant here should time out, so every one settles on its first attempt:
	// one start and one finish each, and no retry pass.
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
			// A red suite is not yet evidence: a tree that stopped compiling
			// exits non-zero too. The mutant's signature is the wrong answer it
			// produces at the one input where it differs from the original, so
			// that is what gets asserted.
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

// TestRefusesAnIdentityTheGeneratedRuntimeDoesNotKnow proves the one exit
// status that must never be read as a kill really is produced, and really is
// recognised.
//
// Exit 97 comes from the generated runtime's init, before the testing framework
// starts. It is non-zero, so the cheap reading is "the tests failed" — and that
// reading would let a catalogue that no longer matches the instrumented tree
// report a perfect score. Nothing but a real instrumented binary can prove the
// status is what this package thinks it is.
func TestRefusesAnIdentityTheGeneratedRuntimeDoesNotKnow(t *testing.T) {
	toolchain := locateToolchain(t)
	snap := snapshotFixture(t, "killable")
	instrumentFixture(t, toolchain, snap)

	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		BinDir:       filepath.Join(t.TempDir(), "bin"),
		ScratchDir:   filepath.Join(t.TempDir(), "tmp"),
		Jobs:         1,
		Timeout:      buildTimeout,
	}
	bins, err := execute.BuildTestBinaries(t.Context(), opts)
	if err != nil {
		t.Fatalf("building the fixture's test binaries: %v\n%s", err, execute.OutputOf(err))
	}

	// An identity of the right shape — 64 hex characters — and a value no digest
	// produces.
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
	// The refusal happens in init, so nothing ran. That is the property worth
	// asserting and the only one that does not become a flake on a loaded
	// machine.
	for _, forbidden := range []string{"PASS", "=== RUN", "--- "} {
		if strings.Contains(attempt.OutputTail, forbidden) {
			t.Errorf("the binary got as far as %q before refusing:\n%s", forbidden, attempt.OutputTail)
		}
	}
}

// TestOnlyTheInstrumentedFilesDriftedDuringExecution is the gate that catches a
// test writing into the shared snapshot, applied to this package's own
// activity.
//
// Every worker shares one snapshot, which is what makes one build enough. That
// only holds if running the binaries leaves the tree alone — so the drift after
// a full schedule has to be exactly the instrumentation's own rewrite, with no
// compiled binary, no temporary file, and no coverage data added to it.
func TestOnlyTheInstrumentedFilesDriftedDuringExecution(t *testing.T) {
	toolchain := locateToolchain(t)
	snap := snapshotFixture(t, "killable")
	catalog := instrumentFixture(t, toolchain, snap)

	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		BinDir:       filepath.Join(t.TempDir(), "bin"),
		ScratchDir:   filepath.Join(t.TempDir(), "tmp"),
		Jobs:         2,
		Timeout:      buildTimeout,
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

// locateToolchain finds the Go toolchain, or ends the test saying so.
func locateToolchain(t *testing.T) gocmd.Toolchain {
	t.Helper()
	toolchain, err := gocmd.LocateContext(t.Context(), gocmd.Options{})
	if err != nil {
		t.Fatalf("locating the Go toolchain: %v", err)
	}
	return toolchain
}

// snapshotFixture copies a corpus module into a disposable directory and
// registers its removal.
//
// The cleanup is registered the moment the snapshot exists, before the caller
// can do anything that fails: every step after this one is entitled to call
// t.Fatalf, and a snapshot that outlives the test is a copy of a tree left in
// the temporary directory with nobody to remove it.
func snapshotFixture(t *testing.T, name string) *snapshot.Snapshot {
	t.Helper()
	root, absErr := filepath.Abs(filepath.Join("..", "..", "fixtures", name))
	if absErr != nil {
		t.Fatalf("resolving the %s fixture: %v", name, absErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr != nil {
		t.Fatalf("fixture %s is not a module: %v", name, statErr)
	}
	snap, createErr := snapshot.Create(root, snapshot.Options{DestParent: t.TempDir()})
	if createErr != nil {
		t.Fatalf("snapshotting the %s fixture: %v", name, createErr)
	}
	t.Cleanup(func() {
		if cleanupErr := snap.Cleanup(); cleanupErr != nil {
			t.Errorf("cleaning up the snapshot at %s: %v", snap.Root, cleanupErr)
		}
	})
	return snap
}

// instrumentFixture discovers, catalogues, and instruments a snapshot, and
// returns the catalogue every later step indexes mutants by.
func instrumentFixture(t *testing.T, toolchain gocmd.Toolchain, snap *snapshot.Snapshot) *mutation.Catalog {
	t.Helper()
	found, discoverErr := discover.Discover(t.Context(), discover.Options{
		SnapshotRoot: snap.Root,
		Toolchain:    toolchain,
	})
	if discoverErr != nil {
		t.Fatalf("discovering the fixture: %v", discoverErr)
	}
	catalog, catalogErr := discover.BuildCatalog(found)
	if catalogErr != nil {
		t.Fatalf("building the catalogue: %v", catalogErr)
	}
	// The guard hints travel with the catalogue, from the pass that had the
	// type checker to the one that rewrites bytes: internal/instrument cannot
	// choose a rewrite form for itself, and a catalogued mutant with no hint is
	// refused rather than guessed at.
	hints, hintsErr := instrument.HintsOf(found.Candidates)
	if hintsErr != nil {
		t.Fatalf("indexing the guard hints: %v", hintsErr)
	}
	if _, instrumentErr := instrument.Instrument(instrument.Options{
		SnapshotRoot: snap.Root,
		ModulePath:   found.ModulePath,
		Catalog:      catalog,
		Hints:        hints,
	}); instrumentErr != nil {
		t.Fatalf("instrumenting the snapshot: %v", instrumentErr)
	}
	return catalog
}

// mutantAt returns the one catalogued mutant of a rule in a file.
//
// Uniqueness is asserted rather than assumed, and the assertion is what turns
// the fixture's layout into a contract: one function per file and no repeated
// operator means a rule in a file names exactly one mutant, so a test can say
// which mutant it means without knowing an identity or a catalogue position.
func mutantAt(t *testing.T, catalog *mutation.Catalog, path, rule string) mutation.Mutant {
	t.Helper()
	var found []mutation.Mutant
	for _, m := range catalog.Mutants() {
		if m.Path == path && m.Rule.Name == rule {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the catalogue holds %d mutants of %s in %s, want exactly 1", len(found), rule, path)
	}
	return found[0]
}

// TestCoveragePassLeavesNoTraceInTheSnapshot is the drift gate's verification
// for the phase that was added after it.
//
// The gate allowlists exactly two kinds of change — the files validation left
// carrying guards, and the generated runtime package — and coverage-guided
// selection deliberately needed no third entry. This asserts why: the raw
// coverage data goes into a directory the caller places outside the snapshot,
// and the `-cover` binaries' own temporary files follow TMPDIR to the
// per-worker scratch directory, so a full profiling pass plus a full schedule
// drifts the tree by exactly what instrumentation drifted it by.
//
// It is the same assertion [TestOnlyTheInstrumentedFilesDriftedDuringExecution]
// makes, with coverage turned on — which is the configuration a default run now
// uses, and therefore the one the gate has to hold for.
func TestCoveragePassLeavesNoTraceInTheSnapshot(t *testing.T) {
	toolchain := locateToolchain(t)
	snap := snapshotFixture(t, "killable")
	catalog := instrumentFixture(t, toolchain, snap)

	work := t.TempDir()
	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: snap.Root,
		BinDir:       filepath.Join(work, "bin"),
		ScratchDir:   filepath.Join(work, "workers"),
		CoverPkg:     killableModule + "/...",
		Jobs:         2,
		Timeout:      buildTimeout,
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
	// The data really was written, or this would be asserting that a pass which
	// did nothing left no trace.
	for _, data := range collected {
		entries, readErr := os.ReadDir(data.Dir)
		if readErr != nil {
			t.Fatalf("reading %s: %v", data.Dir, readErr)
		}
		if len(entries) == 0 {
			t.Fatalf("the coverage pass over %s wrote nothing into %s", data.ImportPath, data.Dir)
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
