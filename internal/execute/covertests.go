// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"bytes"
	"context"
	"regexp"
	"strings"

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
	// Dir is the absolute directory holding the covmeta and covcounters
	// files this run wrote — one directory per test, for the same reason the
	// binary profiles have one per binary: merged data answers the wrong
	// question.
	Dir string
	// Passed reports whether the test passed when run alone. A test that
	// does not is a fact rather than an error: it depends on something the
	// rest of the suite does first, or it was never green. Either way it
	// cannot be the sole witness of a mutant, and the caller is expected to
	// leave it out of the narrowing and say so. Its Dir may hold a profile
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
	env := baseEnvFrom(opts.Env, scratch)

	var collected []TestCoverageData
	for i, bin := range bins {
		names, err := listTests(ctx, opts, bin, env)
		if err != nil {
			return nil, err
		}
		for j, name := range names {
			testDir, err := profileDir(root, i, j)
			if err != nil {
				return nil, err
			}
			spec := runner.Spec{
				// The anchors matter: `-test.run=TestA` would also select
				// TestAB and every subtest of both.
				Argv:        []string{bin.BinPath, testRunFlag + "^" + regexp.QuoteMeta(name) + "$", coverDirFlag + testDir},
				Dir:         bin.Dir,
				Env:         env,
				Timeout:     opts.Timeout,
				MemoryLimit: opts.MemoryLimit,
				Trace:       opts.Trace,
				Kind:        trace.ExecKindCoverageRun,
				// The import path, a space, the test: an import path holds
				// no space, so the two are recoverable from the subject.
				Subject: bin.ImportPath + " " + name,
			}
			result := opts.runProcess(ctx, spec)
			// Only a run that could not happen is an error here: a test that
			// ran and did not pass is what Passed is for.
			if result.Err != nil || ctx.Err() != nil {
				if err := commandFailure(ctx, spec, result, CodeCoverageFailed,
					"the coverage pass over "+name+" of "+bin.ImportPath+" failed", opts.Timeout); err != nil {
					return nil, err
				}
			}
			data := TestCoverageData{
				ImportPath: bin.ImportPath,
				Name:       name,
				Dir:        testDir,
				Passed:     result.ExitCode == 0 && !result.TimedOut,
				TimedOut:   result.TimedOut,
			}
			if !data.Passed {
				data.Output = tail(result.Output)
			}
			collected = append(collected, data)
		}
	}
	return collected, nil
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
