// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// ExitCodeUnavailable is [Result.ExitCode] when there is no exit status to
// report: the process never started, or it was killed by this package and its
// status therefore says nothing the caller did not already know.
//
// It is negative on purpose. No process exits with a negative status on either
// supported platform, so a caller that forgets to check [Result.TimedOut] or
// [Result.Err] cannot mistake it for a real code — least of all for zero.
const ExitCodeUnavailable = -1

// Spec describes one process to run.
//
// The zero value is not runnable: Argv is required. Everything else has a
// documented default, and none of the defaults reach out to global state
// beyond what os/exec would do anyway.
type Spec struct {
	// Argv is the argument vector, executable first. It is never evaluated by
	// a shell, never split on whitespace, and never expanded: each element
	// becomes exactly one argument to the child. A bare Argv[0] is resolved
	// through PATH by os/exec; anything with a separator is used as given.
	Argv []string

	// Dir is the child's working directory. Empty means the current process's
	// directory. The execution phase always sets it, because a Go test binary
	// resolves testdata relative to where it runs.
	Dir string

	// Env is the child's complete environment, in "KEY=VALUE" form. It is not
	// merged with anything: what is here is what the child gets.
	//
	// A nil Env inherits this process's environment, which is os/exec's rule
	// and is convenient for one-shot probes like `go version`. It is the wrong
	// thing for executing mutants: activation, per-worker TMPDIR, and cache
	// settings all live in the environment, so the engine composes the full
	// set explicitly and never relies on inheritance.
	Env []string

	// Timeout bounds the child's wall-clock run time. Zero means no timeout.
	// When it expires the whole process tree is killed and [Result.TimedOut]
	// is set; nothing is retried here, because whether a timeout is a
	// detection or scheduling noise is a policy question that belongs to the
	// engine.
	Timeout time.Duration

	// OutputLimit caps the retained combined output in bytes. Zero or negative
	// selects [DefaultOutputLimit]; anything positive below [MinOutputLimit]
	// is raised to it so the truncation notice still fits inside the budget.
	OutputLimit int
}

// Result is what one [Run] produced.
type Result struct {
	// ExitCode is the child's exit status, or [ExitCodeUnavailable] when there
	// is none. On POSIX a death by signal is reported as 128+N.
	ExitCode int

	// TimedOut reports that [Spec.Timeout] expired and the tree was killed. It
	// is the only field that distinguishes a timeout from a cancellation: a
	// context that was cancelled produces TimedOut false, ExitCode
	// [ExitCodeUnavailable], and a nil Err, and the caller tells them apart by
	// asking its own context.
	TimedOut bool

	// Duration is the wall-clock time [Run] took, from entry until the child
	// had been reaped — supervision set-up and any time spent killing the tree
	// included. It is deliberately the outer measurement rather than the
	// child's own: the engine derives mutant timeouts from baseline durations,
	// and a budget that excluded this package's own overhead would be a budget
	// the same work could exceed.
	Duration time.Duration

	// Output is combined stdout and stderr in the order the child wrote them,
	// capped at the effective [Spec.OutputLimit] by keeping the tail. When
	// bytes were dropped the first line is a notice beginning with
	// [OutputTruncatedPrefix], and it is paid for out of the budget:
	// len(Output) never exceeds the limit.
	Output []byte

	// Err is set only when the process could not be started or could not be
	// supervised — never when it ran and failed. A non-zero ExitCode is data
	// about the test; Err means go-mutants itself could not do its job, and
	// every Err carries a stable GOM72xx code (see [CodeOf]).
	Err error
}

// OK reports whether the process ran to completion with a zero exit status.
func (r Result) OK() bool {
	return r.Err == nil && !r.TimedOut && r.ExitCode == 0
}

// Run starts the process described by spec, supervises its whole process tree,
// and returns when it has finished, timed out, or been cancelled.
//
// The tree is killed on both the timeout and the cancellation path. It is
// never left running: on every exit from this function the supervisor has been
// released, and on Windows releasing it kills whatever is still inside the job.
//
// Run is safe for concurrent use and shares no mutable state between calls.
func Run(ctx context.Context, spec Spec) Result {
	started := time.Now()

	if err := validate(spec); err != nil {
		return Result{ExitCode: ExitCodeUnavailable, Duration: time.Since(started), Err: err}
	}
	// A context that is already done is a cancellation, not a start failure:
	// the engine draining a Ctrl-C should see its queued work come back as
	// cancelled rather than as thousands of errored mutants.
	if ctx.Err() != nil {
		return Result{ExitCode: ExitCodeUnavailable, Duration: time.Since(started)}
	}

	out := newTailWriter(effectiveOutputLimit(spec.OutputLimit))

	// Supervision is established before anything is running, so a machine that
	// cannot supervise never gets as far as spawning a child.
	sup, err := newSupervisor()
	if err != nil {
		return Result{ExitCode: ExitCodeUnavailable, Duration: time.Since(started), Err: err}
	}
	defer sup.release()

	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	// No stdin. A test binary that reads from the terminal would hang here
	// forever, and on POSIX it is in its own process group and would take a
	// SIGTTIN for trying.
	cmd.Stdin = nil
	// One writer for both streams: os/exec sees that they are the same value
	// and gives the child a single pipe, so the interleaving is the child's.
	cmd.Stdout = out
	cmd.Stderr = out
	// Bound the wait for output to reach EOF after the child exits, so an
	// orphaned descendant still holding the pipe cannot stall the run.
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

	// Fail closed. An unsupervised child is one this package cannot promise to
	// kill, and on Windows an unadopted child is also one that is still
	// suspended, so it is killed here rather than left to hang or to finish
	// out of reach.
	if err := sup.adopt(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return Result{
			ExitCode: ExitCodeUnavailable,
			Duration: time.Since(started),
			Output:   out.capture(),
			Err:      err,
		}
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

	var timedOut, killed bool
	select {
	case <-exited:
	case <-timeoutC:
		timedOut, killed = true, true
		sup.terminate(exited)
		<-exited
	case <-ctx.Done():
		killed = true
		sup.terminate(exited)
		<-exited
	}
	// close(exited) happens before every read above, so waitErr and
	// cmd.ProcessState are safe to read from here on.

	result := Result{
		ExitCode: ExitCodeUnavailable,
		TimedOut: timedOut,
		Duration: time.Since(started),
		Output:   out.capture(),
	}
	if !killed {
		result.ExitCode = exitCodeOf(cmd.ProcessState)
		result.Err = waitFailure(waitErr)
	}
	return result
}

// waitFailure decides which Wait errors are real.
//
// Two are not, and both are ordinary here. An *exec.ExitError is the child
// saying it failed, which is the data this whole tool is built to collect, and
// exec.ErrWaitDelay is this package's own [IODrainGrace] doing its job after a
// descendant kept the pipe open — in both cases ProcessState is populated and
// the result stands. Anything else means the operating system would not tell
// us how the process ended, which leaves the exit code untrustworthy, so it is
// reported rather than dropped.
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

// validate rejects a spec that cannot describe a process. A blank Argv[0] is
// rejected too: os/exec would report it as a confusing file-not-found on a
// path made of spaces, and a caller that built an argv out of an empty
// configuration value deserves to be told which mistake it made.
func validate(spec Spec) error {
	if len(spec.Argv) == 0 {
		return &Error{Code: CodeSpecInvalid, Message: "the command has no argument vector"}
	}
	if strings.TrimSpace(spec.Argv[0]) == "" {
		return &Error{Code: CodeSpecInvalid, Message: "the command's executable name is empty"}
	}
	return nil
}

// effectiveOutputLimit resolves [Spec.OutputLimit] against its documented
// defaults.
func effectiveOutputLimit(limit int) int {
	if limit <= 0 {
		return DefaultOutputLimit
	}
	return max(limit, MinOutputLimit)
}
