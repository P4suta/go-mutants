// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

type ProbeRun struct {
	Timeout time.Duration

	MemoryLimit int64

	Binaries []int

	Args []string

	OutputLimit int

	LogPath string

	RecordTestLog bool

	Digest  string
	Mutants int
}

type ProbeOutcome string

const (
	ProbeMeasured    ProbeOutcome = "measured"
	ProbeTestFailed  ProbeOutcome = "test-failed"
	ProbeTimedOut    ProbeOutcome = "timed-out"
	ProbeUnavailable ProbeOutcome = "unavailable"
)

func ProbeOutcomes() []ProbeOutcome {
	return []ProbeOutcome{
		ProbeMeasured,
		ProbeTestFailed,
		ProbeTimedOut,
		ProbeUnavailable,
	}
}

type ProbeAttempt struct {
	Outcome        ProbeOutcome
	Infected       []uint32
	ExitCode       int
	Duration       time.Duration
	Output         []byte
	OutputBytes    int64
	Truncated      bool
	PeakMemory     int64
	MemoryExceeded bool
	Binaries       []string
	ExecSeqs       []int64
	TestLogs       []TestLog
	Err            error
}

func RunProbe(ctx context.Context, opts Options, p ProbeRun, bins []TestBinary) ProbeAttempt {
	switch {
	case p.Timeout <= 0:
		return probeErrored(&Error{
			Code:    CodeProbeInvalid,
			Message: "the probe pass has no timeout, and go-mutants does not run a test binary unbounded",
		})
	case strings.TrimSpace(p.LogPath) == "":
		return probeErrored(&Error{
			Code: CodeProbeInvalid,
			Message: "the probe pass has no infection log to record into; a pass that recorded nowhere would " +
				"exit having written nothing, which reads exactly like a pass that saw nothing infected",
		})
	case len(bins) == 0:
		return probeErrored(&Error{
			Code: CodeProbeInvalid,
			Message: "the probe pass has no test binaries to measure; reporting no infected mutants having " +
				"started nothing would license skipping every execution of this target",
		})
	}
	if err := validateProbeArgs(p); err != nil {
		return probeErrored(err)
	}
	selected, err := selectProbeBinaries(p, bins)
	if err != nil {
		return probeErrored(err)
	}
	scratch, err := workerScratch(opts.ScratchDir)
	if err != nil {
		return probeErrored(err)
	}

	env := probeEnvFrom(opts.Env, scratch, p.LogPath)
	subject := probeSubject(selected)
	logs := planTestLog(p.RecordTestLog, scratch, p.Args)
	attempt := ProbeAttempt{Outcome: ProbeMeasured}
	for i, bin := range selected {
		if ctx.Err() != nil {
			return attempt.failed(probeInterrupted(ctx, "", nil))
		}

		logPath := logs.path(i)
		spec, result := startTarget(ctx, opts, trace.ExecKindProbeRun, subject, bin, env,
			p.Timeout, p.MemoryLimit, p.Args, nil, logPath, p.OutputLimit, false)
		attempt.Duration += result.Duration
		attempt.PeakMemory = max(attempt.PeakMemory, result.PeakMemory)
		attempt.ExitCode = result.ExitCode
		attempt.Output = slices.Clone(result.Output)
		attempt.OutputBytes = result.OutputBytes
		attempt.Truncated = result.Truncated
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
				Code:       CodeProbeStart,
				Message:    "the probe tree's test binary for " + bin.ImportPath + " could not be run",
				Output:     tail(result.Output),
				Err:        result.Err,
				Invocation: runner.CommandOf(spec, result),
				Package:    bin.ImportPath,
			})

		case result.TimedOut:
			attempt.Outcome = ProbeTimedOut
			return attempt

		case result.MemoryExceeded:
			attempt.Outcome = ProbeTimedOut
			attempt.MemoryExceeded = true
			return attempt

		case result.ExitCode == runner.ExitCodeUnavailable:
			return attempt.failed(probeInterrupted(ctx, bin.ImportPath, runner.CommandOf(spec, result)))

		case result.ExitCode == instrument.ProbeUnavailableExit:
			attempt.Outcome = ProbeUnavailable
			return attempt

		case testLogUnsupported(logPath, result, record):
			return attempt.failed(testLogUnsupportedError("probe pass", bin, spec, result))

		case result.ExitCode != 0:
			attempt.Outcome = ProbeTestFailed
			return attempt
		}
	}

	infected, err := readInfection(p)
	if err != nil {
		return attempt.failed(err)
	}
	attempt.Infected = infected
	return attempt
}

func readInfection(p ProbeRun) ([]uint32, error) {
	file, err := os.Open(p.LogPath)
	if errors.Is(err, fs.ErrNotExist) {
		return []uint32{}, nil
	}
	if err != nil {
		return nil, &Error{
			Code: CodeProbeLog,
			Message: "the infection log " + strconv.Quote(p.LogPath) +
				" is there and could not be opened, so what the pass recorded cannot be read",
			Err: err,
		}
	}
	defer func() { _ = file.Close() }()

	infected, err := instrument.ReadInfectionLog(file, p.Digest, p.Mutants)
	if err != nil {
		return nil, &Error{
			Code: CodeProbeLog,
			Message: "the infection log " + strconv.Quote(p.LogPath) +
				" cannot be read against the catalogue it was written for",
			Err: err,
		}
	}
	if infected == nil {
		infected = []uint32{}
	}
	return infected, nil
}

func ProbePassRecord(p ProbeRun, attempt ProbeAttempt) trace.ProbeRecord {
	record := trace.ProbeRecord{
		Binaries:   slices.Clone(attempt.Binaries),
		Args:       slices.Clone(p.Args),
		TimeoutMS:  p.Timeout.Milliseconds(),
		Outcome:    string(attempt.Outcome),
		ExitCode:   attempt.ExitCode,
		DurationMS: attempt.Duration.Milliseconds(),
		ExecSeqs:   slices.Clone(attempt.ExecSeqs),
	}
	if attempt.Err != nil {
		record.Error = attempt.Err.Error()
	}
	return record
}

func probeSubject(selected []TestBinary) string {
	if len(selected) == 1 {
		return selected[0].ImportPath
	}
	return ""
}

func validateProbeArgs(p ProbeRun) error {
	switch {
	case overridesTimeout(p.Args):
		return &Error{
			Code:    CodeProbeInvalid,
			Message: "the probe target overrides -test.timeout, which is reserved by the process supervisor",
		}
	case p.RecordTestLog && suppliesTestLog(p.Args):
		return &Error{
			Code: CodeProbeInvalid,
			Message: "the probe target supplies " + testLogFlagName +
				", which this pass reserved by asking for the test log to be recorded",
		}
	}
	return nil
}

func selectProbeBinaries(p ProbeRun, bins []TestBinary) ([]TestBinary, error) {
	return selectSubset(p.Binaries, bins,
		func() error {
			return &Error{
				Code: CodeProbeInvalid,
				Message: "the probe pass was given an empty set of test binaries; a pass that started none of " +
					"them would report no infected mutants having measured nothing",
			}
		},
		func(index int) error {
			return &Error{
				Code: CodeProbeInvalid,
				Message: "the probe pass names test binary " + strconv.Itoa(index) + " of " +
					strconv.Itoa(len(bins)) + "; the caller's binaries and this pass's have drifted apart",
			}
		})
}

func probeInterrupted(ctx context.Context, pkg string, command *runner.Invocation) error {
	return &Error{
		Code:       CodeInterrupted,
		Message:    "the probe pass was interrupted",
		Err:        context.Cause(ctx),
		Invocation: command,
		Package:    pkg,
	}
}

func probeErrored(err error) ProbeAttempt {
	return ProbeAttempt{Err: err}
}

func (a ProbeAttempt) failed(err error) ProbeAttempt {
	return ProbeAttempt{Binaries: a.Binaries, ExecSeqs: a.ExecSeqs, TestLogs: a.TestLogs, Err: err}
}
