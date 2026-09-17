// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/trace"
)

const ExitCodeUnavailable = -1

type Spec struct {
	Argv []string

	Dir string

	Env []string

	Timeout time.Duration

	MemoryLimit int64

	OutputLimit int

	SeparateStdout bool

	Trace *trace.Recorder

	Kind string

	Subject string
}

type Invocation struct {
	Argv []string

	Dir string

	Kind string

	TraceSeq int64
}

func InvocationOf(spec Spec, result Result) Invocation {
	return Invocation{
		Argv:     slices.Clone(spec.Argv),
		Dir:      spec.Dir,
		Kind:     spec.Kind,
		TraceSeq: result.TraceSeq,
	}
}

func CommandOf(spec Spec, result Result) *Invocation {
	var failure *Error
	if errors.As(result.Err, &failure) && failure.Invocation != nil {
		return failure.Invocation
	}
	invocation := InvocationOf(spec, result)
	return &invocation
}

type Result struct {
	ExitCode int

	TimedOut bool

	MemoryExceeded bool

	PeakMemory int64

	Duration time.Duration

	Output []byte

	Stdout []byte

	OutputBytes int64

	Truncated bool

	Err error

	TraceSeq int64
}

func (r Result) OK() bool {
	return r.Err == nil && !r.TimedOut && !r.MemoryExceeded && r.ExitCode == 0
}

func Run(ctx context.Context, spec Spec) Result {
	return record(spec, runProcess(ctx, spec))
}

func record(spec Spec, result Result) Result {
	result.TraceSeq = spec.Trace.Exec(trace.ExecRecord{
		Kind:            spec.Kind,
		Subject:         spec.Subject,
		Argv:            spec.Argv,
		Dir:             spec.Dir,
		EnvNames:        environmentOf(spec),
		TimeoutMS:       milliseconds(spec.Timeout),
		ExitCode:        result.ExitCode,
		TimedOut:        result.TimedOut,
		DurationMS:      milliseconds(result.Duration),
		PeakMemoryBytes: result.PeakMemory,
		Output:          result.Output,
		Error:           errorText(result.Err),
	})
	if result.Err == nil {
		return result
	}
	var failure *Error
	if errors.As(result.Err, &failure) {
		if failure.Invocation == nil {
			invocation := InvocationOf(spec, result)
			failure.Invocation = &invocation
		}
		if failure.Output == "" {
			failure.Output = string(result.Output)
		}
	}
	return result
}

func environmentOf(spec Spec) []string {
	if spec.Env != nil {
		return spec.Env
	}
	return os.Environ()
}

func milliseconds(d time.Duration) int64 {
	if ms := d.Milliseconds(); ms > 0 {
		return ms
	}
	return 0
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func runProcess(ctx context.Context, spec Spec) Result {
	started := time.Now()

	if err := validate(spec); err != nil {
		return Result{ExitCode: ExitCodeUnavailable, Duration: time.Since(started), Err: err}
	}
	if ctx.Err() != nil {
		return Result{ExitCode: ExitCodeUnavailable, Duration: time.Since(started)}
	}

	limit := effectiveOutputLimit(spec.OutputLimit)
	out := newTailWriter(limit)
	var stdoutOnly *tailWriter
	if spec.SeparateStdout {
		stdoutOnly = newTailWriter(limit)
	}
	captured := func(result Result) Result {
		result.Output, result.OutputBytes, result.Truncated = out.capture()
		if stdoutOnly != nil {
			result.Stdout, _, _ = stdoutOnly.capture()
		}
		return result
	}

	sup, err := newSupervisor(spec.MemoryLimit)
	if err != nil {
		return Result{ExitCode: ExitCodeUnavailable, Duration: time.Since(started), Err: err}
	}
	defer sup.release()

	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdin = nil
	cmd.Stdout = out
	cmd.Stderr = out
	if stdoutOnly != nil {
		cmd.Stdout = io.MultiWriter(out, stdoutOnly)
	}
	cmd.WaitDelay = IODrainGrace
	sup.configure(cmd)

	if err := cmd.Start(); err != nil {
		return Result{
			ExitCode: ExitCodeUnavailable,
			Duration: time.Since(started),
			Err: &Error{
				Code:    CodeProcessStartFailed,
				Message: "could not start " + spec.Argv[0],
				Err:     err,
			},
		}
	}

	if err := sup.adopt(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return captured(Result{
			ExitCode: ExitCodeUnavailable,
			Duration: time.Since(started),
			Err:      err,
		})
	}

	var waitErr error
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		waitErr = cmd.Wait()
	}()

	var timeoutC <-chan time.Time
	if spec.Timeout > 0 {
		timer := time.NewTimer(spec.Timeout)
		defer timer.Stop()
		timeoutC = timer.C
	}

	watchdog := watchMemory(sup, max(spec.MemoryLimit, 0))

	var timedOut, memoryExceeded, killed bool
	select {
	case <-exited:
	case <-timeoutC:
		timedOut, killed = true, true
		sup.terminate(exited, TerminationGrace)
		<-exited
	case <-watchdog.exceededC():
		memoryExceeded, killed = true, true
		sup.terminate(exited, 0)
		<-exited
	case <-ctx.Done():
		killed = true
		sup.terminate(exited, TerminationGrace)
		<-exited
	}

	watchdog.stop()

	result := captured(Result{
		ExitCode:       ExitCodeUnavailable,
		TimedOut:       timedOut,
		MemoryExceeded: memoryExceeded,
		PeakMemory:     peakOf(sup, cmd.ProcessState, watchdog),
		Duration:       time.Since(started),
	})
	if !killed {
		result.ExitCode = exitCodeOf(cmd.ProcessState)
		result.Err = waitFailure(waitErr)
	}
	result.MemoryExceeded = result.MemoryExceeded ||
		exceededAtExit(kernelBoundsMemory, spec.MemoryLimit, result.PeakMemory, result.ExitCode, killed)
	return result
}

func peakOf(sup supervisor, ps *os.ProcessState, watchdog *memoryWatchdog) int64 {
	peak := watchdog.observedPeak()
	if !accountedPeakBelongsToTheChild {
		return peak
	}
	if accounted, ok := sup.peakMemory(ps); ok {
		peak = max(peak, accounted)
	}
	return peak
}

func waitFailure(err error) error {
	var exitErr *exec.ExitError
	switch {
	case err == nil, errors.As(err, &exitErr), errors.Is(err, exec.ErrWaitDelay):
		return nil
	default:
		return &Error{
			Code:    CodeProcessWaitFailed,
			Message: "could not collect the child process's exit status",
			Err:     err,
		}
	}
}

func validate(spec Spec) error {
	if len(spec.Argv) == 0 {
		return &Error{Code: CodeSpecInvalid, Message: "the command has no argument vector"}
	}
	if strings.TrimSpace(spec.Argv[0]) == "" {
		return &Error{Code: CodeSpecInvalid, Message: "the command's executable name is empty"}
	}
	return nil
}

func effectiveOutputLimit(limit int) int {
	if limit <= 0 {
		return DefaultOutputLimit
	}
	return max(limit, MinOutputLimit)
}
