// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"

	"github.com/P4suta/go-mutants/internal/runner"
)

// This file is compiled only under `go test`. It exposes the one internal the
// unit tests need — the seam where child processes are created — without
// widening the package's real API.
//
// Injecting the runner rather than exercising real processes is what makes the
// scheduling policy testable at all. A kill, a timeout, a stale-catalogue exit
// and a start failure are four exit statuses, and building four fixture
// programs that produce them on two platforms would test the fixtures. The
// policy — which binary is tried next, which timeout is retried, what two
// disagreeing attempts mean — is what the tests here are about, and it is
// entirely a function of what the runner returns.

// WithRunner returns opts with its process runner replaced.
func WithRunner(opts Options, run func(context.Context, runner.Spec) runner.Result) Options {
	opts.run = run
	return opts
}

// Tail exposes the output-trimming rule the retained tails are produced with.
func Tail(output []byte) string { return tail(output) }

// PlanBinaries exposes the listing-to-binaries step, so that the naming and
// ordering rules can be tested without a toolchain.
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

// WorkerScratchDir exposes the per-worker temporary directory rule.
func WorkerScratchDir(parent string, worker int) string { return workerScratchDir(parent, worker) }

// BaseEnv exposes the scrubbed child environment.
func BaseEnv(scratch string) []string { return baseEnv(scratch) }

// MutantEnv exposes the environment one activated test binary runs with.
func MutantEnv(active, scratch string) []string { return mutantEnv(active, scratch) }

// ProbeEnv exposes the environment one test binary of the probe tree runs with.
func ProbeEnv(scratch, logPath string) []string { return probeEnv(scratch, logPath) }
