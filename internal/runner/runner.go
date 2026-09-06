// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/trace"
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

	// Trace is where this execution is recorded. It is the run's own recorder,
	// handed down through the options of every layer rather than reached for
	// through a global, so two runs in one process record into two recordings.
	//
	// A nil recorder is the disabled trace and every method on it is safe to
	// call, which is why [Run] records unconditionally: a traced run and an
	// untraced one take the same path, and there is no branch for a verdict to
	// come to depend on.
	Trace *trace.Recorder

	// Kind is what this command is, from [trace.ExecKinds]. It is the one thing
	// about an execution this package cannot know — `go test -c` and a mutant's
	// test binary are the same syscall from here — so it is the one thing a
	// call site has to supply.
	//
	// An empty Kind is accepted: the recorder stores what it is given, and the
	// schema is where an unlabelled command is refused. That division is
	// deliberate. A runner that rejected an unlabelled spec would turn a
	// missing diagnostic into a failed run, which is the one thing a diagnostic
	// must never do.
	Kind string

	// Subject is what the command is about: a mutant id, an import path, a
	// scope pattern. It is empty when the kind says everything there is to say.
	Subject string
}

// An Invocation is the command a failure was about: what was run, where, under
// which label, and where the recording kept it.
//
// It exists because [Result] deliberately carries no argument vector, and an
// error that travelled up three layers therefore arrived saying that something
// could not be started without saying what. The command is attached where it is
// still known — here — rather than reconstructed by a renderer that would have
// to guess at the working directory and the environment.
type Invocation struct {
	// Argv is a copy of the argument vector. It is copied rather than shared
	// because a caller may reuse its buffer for the next command, and an error
	// already handed over is not a place for a later write to arrive.
	Argv []string

	// Dir is the directory the command was to run in. Empty means this
	// process's own, which is what a reader reproducing the failure needs to
	// know rather than guess.
	Dir string

	// Kind is [Spec.Kind], so a rendered failure can say what the command was
	// for as well as what it was.
	Kind string

	// TraceSeq is the `exec` event this execution was recorded at, or zero when
	// nothing was recorded. It is what turns a one-line rendered failure into
	// the whole command's preserved output in the recording.
	TraceSeq int64
}

// InvocationOf names the command a spec described, recorded at the sequence its
// result came back with.
func InvocationOf(spec Spec, result Result) Invocation {
	return Invocation{
		Argv:     slices.Clone(spec.Argv),
		Dir:      spec.Dir,
		Kind:     spec.Kind,
		TraceSeq: result.TraceSeq,
	}
}

// CommandOf is what a caller turning a [Result] into its own error should
// attach: the command the result was about, as a pointer it can hand straight
// to an error's Invocation field.
//
// It prefers the invocation this package already attached to its own failure
// over building a second one from the same spec. The two describe the same
// command, so the choice costs nothing either way — but only one of them is the
// value the recording also points at, and an error naming a command the trace
// does not know is exactly the kind of small disagreement nobody can debug
// afterwards. It also means a caller that wrapped somebody else's failure
// cannot relabel it with its own spec.
//
// Every layer above needs this, so it lives here rather than three times over
// in internal/engine, internal/execute and internal/validate.
func CommandOf(spec Spec, result Result) *Invocation {
	var failure *Error
	if errors.As(result.Err, &failure) && failure.Invocation != nil {
		return failure.Invocation
	}
	invocation := InvocationOf(spec, result)
	return &invocation
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
	// every Err carries a stable GOM72xx code (see [CodeOf]) and the
	// [Invocation] it was about (see [Error.Command]).
	Err error

	// TraceSeq is the sequence number of the `exec` event this execution was
	// recorded at, or zero when [Spec.Trace] was nil and nothing was recorded.
	//
	// It is how the rest of a recording points at a command: a mutant attempt
	// names the executions it ran, a validation step names the compile it
	// spent, and both are joins onto a line that carries the argv, the timing
	// and the digest of the output.
	TraceSeq int64
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
//
// Every call records exactly one `exec` event into [Spec.Trace], and every
// error it returns carries the [Invocation] it was about. Both happen here, at
// the one place in go-mutants that starts a process, because a rule enforced at
// the choke point is a rule no future call site can forget: a caller can forget
// to *label* a command, and the schema's `kind` enum catches that, but it
// cannot forget to record one.
func Run(ctx context.Context, spec Spec) Result {
	return record(spec, runProcess(ctx, spec))
}

// record hands the finished execution to the recorder and stamps what came back
// onto the result and onto the failure, if there was one.
//
// It runs after the child has been reaped and its output captured, so the
// execution and its outcome are one line rather than two that a reader has to
// pair up. It is a wrapper around [runProcess] rather than a defer inside it
// for one reason: a single exit through the recorder cannot be skipped by an
// early return somebody adds later.
func record(spec Spec, result Result) Result {
	result.TraceSeq = spec.Trace.Exec(trace.ExecRecord{
		Kind:      spec.Kind,
		Subject:   spec.Subject,
		Argv:      spec.Argv,
		Dir:       spec.Dir,
		EnvNames:  environmentOf(spec),
		TimeoutMS: milliseconds(spec.Timeout),
		ExitCode:  result.ExitCode,
		TimedOut:  result.TimedOut,
		// Duration is [Run]'s own outer measurement, supervision and any time
		// spent killing the tree included, which is the number a reader
		// comparing two runs wants.
		DurationMS: milliseconds(result.Duration),
		// The retained capture, which is what the recorder sizes and digests
		// and what a directory sink preserves: the tail this package kept,
		// truncation notice included, and not the total the child produced.
		// That total is a separate fact and gets a field of its own when
		// something needs it.
		Output: result.Output,
		Error:  errorText(result.Err),
	})
	// Nothing to attach on the path a run usually takes, and the errors.As
	// below would otherwise force the pointer onto the heap for every process
	// go-mutants starts.
	if result.Err == nil {
		return result
	}
	// Every error this package produces is freshly allocated by the call that
	// failed, so filling the invocation and the output in here cannot be seen by
	// anybody else. Each is filled in only when it is empty, so that an inner
	// failure which already named its own command, or kept its own words, keeps
	// them.
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

// environmentOf is the environment the child could see, handed to the recorder
// as whole entries because reducing them to names is the recorder's job and
// belongs in one place rather than at every call site.
//
// A nil [Spec.Env] is os/exec's "inherit", so what the child got is this
// process's own environment and that is what is recorded. Leaving the field
// empty instead would say the command ran with no environment at all, which is
// a different and false statement. The copy is paid for only by the one-shot
// probes that inherit — every command the engine issues composes its
// environment explicitly — and it is paid whether or not anybody is recording,
// which is the price of having no branch here for a verdict to depend on.
func environmentOf(spec Spec) []string {
	if spec.Env != nil {
		return spec.Env
	}
	return os.Environ()
}

// milliseconds renders a duration the way every duration in the trace contract
// is recorded: whole milliseconds, never negative. Zero therefore means both
// "under a millisecond" and, for a timeout, "none", which is what [Spec.Timeout]
// already means by zero.
func milliseconds(d time.Duration) int64 {
	if ms := d.Milliseconds(); ms > 0 {
		return ms
	}
	return 0
}

// errorText renders a failure for the recording, and nothing for the absence of
// one.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// runProcess is [Run] without the recording: it starts the process, supervises
// it, and returns what happened.
func runProcess(ctx context.Context, spec Spec) Result {
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
