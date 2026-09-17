// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

type ControlRun struct {
	Timeout time.Duration

	MemoryLimit int64

	Binaries []int

	Tests map[string][]string

	Args []string

	OutputLimit int

	RecordTestLog bool
}

type ControlAttempt struct {
	Package string

	ExitCode int
	TimedOut bool

	Duration time.Duration

	Output      []byte
	OutputBytes int64
	Truncated   bool

	PeakMemory     int64
	MemoryExceeded bool

	Binaries []string
	ExecSeqs []int64

	TestLogs []TestLog

	Err error
}

func RunControl(ctx context.Context, opts Options, c ControlRun, bins []TestBinary) ControlAttempt {
	switch {
	case c.Timeout <= 0:
		return controlErrored(&Error{
			Code:    CodeControlInvalid,
			Message: "the control run has no timeout, and go-mutants does not run a test binary unbounded",
		})
	case len(bins) == 0:
		return controlErrored(&Error{
			Code: CodeControlInvalid,
			Message: "the control run has no test binaries to run the original program in; reporting it " +
				"as having passed while having started nothing would license blaming every mutant for a suite that never ran",
		})
	}
	if err := validateControlArgs(c); err != nil {
		return controlErrored(err)
	}
	selected, err := selectControlBinaries(c, bins)
	if err != nil {
		return controlErrored(err)
	}
	if err = validateTestSelection(c.Tests, selected, func(importPath, why string) error {
		return &Error{Code: CodeControlInvalid, Message: "the control run names tests of " + importPath + " " + why}
	}); err != nil {
		return controlErrored(err)
	}
	scratch, err := workerScratch(opts.ScratchDir)
	if err != nil {
		return controlErrored(err)
	}

	env := controlEnvFrom(opts.Env, scratch)
	logs := planTestLog(c.RecordTestLog, scratch, c.Args)
	attempt := ControlAttempt{}
	var last runner.Result
	for i, bin := range selected {
		if ctx.Err() != nil {
			return attempt.failed(controlInterrupted(ctx, "", nil))
		}

		logPath := logs.path(i)
		spec, result := startTarget(ctx, opts, trace.ExecKindControlRun, bin.ImportPath,
			bin, env, c.Timeout, c.MemoryLimit, c.Args, c.Tests[bin.ImportPath], logPath, c.OutputLimit, true)
		last = result
		attempt.Duration += result.Duration
		attempt.PeakMemory = max(attempt.PeakMemory, result.PeakMemory)
		attempt.ExitCode = result.ExitCode
		attempt.Binaries = append(attempt.Binaries, bin.ImportPath)
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
			return attempt.failed(&Error{
				Code:       CodeControlStart,
				Message:    "the control run's test binary for " + bin.ImportPath + " could not be run",
				Output:     tail(result.Output),
				Err:        result.Err,
				Invocation: runner.CommandOf(spec, result),
				Package:    bin.ImportPath,
			})

		case result.TimedOut:
			attempt.TimedOut = true
			attempt.Package = bin.ImportPath
			attempt.keep(result)
			return attempt

		case result.MemoryExceeded:
			attempt.MemoryExceeded = true
			attempt.Package = bin.ImportPath
			attempt.keep(result)
			return attempt

		case result.ExitCode == runner.ExitCodeUnavailable:
			return attempt.failed(controlInterrupted(ctx, bin.ImportPath, runner.CommandOf(spec, result)))

		case testLogUnsupported(logPath, result, record):
			return attempt.failed(testLogUnsupportedError("control run", bin, spec, result))

		case result.ExitCode != 0:
			attempt.Package = bin.ImportPath
			attempt.keep(result)
			return attempt
		}
	}
	attempt.keep(last)
	return attempt
}

func (a *ControlAttempt) keep(result runner.Result) {
	a.Output = slices.Clone(result.Output)
	a.OutputBytes = result.OutputBytes
	a.Truncated = result.Truncated
}

func ControlRecord(c ControlRun, attempt ControlAttempt) trace.NoteRecord {
	var detail strings.Builder
	switch {
	case attempt.Err != nil:
		detail.WriteString(attempt.Err.Error())
	case attempt.TimedOut:
		detail.WriteString("timed out after " + c.Timeout.String())
	default:
		detail.WriteString("exit " + strconv.Itoa(attempt.ExitCode))
	}
	if attempt.Package != "" {
		detail.WriteString(" in " + attempt.Package)
	}
	if len(attempt.Binaries) > 0 {
		detail.WriteString("; binaries: " + strings.Join(attempt.Binaries, ", "))
	}
	if len(attempt.ExecSeqs) > 0 {
		seqs := make([]string, len(attempt.ExecSeqs))
		for i, seq := range attempt.ExecSeqs {
			seqs[i] = strconv.FormatInt(seq, 10)
		}
		detail.WriteString("; exec: " + strings.Join(seqs, ", "))
	}
	return trace.NoteRecord{
		Kind:   trace.NoteControl,
		Code:   CodeOf(attempt.Err).String(),
		Detail: detail.String(),
	}
}

func validateControlArgs(c ControlRun) error {
	switch {
	case overridesTimeout(c.Args):
		return &Error{
			Code:    CodeControlInvalid,
			Message: "the control target overrides -test.timeout, which is reserved by the process supervisor",
		}
	case len(c.Tests) > 0 && suppliesTestRun(c.Args):
		return &Error{
			Code:    CodeControlInvalid,
			Message: "the control target supplies -test.run, which this run reserved by narrowing the control to named tests",
		}
	case c.RecordTestLog && suppliesTestLog(c.Args):
		return &Error{
			Code: CodeControlInvalid,
			Message: "the control target supplies " + testLogFlagName +
				", which this run reserved by asking for the test log to be recorded",
		}
	}
	return nil
}

func selectControlBinaries(c ControlRun, bins []TestBinary) ([]TestBinary, error) {
	return selectSubset(c.Binaries, bins,
		func() error {
			return &Error{
				Code: CodeControlInvalid,
				Message: "the control run was given an empty set of test binaries; a control that started " +
					"none of them would report the original program as passing having run nothing",
			}
		},
		func(index int) error {
			return &Error{
				Code: CodeControlInvalid,
				Message: "the control run names test binary " + strconv.Itoa(index) + " of " +
					strconv.Itoa(len(bins)) + "; the caller's binaries and this run's have drifted apart",
			}
		})
}

func controlInterrupted(ctx context.Context, pkg string, command *runner.Invocation) error {
	return &Error{
		Code:       CodeInterrupted,
		Message:    "the control run was interrupted",
		Err:        context.Cause(ctx),
		Invocation: command,
		Package:    pkg,
	}
}

func controlErrored(err error) ControlAttempt {
	return ControlAttempt{Err: err}
}

func (a ControlAttempt) failed(err error) ControlAttempt {
	return ControlAttempt{Binaries: a.Binaries, ExecSeqs: a.ExecSeqs, TestLogs: a.TestLogs, Err: err}
}
