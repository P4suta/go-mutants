// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

// testListFlag asks a test binary to print the names of its tests, one per
// line, instead of running them. The value is a regular expression matched
// against every top-level test, benchmark, fuzz target and example; `.`
// matches every name there is, and [ParseTestList] then keeps the ones a run
// can select.
const testListFlag = "-test.list=."

// testRunFlag selects the tests a binary runs, by a regular expression over
// their names.
const testRunFlag = "-test.run="

// runnableTest is the shape of a name a `-test.run` can select and a run
// executes: a test, an example, or a fuzz target run over its seed corpus.
// Benchmarks are listed by `-test.list` too but are not run by a test binary
// unless it is asked to with `-test.bench`, so a benchmark cannot cover a
// mutant and is not a unit the narrowing can run.
var runnableTest = regexp.MustCompile(`^(Test|Example|Fuzz)[^\s]*$`)

// ParseTestList reads what a test binary printed for `-test.list` and returns
// the names a run can execute, in the order the binary printed them, which is
// source order.
//
// It keeps tests, examples and fuzz targets, drops benchmarks, and drops any
// line that is not a bare name — the `ok` status line the go command appends
// when it is the one running the binary, for instance. Nothing is returned for
// empty output: a binary with no tests lists nothing, and that is an answer.
func ParseTestList(output []byte) []string {
	var names []string
	for line := range bytes.Lines(output) {
		name := strings.TrimSpace(string(line))
		if runnableTest.MatchString(name) {
			names = append(names, name)
		}
	}
	return names
}

// A TestCoverageData is where one test of one test binary, run on its own,
// left its raw coverage data — the per-test twin of [CoverageData].
type TestCoverageData struct {
	// ImportPath is the package whose test binary produced it.
	ImportPath string
	// Name is the test's top-level name as `-test.list` printed it: a
	// `TestX`, `ExampleX` or `FuzzX`. Subtests are not units of their own
	// here; their parent is, because the parent is what `-test.run` selects
	// at the granularity a binary can be asked for cheaply.
	Name string
	// Path is the absolute path of the text-format profile this run wrote --
	// one file per test, for the same reason the binary profiles have one
	// per binary: merged data answers the wrong question.
	Path string
	// Passed reports whether the test passed when run alone. A test that
	// does not is a fact rather than an error: it depends on something the
	// rest of the suite does first, or it was never green. Either way it
	// cannot be the sole witness of a mutant, and the caller is expected to
	// leave it out of the narrowing and say so. Its Path may hold a profile
	// or not, depending on whether the binary's own teardown ran; a caller
	// that leaves the test out need not read it.
	Passed bool
	// TimedOut reports that the run was stopped at the timeout, which is
	// one way of not passing and is worth telling apart from a failure: a
	// test that does not finish alone within the bound sized for the whole
	// suite is hanging, not failing.
	TimedOut bool
	// Output is the tail of what the run printed, retained only when it did
	// not pass, so that the warning about it can quote the reason.
	Output string
}

// CollectTestCoverage runs every test of every test binary on its own, with
// no mutant activated, and collects the lines each one reached.
//
// It is the profiling pass of test-level narrowing, which runs a mutant
// against only the tests that reach it rather than against every binary that
// does — and it costs what it sounds like: one `-test.list` per binary, then
// one process per test. That is cheap per test (a process start and a
// coverage teardown) and adds up to about one run of the suite plus that
// overhead, paid once per run.
//
// Each test runs the way a mutant run will: in its package directory, under
// the same environment and the same bound. A test that fails alone, or does
// not finish, is recorded as such rather than failing the pass — see
// [TestCoverageData.Passed] — because the answer is still useful for every
// other test. A binary that cannot list its tests, or a test that cannot be
// started at all, is an error, and the caller is expected to warn and fall
// back to the binary-level pass as it does for every other coverage failure.
//
// The directories are created under dir, which must be outside the snapshot,
// as `<binary position>/<test position>`.
func CollectTestCoverage(ctx context.Context, opts Options, bins []TestBinary, dir string) ([]TestCoverageData, error) {
	opts, root, scratch, err := coverageTarget(opts, dir)
	if err != nil {
		return nil, err
	}

	// The listings first, because the work below needs to know how many runs
	// there will be before it can share them out. They are one per binary
	// rather than one per test, and they go through the same worker pool for
	// the same reason -- a project of thirty packages is thirty process starts
	// before the profiling has begun.
	listings, err := listEveryBinary(ctx, opts, bins, scratch)
	if err != nil {
		return nil, err
	}
	type profiling struct {
		bin  TestBinary
		name string
		path string
	}
	var planned []profiling
	for i, bin := range bins {
		for j, name := range listings[i] {
			path, err := profilePath(root, "t", i, j)
			if err != nil {
				return nil, err
			}
			planned = append(planned, profiling{bin: bin, name: name, path: path})
		}
	}
	if len(planned) == 0 {
		return nil, nil
	}

	// And the runs concurrently, [Options.Jobs] at a time.
	//
	// There is one process per test here and a suite has as many tests as it
	// has, so this is the pass whose cost grows with the *suite* rather than
	// with the catalogue -- the one place a run pays for a project's test count
	// before it measures a single mutant. Each run is a separate process
	// writing a profile of its own under a scratch directory of its own, and
	// nothing is shared but the package directory the binaries already read
	// from, so the only thing serialising them bought was that they did not
	// overlap. Mutant runs of the same binaries already overlap.
	//
	// The results are written by index rather than appended, so the order is
	// the order of the plan above and not the order the workers finished in: a
	// coverage set that came out in a different order on two runs of one tree
	// would make the narrowing -- and the catalogue digest a cache is keyed on
	// -- depend on scheduling.
	collected := make([]TestCoverageData, len(planned))
	failures := make([]error, len(planned))
	var next atomic.Int64
	var wg sync.WaitGroup
	for worker := range min(opts.workers(), len(planned)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerOpts := opts
			workerOpts.ScratchDir = workerScratchDir(opts.ScratchDir, worker)
			workerScratchPath, scratchErr := workerScratch(workerOpts.ScratchDir)
			env := baseEnvFrom(opts.Env, workerScratchPath)
			for {
				if ctx.Err() != nil {
					return
				}
				i := int(next.Add(1)) - 1
				if i >= len(planned) {
					return
				}
				if scratchErr != nil {
					failures[i] = scratchErr
					continue
				}
				collected[i], failures[i] = profileOneTest(ctx, workerOpts, planned[i].bin, planned[i].name, planned[i].path, env)
			}
		}()
	}
	wg.Wait()
	// The first failure by position, so that two runs of one tree report the
	// same one.
	for _, err := range failures {
		if err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return collected, nil
}

// profileOneTest runs one test alone and records what it reached.
func profileOneTest(
	ctx context.Context, opts Options, bin TestBinary, name, path string, env []string,
) (TestCoverageData, error) {
	spec := runner.Spec{
		// The anchors matter: `-test.run=TestA` would also select TestAB and
		// every subtest of both.
		Argv:        []string{bin.BinPath, testRunFlag + "^" + regexp.QuoteMeta(name) + "$", coverProfileFlag + path},
		Dir:         bin.Dir,
		Env:         env,
		Timeout:     opts.Timeout,
		MemoryLimit: opts.MemoryLimit,
		Trace:       opts.Trace,
		Kind:        trace.ExecKindCoverageRun,
		// The import path, a space, the test: an import path holds no space, so
		// the two are recoverable from the subject.
		Subject: bin.ImportPath + " " + name,
	}
	result := opts.runProcess(ctx, spec)
	// Only a run that could not happen is an error here: a test that ran and
	// did not pass is what Passed is for.
	if result.Err != nil || ctx.Err() != nil {
		if err := commandFailure(ctx, spec, result, CodeCoverageFailed,
			"the coverage pass over "+name+" of "+bin.ImportPath+" failed", opts.Timeout); err != nil {
			return TestCoverageData{}, err
		}
	}
	data := TestCoverageData{
		ImportPath: bin.ImportPath,
		Name:       name,
		Path:       path,
		Passed:     result.ExitCode == 0 && !result.TimedOut,
		TimedOut:   result.TimedOut,
	}
	if !data.Passed {
		data.Output = tail(result.Output)
	}
	return data, nil
}

// listEveryBinary asks each binary for the names of its tests, [Options.Jobs]
// at a time, and returns the answers in the order the binaries were given.
//
// The order is the binaries' rather than the order the workers finished in, for
// the reason the profiles below are written by index: what a run profiles has
// to be a function of the tree.
func listEveryBinary(ctx context.Context, opts Options, bins []TestBinary, scratch string) ([][]string, error) {
	listings := make([][]string, len(bins))
	failures := make([]error, len(bins))
	var next atomic.Int64
	var wg sync.WaitGroup
	for worker := range min(opts.workers(), len(bins)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerOpts := opts
			workerOpts.ScratchDir = workerScratchDir(opts.ScratchDir, worker)
			workerScratchPath, scratchErr := workerScratch(workerOpts.ScratchDir)
			env := baseEnvFrom(opts.Env, workerScratchPath)
			for {
				if ctx.Err() != nil {
					return
				}
				i := int(next.Add(1)) - 1
				if i >= len(bins) {
					return
				}
				if scratchErr != nil {
					failures[i] = scratchErr
					continue
				}
				listings[i], failures[i] = listTests(ctx, workerOpts, bins[i], env)
			}
		}()
	}
	wg.Wait()
	for _, err := range failures {
		if err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return listings, nil
}

// listTests asks one test binary for the names of its tests.
func listTests(ctx context.Context, opts Options, bin TestBinary, env []string) ([]string, error) {
	spec := runner.Spec{
		Argv:        []string{bin.BinPath, testListFlag},
		Dir:         bin.Dir,
		Env:         env,
		Timeout:     opts.Timeout,
		MemoryLimit: opts.MemoryLimit,
		Trace:       opts.Trace,
		Kind:        trace.ExecKindTestList,
		Subject:     bin.ImportPath,
	}
	result := opts.runProcess(ctx, spec)
	if err := commandFailure(ctx, spec, result, CodeCoverageFailed,
		"listing the tests of "+bin.ImportPath+" failed", opts.Timeout); err != nil {
		return nil, err
	}
	return ParseTestList(result.Output), nil
}
