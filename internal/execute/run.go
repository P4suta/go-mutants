// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testflag"
	"github.com/P4suta/go-mutants/trace"
)

const InProcessTimeoutFactor = 2

type MutantRun struct {
	ID        string
	DisplayID string
	Package   string
	Timeout   time.Duration

	NeverReturns bool

	MemoryLimit int64

	Binaries []int

	Tests map[string][]string

	Args []string

	OutputLimit int

	RecordTestLog bool
}

type Attempt struct {
	Worker         int
	Outcome        mutation.Outcome
	KilledBy       string
	Duration       time.Duration
	Binaries       []string
	Tests          map[string][]string
	ExecSeqs       []int64
	TestLogs       []TestLog
	OutputTail     string
	Output         []byte
	OutputBytes    int64
	Truncated      bool
	PeakMemory     int64
	MemoryExceeded bool

	Diverged bool
	Err      error
}

func RunOne(ctx context.Context, opts Options, m MutantRun, bins []TestBinary) Attempt {
	attempt := runNarrowed(ctx, opts, m, bins)
	if len(m.Tests) == 0 || attempt.Outcome != mutation.OutcomeSurvived {
		return attempt
	}
	whole := m
	whole.Tests = nil
	return runNarrowed(ctx, opts, whole, bins)
}

var ErrTreeNotRestored = errors.New("execute: the worker's copy of the tree could not be put back")

func restoreTree(opts Options) error {
	if opts.Restore == nil {
		return nil
	}
	if err := opts.Restore(); err != nil {
		return fmt.Errorf("%w: %w", ErrTreeNotRestored, err)
	}
	return nil
}

func runNarrowed(ctx context.Context, opts Options, m MutantRun, bins []TestBinary) Attempt {
	attempt := runPass(ctx, opts, m, bins)
	if len(attempt.Binaries) == 0 {
		return attempt
	}
	if err := restoreTree(opts); err != nil {
		return errored(err)
	}
	return attempt
}

func runPass(ctx context.Context, opts Options, m MutantRun, bins []TestBinary) Attempt {
	switch {
	case strings.TrimSpace(m.ID) == "":
		return errored(&Error{Code: CodeMutantInvalid, Message: "the mutant has no activation identity"})
	case m.Timeout <= 0:
		return errored(&Error{
			Code:    CodeMutantInvalid,
			Message: "the mutant " + display(m.ID) + " has no timeout, and go-mutants does not run a test binary unbounded",
		})
	case len(bins) == 0:
		return errored(&Error{
			Code: CodeNoTestBinaries,
			Message: "the mutant " + display(m.ID) +
				" has no test binaries to be measured against; reporting it as survived would be a green produced by running nothing",
		})
	}
	if err := validateArgs(m); err != nil {
		return errored(err)
	}

	selected, err := selectBinaries(m, bins)
	if err != nil {
		return errored(err)
	}
	if err = validateTestSelection(m.Tests, selected, func(importPath, why string) error {
		return &Error{
			Code:    CodeMutantInvalid,
			Message: "the mutant " + display(m.ID) + " names tests of " + importPath + " " + why,
		}
	}); err != nil {
		return errored(err)
	}

	scratch, err := workerScratch(opts.ScratchDir)
	if err != nil {
		return errored(err)
	}

	env := mutantEnvFrom(opts.Env, m.ID, scratch)
	if opts.LoopLimits != "" {
		env = append(env, instrument.LoopLimitsEnv+"="+opts.LoopLimits)
	}
	logs := planTestLog(m.RecordTestLog, scratch, m.Args)

	attempt := Attempt{Outcome: mutation.OutcomeSurvived}
	for i, bin := range selected {
		if ctx.Err() != nil {
			attempt.Outcome = mutation.OutcomeNotRun
			return attempt
		}

		logPath := logs.path(i)
		tests := m.Tests[bin.ImportPath]
		spec, result := startTarget(ctx, opts, trace.ExecKindMutantRun, m.ID, bin, env,
			m.Timeout, m.MemoryLimit, m.Args, tests, logPath, m.OutputLimit, true)
		attempt.Duration += result.Duration
		attempt.PeakMemory = max(attempt.PeakMemory, result.PeakMemory)
		attempt.Binaries = append(attempt.Binaries, bin.ImportPath)
		if len(tests) > 0 {
			if attempt.Tests == nil {
				attempt.Tests = make(map[string][]string)
			}
			attempt.Tests[bin.ImportPath] = slices.Clone(tests)
		}
		if result.TraceSeq != 0 {
			attempt.ExecSeqs = append(attempt.ExecSeqs, result.TraceSeq)
		}
		var record TestLog
		if logs.record {
			record = logs.read(bin, logPath, result)
			attempt.TestLogs = append(attempt.TestLogs, record)
		}

		switch {
		case result.Err != nil:
			attempt.Outcome = mutation.OutcomeErrored
			attempt.keep(result)
			attempt.Err = &Error{
				Code:       CodeMutantStart,
				Message:    "the test binary for " + bin.ImportPath + " could not be run",
				Output:     attempt.OutputTail,
				Err:        result.Err,
				Invocation: runner.CommandOf(spec, result),
				Package:    bin.ImportPath,
			}
			return attempt

		case result.TimedOut:
			attempt.Outcome = mutation.OutcomeTimedOut
			attempt.KilledBy = bin.ImportPath
			attempt.keep(result)
			return attempt

		case result.MemoryExceeded:
			attempt.Outcome = mutation.OutcomeKilled
			attempt.KilledBy = bin.ImportPath
			attempt.MemoryExceeded = true
			attempt.keep(result)
			return attempt

		case result.ExitCode == runner.ExitCodeUnavailable:
			attempt.Outcome = mutation.OutcomeNotRun
			attempt.Err = &Error{
				Code:       CodeInterrupted,
				Message:    "the test binary for " + bin.ImportPath + " was interrupted",
				Output:     tail(result.Output),
				Err:        context.Cause(ctx),
				Invocation: runner.CommandOf(spec, result),
				Package:    bin.ImportPath,
			}
			return attempt

		case result.ExitCode == instrument.UnknownMutantExit:
			attempt.Outcome = mutation.OutcomeErrored
			attempt.keep(result)
			attempt.Err = &Error{
				Code: CodeStaleCatalog,
				Message: "the generated runtime in " + bin.ImportPath + " does not know the mutant " +
					display(m.ID) + "; the catalogue and the instrumented snapshot disagree",
				Output:     attempt.OutputTail,
				Invocation: runner.CommandOf(spec, result),
				Package:    bin.ImportPath,
			}
			return attempt

		case result.ExitCode == instrument.DivergedExit:
			attempt.Outcome = mutation.OutcomeTimedOut
			attempt.KilledBy = bin.ImportPath
			attempt.Diverged = true
			attempt.keep(result)
			return attempt

		case testLogUnsupported(logPath, result, record):
			attempt.Outcome = mutation.OutcomeErrored
			attempt.keep(result)
			attempt.Err = testLogUnsupportedError("mutant run", bin, spec, result)
			return attempt

		case result.ExitCode != 0:
			attempt.Outcome = mutation.OutcomeKilled
			attempt.KilledBy = bin.ImportPath
			attempt.keep(result)
			return attempt
		}
	}
	return attempt
}

func (a *Attempt) keep(result runner.Result) {
	a.Output = slices.Clone(result.Output)
	a.OutputBytes = result.OutputBytes
	a.Truncated = result.Truncated
	a.OutputTail = tail(result.Output)
}

const failFastFlag = "-test.failfast"

func workingDir(opts Options, bin TestBinary) string {
	if opts.Tree == "" || opts.SnapshotRoot == "" || opts.Tree == opts.SnapshotRoot {
		return bin.Dir
	}
	rel, ok := under(opts.SnapshotRoot, bin.Dir)
	if !ok {
		return bin.Dir
	}
	return filepath.Join(opts.Tree, rel)
}

func under(root, dir string) (string, bool) {
	if rel, ok := relativeTo(root, dir); ok {
		return rel, true
	}
	resolvedRoot, rootErr := filepath.EvalSymlinks(root)
	resolvedDir, dirErr := filepath.EvalSymlinks(dir)
	if rootErr != nil || dirErr != nil {
		return "", false
	}
	return relativeTo(resolvedRoot, resolvedDir)
}

func relativeTo(root, dir string) (string, bool) {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func startTarget(
	ctx context.Context,
	opts Options,
	kind string,
	subject string,
	bin TestBinary,
	env []string,
	timeout time.Duration,
	memoryLimit int64,
	args []string,
	tests []string,
	testLogPath string,
	outputLimit int,
	stopAtFirstFailure bool,
) (runner.Spec, runner.Result) {
	argv := make([]string, 0, len(args)+5)
	argv = append(argv, bin.BinPath, "-test.timeout="+(InProcessTimeoutFactor*timeout).String())
	if stopAtFirstFailure {
		argv = append(argv, failFastFlag)
	}
	if testLogPath != "" {
		argv = append(argv, testLogFlagName+"="+testLogPath)
	}
	if len(tests) > 0 {
		argv = append(argv, testRunSelector(tests))
	}
	argv = append(argv, args...)
	spec := runner.Spec{
		Argv:        argv,
		Dir:         workingDir(opts, bin),
		Env:         env,
		Timeout:     timeout,
		MemoryLimit: memoryLimit,
		OutputLimit: outputLimit,
		Trace:       opts.Trace,
		Kind:        kind,
		Subject:     subject,
	}
	return spec, opts.runProcess(ctx, spec)
}

func overridesTimeout(args []string) bool {
	return slices.ContainsFunc(args, func(argument string) bool {
		return testflag.Match(argument, "test.timeout")
	})
}

func validateArgs(m MutantRun) error {
	switch {
	case overridesTimeout(m.Args):
		return &Error{
			Code: CodeMutantInvalid,
			Message: "the mutant " + display(m.ID) +
				" target overrides -test.timeout, which is reserved by the process supervisor",
		}
	case len(m.Tests) > 0 && suppliesTestRun(m.Args):
		return &Error{
			Code: CodeMutantInvalid,
			Message: "the mutant " + display(m.ID) +
				" target supplies -test.run, which this run reserved by narrowing the measurement to named tests",
		}
	case m.RecordTestLog && suppliesTestLog(m.Args):
		return &Error{
			Code: CodeMutantInvalid,
			Message: "the mutant " + display(m.ID) + " target supplies " + testLogFlagName +
				", which this run reserved by asking for the test log to be recorded",
		}
	}
	return nil
}

func selectSubset(
	subset []int, bins []TestBinary, refuseEmpty func() error, refuseIndex func(index int) error,
) ([]TestBinary, error) {
	if subset == nil {
		return bins, nil
	}
	if len(subset) == 0 {
		return nil, refuseEmpty()
	}
	selected := make([]TestBinary, 0, len(subset))
	for _, index := range subset {
		if index < 0 || index >= len(bins) {
			return nil, refuseIndex(index)
		}
		selected = append(selected, bins[index])
	}
	return selected, nil
}

func selectBinaries(m MutantRun, bins []TestBinary) ([]TestBinary, error) {
	return selectSubset(m.Binaries, bins,
		func() error {
			return &Error{
				Code: CodeMutantInvalid,
				Message: "the mutant " + display(m.ID) +
					" was given an empty set of test binaries to be measured against; a mutant no binary covers is not executed at all, and running none of them would report it as survived having started nothing",
			}
		},
		func(index int) error {
			return &Error{
				Code: CodeMutantInvalid,
				Message: "the mutant " + display(m.ID) + " names test binary " + strconv.Itoa(index) +
					" of " + strconv.Itoa(len(bins)) + "; the caller's binaries and this run's have drifted apart",
			}
		})
}

func workerScratch(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil || abs == "" {
		return "", &Error{
			Code: CodeScratchDir,
			Message: "the worker's temporary directory " + strconv.Quote(dir) +
				" could not be resolved against the working directory",
			Err: err,
		}
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", &Error{
			Code:    CodeScratchDir,
			Message: "the worker's temporary directory " + strconv.Quote(abs) + " could not be created",
			Err:     err,
		}
	}
	return abs, nil
}

func errored(err error) Attempt {
	return Attempt{Outcome: mutation.OutcomeErrored, Err: err}
}

func display(id string) string {
	const shown = 20
	if len(id) <= shown {
		return id
	}
	return id[:shown]
}
