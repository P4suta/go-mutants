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

const coverProfileFlag = "-test.coverprofile="

type CoverageData struct {
	ImportPath string
	Path       string
}

func CollectCoverage(ctx context.Context, opts Options, bins []TestBinary, dir string) ([]CoverageData, error) {
	opts, root, _, err := coverageTarget(opts, dir)
	if err != nil {
		return nil, err
	}

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

func profileOneBinary(
	ctx context.Context, opts Options, bin TestBinary, path, scratch string,
) (CoverageData, error) {
	spec := runner.Spec{
		Argv:        []string{bin.BinPath, coverProfileFlag + path},
		Dir:         bin.Dir,
		Env:         baseEnvFrom(opts.Env, scratch),
		Timeout:     opts.Timeout,
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

	scratch, err := workerScratch(opts.ScratchDir)
	if err != nil {
		return opts, "", "", err
	}
	return opts, root, scratch, nil
}

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
