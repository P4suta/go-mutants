// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

// coverProfileFlag is how a test binary is told where to write its coverage
// profile, in the text format internal/coverage reads.
//
// It is this flag rather than `-test.gocoverdir` for one reason, and the reason
// is a *process*: a binary handed a coverage directory writes the raw counter
// and metadata files, which then need `go tool covdata textfmt` to become
// something readable, and that is one more child process per profile. A run
// that profiles every test of a suite pays it once per test. `-test.coverprofile`
// makes the binary write the text format itself, and the two documents are the
// same format from the same data -- what `go test -coverprofile` has written
// since Go 1.2 and what covdata renders into.
//
// It is a flag and deliberately not the GOCOVERDIR environment variable, which
// is the obvious guess and is wrong here for either mechanism. GOCOVERDIR is
// read by internal/coverage/cfile's emitMetaData, the path a program built with
// `go build -cover` takes; a *test* binary emits through testing's
// coverTearDown instead, which is handed only what the flags say and, when they
// say nothing, writes into a temporary directory it then deletes. Setting the
// environment variable on a test binary therefore produces a run that reports a
// coverage percentage and leaves nothing behind, which is the most confusing
// possible failure: it looks like it worked. Verified against go1.26.6.
const coverProfileFlag = "-test.coverprofile="

// A CoverageData is where one test binary left its raw coverage data.
type CoverageData struct {
	// ImportPath is the package whose test binary produced it, which is the
	// name the mapping and the report know a binary by.
	ImportPath string
	// Path is the absolute path of the text-format profile this binary wrote.
	// It is one file per binary rather than one shared one because merging two
	// binaries' data would answer "was this line reached by anything", which is
	// the question coverage-guided selection exists not to ask.
	Path string
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
// The binaries run [Options.Jobs] at a time, as the mutants they are profiled
// for will. Each is a separate process writing a profile of its own under a
// scratch directory of its own, and nothing is shared but the package directory
// they already read from -- so serialising them bought only that they did not
// overlap, which is a cost paid once per *package* before a run measures
// anything. The answers are written by index, so what the mapping sees is a
// function of the tree rather than of the order the workers finished in.
//
// A failure is returned rather than recovered from, and the caller is expected
// to warn and fail open: see internal/coverage's [coverage.CodeUnavailable].
// The directories are created under dir, which must be outside the snapshot —
// data written inside it would be indistinguishable from a test writing into
// the tree, which is exactly what the drift gate exists to catch.
func CollectCoverage(ctx context.Context, opts Options, bins []TestBinary, dir string) ([]CoverageData, error) {
	opts, root, _, err := coverageTarget(opts, dir)
	if err != nil {
		return nil, err
	}

	// One file per binary, named by position rather than by import path; see
	// profilePath.
	paths := make([]string, len(bins))
	for i := range bins {
		binPath, err := profilePath(root, "b", i)
		if err != nil {
			return nil, err
		}
		paths[i] = binPath
	}

	collected := make([]CoverageData, len(bins))
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
				collected[i], failures[i] = profileOneBinary(ctx, workerOpts, bins[i], paths[i], workerScratchPath)
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
	return collected, nil
}

// profileOneBinary runs one test binary with nothing activated and records
// where it left its profile.
func profileOneBinary(
	ctx context.Context, opts Options, bin TestBinary, path, scratch string,
) (CoverageData, error) {
	spec := runner.Spec{
		Argv: []string{bin.BinPath, coverProfileFlag + path},
		Dir:  bin.Dir,
		// No activation, and the same composed environment a mutant gets:
		// a profile taken under a different environment would describe a
		// different program from the one the mutants are measured in.
		Env:     baseEnvFrom(opts.Env, scratch),
		Timeout: opts.Timeout,
		// The same bound the mutants will be measured under, because these
		// are the same binaries. It is worth knowing what a bound that is
		// too small for them does: this pass fails, internal/engine treats
		// a failed coverage pass as it treats every other one — it gives up
		// the narrowing, warns, and measures every mutant against every
		// binary — so the run is slower and reaches exactly the same
		// verdicts. A `-cover` binary is the largest thing a run starts, so
		// it is also the first place a bound set below what this project
		// actually needs shows up.
		MemoryLimit: opts.MemoryLimit,
		Trace:       opts.Trace,
		Kind:        trace.ExecKindCoverageRun,
		Subject:     bin.ImportPath,
	}
	result := opts.runProcess(ctx, spec)
	if err := commandFailure(ctx, spec, result, CodeCoverageFailed,
		"the coverage pass over "+bin.ImportPath+" failed", opts.Timeout); err != nil {
		return CoverageData{}, err
	}
	return CoverageData{ImportPath: bin.ImportPath, Path: path}, nil
}

// coverageTarget is what both profiling passes check before they start a
// binary: options that can profile, a directory outside the snapshot to write
// into, and the temporary directory every run is redirected to.
//
// It returns the resolved options, the absolute directory the profiles go
// under, and the scratch directory, in that order.
func coverageTarget(opts Options, dir string) (Options, string, string, error) {
	opts, err := opts.resolve()
	if err != nil {
		return opts, "", "", err
	}
	if opts.CoverPkg == "" {
		return opts, "", "", &Error{
			Code:    CodeOptions,
			Message: "the test binaries were not built with coverage instrumentation, so there is nothing to collect",
		}
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return opts, "", "", &Error{
			Code: CodeCoverageDir,
			Message: "the coverage directory " + strconv.Quote(dir) +
				" cannot be resolved against the working directory",
			Err: err,
		}
	}
	if insideSnapshot(root, opts.SnapshotRoot) {
		return opts, "", "", &Error{
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
		return opts, "", "", err
	}
	return opts, root, scratch, nil
}

// profilePath names one profile under root, by a prefix and the positions
// given, and makes sure the directory holding it exists.
//
// The name is stable between two runs of one workspace: an import path or a
// test name is not a file name, and the order the callers walk in is the sorted
// order [BuildTestBinaries] returned. The prefix keeps a binary's profile and
// its tests' apart, which matters only to whoever reads a `--keep-temp` run.
func profilePath(root, prefix string, positions ...int) (string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", &Error{
			Code:    CodeCoverageDir,
			Message: "the coverage directory " + strconv.Quote(root) + " could not be created",
			Err:     err,
		}
	}
	name := prefix
	for _, position := range positions {
		name += "-" + strconv.Itoa(position)
	}
	return filepath.Join(root, name+".txt"), nil
}
