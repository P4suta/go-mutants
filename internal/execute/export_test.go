// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"

	"github.com/P4suta/go-mutants/internal/runner"
)

func WithRunner(opts Options, run func(context.Context, runner.Spec) runner.Result) Options {
	opts.run = run
	return opts
}

func Tail(output []byte) string { return tail(output) }

func PlanBinaries(importPaths, dirs []string, hasTests []bool, binDir string) []TestBinary {
	packages := make([]listedPackage, len(importPaths))
	for i := range importPaths {
		packages[i] = listedPackage{ImportPath: importPaths[i], Dir: dirs[i]}
		if hasTests[i] {
			packages[i].TestGoFiles = []string{"x_test.go"}
		}
	}
	return plan(packages, binDir)
}

func WorkerScratchDir(parent string, worker int) string { return workerScratchDir(parent, worker) }

func BaseEnv(scratch string) []string { return baseEnv(scratch) }

func MutantEnv(active, scratch string) []string { return mutantEnv(active, scratch) }

func ProbeEnv(scratch, logPath string) []string { return probeEnv(scratch, logPath) }

func ControlEnv(scratch string) []string { return controlEnv(scratch) }

const FailFastFlag = failFastFlag
