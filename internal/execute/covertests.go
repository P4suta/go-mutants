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

const testListFlag = "-test.list=."

const testRunFlag = "-test.run="

var runnableTest = regexp.MustCompile(`^(Test|Example|Fuzz)[^\s]*$`)

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

type TestCoverageData struct {
	ImportPath string
	Name       string
	Path       string
	Passed     bool
	TimedOut   bool
	Output     string
}

func CollectTestCoverage(ctx context.Context, opts Options, bins []TestBinary, dir string) ([]TestCoverageData, error) {
	opts, root, scratch, err := coverageTarget(opts, dir)
	if err != nil {
		return nil, err
	}

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

func profileOneTest(
	ctx context.Context, opts Options, bin TestBinary, name, path string, env []string,
) (TestCoverageData, error) {
	spec := runner.Spec{
		Argv:        []string{bin.BinPath, testRunFlag + "^" + regexp.QuoteMeta(name) + "$", coverProfileFlag + path},
		Dir:         bin.Dir,
		Env:         env,
		Timeout:     opts.Timeout,
		MemoryLimit: opts.MemoryLimit,
		Trace:       opts.Trace,
		Kind:        trace.ExecKindCoverageRun,
		Subject:     bin.ImportPath + " " + name,
	}
	result := opts.runProcess(ctx, spec)
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
