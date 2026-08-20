// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
)

// coverPkg is the pattern a coverage-guided run builds with.
const coverPkg = "example.com/m/..."

// coverOptions wires a fake runner into options that can build and profile, and
// returns the coverage directory the pass is pointed at.
func coverOptions(t *testing.T, f *fake) (execute.Options, string) {
	t.Helper()
	work := t.TempDir()
	opts := execute.Options{
		Toolchain:    toolchain,
		SnapshotRoot: t.TempDir(),
		BinDir:       filepath.Join(work, "bin"),
		ScratchDir:   filepath.Join(work, "workers"),
		CoverPkg:     coverPkg,
		Jobs:         1,
		Timeout:      time.Minute,
	}
	return execute.WithRunner(opts, f.run), filepath.Join(work, "coverage")
}

// TestCoverPkgTurnsCoverageOnForEveryBuild pins the two flags and their order,
// because a `-coverpkg` without `-cover` is silently ignored by the go command
// and would leave the profiling pass with nothing to collect.
func TestCoverPkgTurnsCoverageOnForEveryBuild(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(
				pkgJSON("example.com/m/a", "/snap/a", true, false),
				pkgJSON("example.com/m/b", "/snap/b", true, false),
			))}
		}
		return runner.Result{}
	}}
	opts, _ := coverOptions(t, f)
	if _, err := execute.BuildTestBinaries(t.Context(), opts); err != nil {
		t.Fatalf("BuildTestBinaries: %v", err)
	}

	compiles := 0
	for _, c := range f.seen() {
		if isList(c) {
			continue
		}
		compiles++
		want := []string{"test", "-c", "-cover", "-coverpkg=" + coverPkg, "-o"}
		if len(c.Argv) < len(want)+1 || !slices.Equal(c.Argv[1:len(want)+1], want) {
			t.Errorf("compile argv = %v, want it to start with %v after the go binary", c.Argv, want)
		}
	}
	if compiles != 2 {
		t.Errorf("compiled %d binaries, want 2", compiles)
	}
}

// TestNoCoverPkgBuildsPlainly is the other half: coverage is opt-in, and a run
// that did not ask for it must not pay the teardown cost on every mutant.
func TestNoCoverPkgBuildsPlainly(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if isList(c) {
			return runner.Result{Output: []byte(listing(pkgJSON("example.com/m/a", "/snap/a", true, false)))}
		}
		return runner.Result{}
	}}
	opts, _ := coverOptions(t, f)
	opts.CoverPkg = ""
	if _, err := execute.BuildTestBinaries(t.Context(), opts); err != nil {
		t.Fatalf("BuildTestBinaries: %v", err)
	}
	for _, c := range f.seen() {
		if slices.Contains(c.Argv, "-cover") {
			t.Errorf("a plain build passed -cover: %v", c.Argv)
		}
	}
}

// TestCollectCoverageRunsEveryBinaryOnceIntoItsOwnDirectory pins the shape of
// the profiling pass.
//
// Three things are asserted and each is a decision. The directory is per
// binary, because merging two would answer "was this line reached by anything",
// which is the question coverage-guided selection exists not to ask. The
// activation variable is absent, because a profile taken with a mutant live
// would describe the mutant. And the directory arrives as `-test.gocoverdir`
// rather than as GOCOVERDIR, which is the difference between collecting data
// and silently collecting none.
func TestCollectCoverageRunsEveryBinaryOnceIntoItsOwnDirectory(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts, coverDir := coverOptions(t, f)
	bins := testBins("example.com/m/a", "example.com/m/b")

	collected, err := execute.CollectCoverage(t.Context(), opts, bins, coverDir)
	if err != nil {
		t.Fatalf("CollectCoverage: %v", err)
	}
	if len(collected) != len(bins) {
		t.Fatalf("collected %d profiles for %d binaries", len(collected), len(bins))
	}

	seen := f.seen()
	if len(seen) != len(bins) {
		t.Fatalf("started %d processes for %d binaries: %v", len(seen), len(bins), f.programs())
	}
	dirs := make(map[string]bool, len(collected))
	for i, data := range collected {
		if data.ImportPath != bins[i].ImportPath {
			t.Errorf("profile %d is for %q, want %q", i, data.ImportPath, bins[i].ImportPath)
		}
		if dirs[data.Dir] {
			t.Errorf("two binaries share the coverage directory %s", data.Dir)
		}
		dirs[data.Dir] = true
		if ok, statErr := statDir(data.Dir); statErr != nil || !ok {
			t.Errorf("the coverage directory %s was not created: %v", data.Dir, statErr)
		}

		c := seen[i]
		if c.program() != bins[i].BinPath {
			t.Errorf("process %d started %q, want %q", i, c.program(), bins[i].BinPath)
		}
		if c.Dir != bins[i].Dir {
			t.Errorf("process %d ran in %q, want the package directory %q", i, c.Dir, bins[i].Dir)
		}
		want := "-test.gocoverdir=" + data.Dir
		if !slices.Contains(c.Argv, want) {
			t.Errorf("process %d argv = %v, want it to carry %q", i, c.Argv, want)
		}
		if active := c.active(); active != "" {
			t.Errorf("the profiling run activated the mutant %q", active)
		}
		if envValue(c.Env, instrument.ActiveEnv) != "" {
			t.Errorf("the profiling run inherited %s", instrument.ActiveEnv)
		}
	}
}

// TestCollectCoverageRefusesADirectoryInsideTheSnapshot is the drift gate's
// half of the contract, enforced where the data would be written rather than
// where it would be noticed.
func TestCollectCoverageRefusesADirectoryInsideTheSnapshot(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts, _ := coverOptions(t, f)

	_, err := execute.CollectCoverage(t.Context(), opts,
		testBins("example.com/m/a"), filepath.Join(opts.SnapshotRoot, "coverage"))
	if err == nil {
		t.Fatal("CollectCoverage wrote coverage data into the snapshot")
	}
	if code := execute.CodeOf(err); code != execute.CodeCoverageDir {
		t.Errorf("code = %q, want %q (%v)", code, execute.CodeCoverageDir, err)
	}
	if len(f.seen()) != 0 {
		t.Errorf("a binary was started before the directory was refused: %v", f.programs())
	}
}

// TestCollectCoverageRefusesADirectoryItCannotCreate covers the other way the
// destination can be unusable.
func TestCollectCoverageRefusesADirectoryItCannotCreate(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts, _ := coverOptions(t, f)

	// A regular file where the pass wants a directory, which fails on every
	// platform and needs no permission games.
	blocked := filepath.Join(t.TempDir(), "coverage")
	writeFile(t, blocked, "not a directory")

	_, err := execute.CollectCoverage(t.Context(), opts, testBins("example.com/m/a"), blocked)
	if err == nil {
		t.Fatal("CollectCoverage succeeded with a file where its directory should be")
	}
	if code := execute.CodeOf(err); code != execute.CodeCoverageDir {
		t.Errorf("code = %q, want %q (%v)", code, execute.CodeCoverageDir, err)
	}
}

// TestCollectCoverageReportsAFailingBinary proves the pass does not quietly
// return a profile for a run that never happened.
func TestCollectCoverageReportsAFailingBinary(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if strings.Contains(c.program(), "example.com/m/b") {
			return failed("--- FAIL: TestSomething\n")
		}
		return passed()
	}}
	opts, coverDir := coverOptions(t, f)

	_, err := execute.CollectCoverage(t.Context(), opts,
		testBins("example.com/m/a", "example.com/m/b"), coverDir)
	if err == nil {
		t.Fatal("CollectCoverage succeeded with a failing binary")
	}
	if code := execute.CodeOf(err); code != execute.CodeCoverageFailed {
		t.Errorf("code = %q, want %q (%v)", code, execute.CodeCoverageFailed, err)
	}
	if !strings.Contains(err.Error(), "example.com/m/b") {
		t.Errorf("the failure does not name the binary: %v", err)
	}
}

// TestCollectCoverageRefusesBinariesWithoutInstrumentation catches the caller
// mistake that would otherwise produce empty profiles and a run that reported
// every mutant as uncovered.
func TestCollectCoverageRefusesBinariesWithoutInstrumentation(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts, coverDir := coverOptions(t, f)
	opts.CoverPkg = ""

	_, err := execute.CollectCoverage(t.Context(), opts, testBins("example.com/m/a"), coverDir)
	if code := execute.CodeOf(err); code != execute.CodeOptions {
		t.Fatalf("code = %q, want %q (%v)", code, execute.CodeOptions, err)
	}
	if len(f.seen()) != 0 {
		t.Errorf("a binary was started anyway: %v", f.programs())
	}
}

// TestBinariesNarrowsWhatIsStarted is coverage-guided selection as this package
// sees it: a mutant is measured against the binaries it was given and no
// others.
func TestBinariesNarrowsWhatIsStarted(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	bins := testBins("example.com/m/a", "example.com/m/b", "example.com/m/c")

	attempt := execute.RunOne(t.Context(), options(f, 1),
		execute.MutantRun{ID: "abc", Timeout: mutantTimeout, Binaries: []int{1}}, bins)

	if attempt.Outcome.String() != "survived" {
		t.Fatalf("outcome = %s, want survived", attempt.Outcome)
	}
	if got := f.programs(); !slices.Equal(got, []string{bins[1].BinPath}) {
		t.Errorf("started %v, want only %q", got, bins[1].BinPath)
	}
}

// TestNilBinariesRunsEveryBinary is the default every caller before
// coverage-guided selection relied on, asserted so that adding the field cannot
// have changed it.
func TestNilBinariesRunsEveryBinary(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	bins := testBins("example.com/m/a", "example.com/m/b")

	execute.RunOne(t.Context(), options(f, 1),
		execute.MutantRun{ID: "abc", Timeout: mutantTimeout}, bins)

	if got := f.programs(); !slices.Equal(got, []string{bins[0].BinPath, bins[1].BinPath}) {
		t.Errorf("started %v, want every binary", got)
	}
}

// TestBinariesStillStopsAtTheFirstKill proves narrowing changes which binaries
// are candidates, not how an attempt reads them.
func TestBinariesStillStopsAtTheFirstKill(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if strings.Contains(c.program(), "example.com/m/b") {
			return failed("--- FAIL: TestB\n")
		}
		return passed()
	}}
	bins := testBins("example.com/m/a", "example.com/m/b", "example.com/m/c")

	attempt := execute.RunOne(t.Context(), options(f, 1),
		execute.MutantRun{ID: "abc", Timeout: mutantTimeout, Binaries: []int{1, 2}}, bins)

	if attempt.KilledBy != "example.com/m/b" {
		t.Errorf("KilledBy = %q, want the binary that failed", attempt.KilledBy)
	}
	if got := f.programs(); !slices.Equal(got, []string{bins[1].BinPath}) {
		t.Errorf("started %v, want to have stopped at the kill", got)
	}
}

// TestBinariesRefusesAnEmptySubset is the guard against a free survivor.
//
// An empty subset would walk zero binaries and report the mutant as survived
// having started nothing, which is the flattering green [CodeNoTestBinaries]
// refuses for a whole run. A mutant no binary covers is not executed at all and
// is recorded by internal/engine, so an empty list reaching here can only be a
// caller bug — and it should be loud rather than free.
func TestBinariesRefusesAnEmptySubset(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	attempt := execute.RunOne(t.Context(), options(f, 1),
		execute.MutantRun{ID: "abc", Timeout: mutantTimeout, Binaries: []int{}},
		testBins("example.com/m/a"))

	if attempt.Outcome.String() != "errored" {
		t.Fatalf("outcome = %s, want errored", attempt.Outcome)
	}
	if code := execute.CodeOf(attempt.Err); code != execute.CodeMutantInvalid {
		t.Errorf("code = %q, want %q (%v)", code, execute.CodeMutantInvalid, attempt.Err)
	}
	if len(f.seen()) != 0 {
		t.Errorf("a binary was started for a mutant with an empty subset: %v", f.programs())
	}
}

// TestBinariesRefusesAnIndexOutOfRange catches the two sides of the run holding
// different binary lists, which is the only way a bad index can arise.
func TestBinariesRefusesAnIndexOutOfRange(t *testing.T) {
	t.Parallel()

	for _, index := range []int{-1, 1, 99} {
		f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
		attempt := execute.RunOne(t.Context(), options(f, 1),
			execute.MutantRun{ID: "abc", Timeout: mutantTimeout, Binaries: []int{index}},
			testBins("example.com/m/a"))

		if code := execute.CodeOf(attempt.Err); code != execute.CodeMutantInvalid {
			t.Errorf("index %d: code = %q, want %q (%v)", index, code, execute.CodeMutantInvalid, attempt.Err)
		}
		if len(f.seen()) != 0 {
			t.Errorf("index %d: a binary was started anyway: %v", index, f.programs())
		}
	}
}

// TestScheduleHonoursPerMutantSubsets is the same narrowing seen through the
// scheduler, which is how internal/engine reaches it.
func TestScheduleHonoursPerMutantSubsets(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	bins := testBins("example.com/m/a", "example.com/m/b")
	queue := []execute.MutantRun{
		{ID: "only-a", Timeout: mutantTimeout, Binaries: []int{0}},
		{ID: "only-b", Timeout: mutantTimeout, Binaries: []int{1}},
		{ID: "both", Timeout: mutantTimeout},
	}

	results, err := execute.Schedule(t.Context(), options(f, 1), queue, bins, execute.Hooks{})
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(results) != len(queue) {
		t.Fatalf("got %d results for %d mutants", len(results), len(queue))
	}

	started := make(map[string][]string, len(queue))
	for _, c := range f.seen() {
		started[activeOf(c)] = append(started[activeOf(c)], c.program())
	}
	want := map[string][]string{
		"only-a": {bins[0].BinPath},
		"only-b": {bins[1].BinPath},
		"both":   {bins[0].BinPath, bins[1].BinPath},
	}
	for id, expected := range want {
		if !slices.Equal(started[id], expected) {
			t.Errorf("mutant %s ran %v, want %v", id, started[id], expected)
		}
	}
}

// TestCoverDirFlagIsWhatTheToolchainReads documents, as an executable note, the
// discovery that shaped the profiling pass.
//
// A `go build -cover` program reads GOCOVERDIR; a *test* binary does not. Its
// data is emitted by testing's coverTearDown, which is handed only the value of
// `-test.gocoverdir` and, when that is empty, writes into a temporary directory
// it then deletes — so a profiling pass driven by the environment variable
// prints a coverage percentage, exits 0, and leaves nothing behind.
func TestCoverDirFlagIsWhatTheToolchainReads(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts, coverDir := coverOptions(t, f)
	if _, err := execute.CollectCoverage(t.Context(), opts, testBins("example.com/m/a"), coverDir); err != nil {
		t.Fatalf("CollectCoverage: %v", err)
	}

	c := f.seen()[0]
	if value := envValue(c.Env, "GOCOVERDIR"); value != "" {
		t.Errorf("the profiling run set GOCOVERDIR=%q, which a test binary does not read", value)
	}
	if !slices.ContainsFunc(c.Argv, func(arg string) bool {
		return strings.HasPrefix(arg, "-test.gocoverdir=")
	}) {
		t.Errorf("the profiling run carries no -test.gocoverdir: %v", c.Argv)
	}
}

// TestCollectCoverageCreatesTheWorkerTemporaryDirectory covers the quiet
// dependency the pass has on the same isolation a mutant run gets: a `-cover`
// binary writes into the temporary directory even when told where to put its
// coverage data.
func TestCollectCoverageCreatesTheWorkerTemporaryDirectory(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts, coverDir := coverOptions(t, f)
	if _, err := execute.CollectCoverage(t.Context(), opts, testBins("example.com/m/a"), coverDir); err != nil {
		t.Fatalf("CollectCoverage: %v", err)
	}

	scratch := envValue(f.seen()[0].Env, "TMPDIR")
	if scratch == "" {
		t.Fatal("the profiling run inherited the ambient temporary directory")
	}
	info, err := os.Stat(scratch)
	if err != nil || !info.IsDir() {
		t.Errorf("the redirected temporary directory %s does not exist: %v", scratch, err)
	}
}
