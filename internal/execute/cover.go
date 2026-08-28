// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"os"
	"path/filepath"
	"strconv"

	"github.com/P4suta/go-mutants/internal/runner"
)

// coverDirFlag is how a test binary is told where to leave its coverage data.
//
// It is a flag and deliberately not the GOCOVERDIR environment variable, which
// is the obvious guess and is wrong here. GOCOVERDIR is read by
// internal/coverage/cfile's emitMetaData, the path a program built with
// `go build -cover` takes; a *test* binary emits through testing's coverTearDown
// instead, which is handed only the value of `-test.gocoverdir` and, when that
// is empty, writes into a temporary directory it then deletes. Setting the
// environment variable on a test binary therefore produces a run that reports a
// coverage percentage and leaves nothing behind, which is the most confusing
// possible failure: it looks like it worked. Verified against go1.26.5.
const coverDirFlag = "-test.gocoverdir="

// A CoverageData is where one test binary left its raw coverage data.
type CoverageData struct {
	// ImportPath is the package whose test binary produced it, which is the
	// name the mapping and the report know a binary by.
	ImportPath string
	// Dir is the absolute directory holding the covmeta and covcounters files
	// this binary wrote. It is `go tool covdata`'s input, and it is one
	// directory per binary rather than one shared one because merging two
	// binaries' data would answer "was this line reached by anything", which is
	// the question coverage-guided selection exists not to ask.
	Dir string
}

// CollectCoverage runs every test binary once, with no mutant activated, and
// collects the lines each one reached.
//
// It is the profiling pass of coverage-guided selection, and it is worth being
// explicit about what it costs: one full run of every test binary, on top of
// the baseline runs and the instrumented baseline. That is paid once, and it
// buys skipping every mutant no test reaches — which on a real workspace is
// where most of a run's wall-clock time goes.
//
// Each binary runs in its own package directory, as a mutant run does, because
// a Go test resolves testdata relative to where it runs. Nothing is activated:
// the environment carries no [instrument.ActiveEnv], so every guard takes the
// branch holding the user's own bytes and the coverage collected is the
// coverage of the unmutated program. That is the only coverage that means
// anything — a profile taken with a mutant live would describe the mutant.
//
// The binaries run one after another rather than concurrently. Coverage is
// about which lines a suite reaches and not about how long it takes, so there is
// nothing to gain from overlapping them, and running them serially keeps this
// pass from competing with itself for the machine the timeout was derived on.
//
// A failure is returned rather than recovered from, and the caller is expected
// to warn and fail open: see internal/coverage's [coverage.CodeUnavailable].
// The directories are created under dir, which must be outside the snapshot —
// data written inside it would be indistinguishable from a test writing into
// the tree, which is exactly what the drift gate exists to catch.
func CollectCoverage(ctx context.Context, opts Options, bins []TestBinary, dir string) ([]CoverageData, error) {
	opts, err := opts.resolve()
	if err != nil {
		return nil, err
	}
	if opts.CoverPkg == "" {
		return nil, &Error{
			Code:    CodeOptions,
			Message: "the test binaries were not built with coverage instrumentation, so there is nothing to collect",
		}
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, &Error{
			Code: CodeCoverageDir,
			Message: "the coverage directory " + strconv.Quote(dir) +
				" cannot be resolved against the working directory",
			Err: err,
		}
	}
	if insideSnapshot(root, opts.SnapshotRoot) {
		return nil, &Error{
			Code: CodeCoverageDir,
			Message: "the coverage directory " + strconv.Quote(root) +
				" is inside the snapshot; coverage data written into the tree is indistinguishable from a test that wrote into it",
		}
	}

	// The same temporary-directory redirection every mutant run gets, resolved
	// and created once. A `-cover` binary writes into the temporary directory
	// even when it is told where to put its coverage data, so this pass needs
	// one that exists as much as an execution worker does.
	scratch, err := workerScratch(opts.ScratchDir)
	if err != nil {
		return nil, err
	}

	collected := make([]CoverageData, 0, len(bins))
	for i, bin := range bins {
		// One directory per binary, named by position rather than by import
		// path: an import path is not a file name, and the order here is the
		// sorted order [BuildTestBinaries] returned, so the names are stable
		// between two runs of one workspace.
		binDir := filepath.Join(root, strconv.Itoa(i))
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			return nil, &Error{
				Code:    CodeCoverageDir,
				Message: "the coverage directory " + strconv.Quote(binDir) + " could not be created",
				Err:     err,
			}
		}

		result := opts.runProcess(ctx, runner.Spec{
			Argv: []string{bin.BinPath, coverDirFlag + binDir},
			Dir:  bin.Dir,
			// No activation, and the same composed environment a mutant gets:
			// a profile taken under a different environment would describe a
			// different program from the one the mutants are measured in.
			Env:     baseEnvFrom(opts.Env, scratch),
			Timeout: opts.Timeout,
		})
		if err := commandFailure(ctx, result, CodeCoverageFailed,
			"the coverage pass over "+bin.ImportPath+" failed", opts.Timeout); err != nil {
			return nil, err
		}
		collected = append(collected, CoverageData{ImportPath: bin.ImportPath, Dir: binDir})
	}
	return collected, nil
}
