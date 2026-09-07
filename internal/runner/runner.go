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

	// MemoryLimit bounds the resident memory of the child's whole process tree,
	// in bytes. Zero — and anything negative — means no bound, exactly as a zero
	// Timeout means no deadline.
	//
	// It is the timeout's twin and is enforced the same way. While the child
	// runs, the tree's resident size is sampled every [MemorySampleInterval],
	// and the first sample above the limit kills the whole tree and sets
	// [Result.MemoryExceeded]; nothing is retried here, because whether a
	// runaway allocation is a detection or a suite that is simply large is a
	// policy question that belongs to the engine — which is also where the
	// number comes from, derived from what the unmutated tests were measured to
	// need.
	//
	// A bound is not enforceable on every platform go-mutants runs on; see
	// [MemoryBoundSupported]. Where it is not, this field is accepted and has no
	// effect, and [Result.PeakMemory] is still reported.
	MemoryLimit int64

	// OutputLimit caps the retained combined output in bytes. Zero or negative
	// selects [DefaultOutputLimit]; anything positive below [MinOutputLimit]
	// is raised to it so the truncation notice still fits inside the budget.
	OutputLimit int

	// SeparateStdout asks for [Result.Stdout] beside [Result.Output]: the
	// child's standard output on its own, under the same [Spec.OutputLimit]
	// accounting.
	//
	// It exists for the one kind of caller that has to *parse* what a command
	// printed. `go list -json` writes its document to stdout and writes
	// warnings, module downloads and toolchain switches to stderr, and every
	// one of those is a command that exits zero and did what it was asked — so
	// a decoder handed the combined capture fails on the first byte of
	// `go: warning: …`. The combined stream is still what a person reading a
	// failure needs and is still what the recording digests, so both are kept
	// rather than one replaced by the other.
	//
	// It is opt-in because it changes the combined capture: two writers mean
	// two pipes, so the interleaving in [Result.Output] becomes the operating
	// system's rather than the child's. Every caller that reads output as
	// evidence wants the single pipe, which is what it gets by leaving this
	// false.
	SeparateStdout bool

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

	// MemoryExceeded reports that [Spec.MemoryLimit] was passed. It stands
	// beside TimedOut rather than inside it: the two are different facts about a
	// run, they are never both true, and a caller that conflated them would
	// report a mutant that ate the machine as one that merely took too long.
	//
	// It is set two ways, and the second is why ExitCode has to be read with it.
	// A tree the sampler caught was killed here, and reports ExitCode
	// [ExitCodeUnavailable] and a nil Err exactly as a timed-out one does. A
	// tree the *kernel* ended — on Windows the job object refuses its next
	// commit and the Go runtime dies with "out of memory" — is reported exceeded
	// too, with its own exit code beside the flag: both are true, and the flag
	// is what says the status is a consequence of the budget rather than of the
	// tests. See [exceededAtExit], and note in particular that a tree which went
	// over the bound and *finished* is not reported as exceeded: that is a fact
	// about what it cost, which [PeakMemory] carries, and not a cause.
	//
	// The retained output is left as the child wrote it either way — this
	// package adds no synthetic line saying what happened, because what a caller
	// renders is the caller's to word.
	MemoryExceeded bool

	// PeakMemory is the highest memory the child's process tree was observed to
	// hold, in bytes, and is zero when nothing observed it. A tree killed by
	// [Spec.MemoryLimit] still reports how far it got.
	//
	// Who observes it is the platform's business, and it decides which runs
	// carry a number. On Windows the job object accounts for the whole tree
	// from start to finish, and on macOS wait4's ru_maxrss is the child's own
	// high-water mark, so every run reports one. On Linux ru_maxrss is *not*
	// the child's own — a process started with clone(CLONE_VM|CLONE_VFORK),
	// which is every process os/exec starts, inherits the parent's high-water
	// mark, so a `/bin/true` started by a process that once held a gibibyte
	// reports a gibibyte — and the only honest number is a sampled one. So on
	// Linux every run is sampled, early and often at first and then every
	// [MemorySampleInterval], and reports the highest sample; a run that ends
	// before the first sample reports zero, having ended before anybody looked.
	//
	// It is deliberately not called RSS, because it is not the same quantity on
	// both platforms and no conversion between them would be anything but a
	// number this package invented. On Unix it is the **resident set**: pages
	// actually in memory, from the kernel's own high-water mark. On Windows it
	// is the job's **committed charge**: what the processes in it have claimed,
	// which for a Go program runs a little above its resident set and never
	// below.
	//
	// What "the tree" covers differs too, and is stated rather than smoothed
	// over. On Windows it is exact: the job object accounts for every process
	// in it. On POSIX it is the larger of two approximations — the kernel's
	// high-water mark for the child and every descendant it waited for, and,
	// when the run was bounded and therefore sampled, the largest sum the
	// sampler saw across the process group.
	PeakMemory int64

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

	// Stdout is the child's standard output alone, or nil when
	// [Spec.SeparateStdout] did not ask for it. It is capped the same way
	// Output is, out of the same [Spec.OutputLimit].
	//
	// It is for a caller that has to decode what the command printed; Output
	// stays the evidence, and it is Output the recording digests, because a
	// failure is read with the child's diagnostics beside its document rather
	// than with them thrown away. A capture that lost bytes is unusable as a
	// document either way — the notice and the tail are what survive — and
	// [Result.Truncated] is what says so, for both fields at once: the combined
	// total is at least the stdout total, so a stdout capture that was
	// truncated is one whose Truncated is already set.
	Stdout []byte

	// OutputBytes is everything the child wrote to both streams, kept or not.
	// It is what the process produced and not what survived the cap, so it is
	// the same number whether or not Truncated is set, and a caller reporting a
	// size never has to ask which case it is in.
	//
	// It is zero for a run that started no process, which is the truth about it:
	// nothing wrote anything.
	OutputBytes int64

	// Truncated reports that Output lost bytes to the limit, in which case it
	// begins with [OutputTruncatedPrefix].
	//
	// It is the field to branch on. The notice is a line written for a person to
	// read, and a consumer that matched its text was making a diagnostic into a
	// wire format nobody could reword; this says the same thing as data, and it
	// says it without a caller having to know how the notice is spelled.
	Truncated bool

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
	return r.Err == nil && !r.TimedOut && !r.MemoryExceeded && r.ExitCode == 0
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
		// What the command cost the machine, beside what it cost the clock. It
		// is recorded for every command rather than only for the bounded ones,
		// because the question a reader brings to a recording — which of these
		// thousands of processes was the expensive one — is asked after the run
		// and cannot be asked of a run that only measured what it bounded.
		PeakMemoryBytes: result.PeakMemory,
		// The retained capture, which is what the recorder sizes and digests
		// and what a directory sink preserves: the tail this package kept,
		// truncation notice included, and not the total the child produced.
		// That total is a separate fact and lives on [Result.OutputBytes].
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

	limit := effectiveOutputLimit(spec.OutputLimit)
	out := newTailWriter(limit)
	// The second capture exists only when a caller asked for it, and it is what
	// splits the child's two streams onto two pipes; see [Spec.SeparateStdout].
	var stdoutOnly *tailWriter
	if spec.SeparateStdout {
		stdoutOnly = newTailWriter(limit)
	}
	// captured is what the two writers hold, read once per exit path so that
	// every path reports the same observation.
	captured := func(result Result) Result {
		result.Output, result.OutputBytes, result.Truncated = out.capture()
		if stdoutOnly != nil {
			result.Stdout, _, _ = stdoutOnly.capture()
		}
		return result
	}

	// Supervision is established before anything is running, so a machine that
	// cannot supervise never gets as far as spawning a child. The memory bound
	// travels in with it because one platform enforces it in the kernel, on the
	// same object, and it has to be in force before the first process joins.
	sup, err := newSupervisor(spec.MemoryLimit)
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
	//
	// A caller that asked for the split gets two pipes instead, because that is
	// the only way to know which stream a byte came from — and gives up the
	// exact interleaving in exchange, which is why it is opt-in. The tail
	// writer's mutex is what makes the combined capture correct either way.
	cmd.Stdout = out
	cmd.Stderr = out
	if stdoutOnly != nil {
		cmd.Stdout = io.MultiWriter(out, stdoutOnly)
	}
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

	// Every run is sampled, bounded or not: with a limit the sampler is what
	// enforces it, and without one it only measures, which on Linux is the one
	// honest measurement there is (see [Result.PeakMemory]). What it costs — a
	// goroutine and a handful of small reads a second — is paid per process,
	// and a process that ends before the first sample has paid for nothing.
	watchdog := watchMemory(sup, max(spec.MemoryLimit, 0))

	var timedOut, memoryExceeded, killed bool
	select {
	case <-exited:
	case <-timeoutC:
		timedOut, killed = true, true
		sup.terminate(exited, TerminationGrace)
		<-exited
	case <-watchdog.exceededC():
		// No grace, and this is the one kill that gets none. The grace exists
		// so a test binary can flush the output that explains why it timed out;
		// a tree over its memory bound has already had the only evidence a
		// memory kill rests on — its peak — recorded, and would spend the grace
		// allocating more of what it is being killed for. See
		// [supervisor.terminate].
		memoryExceeded, killed = true, true
		sup.terminate(exited, 0)
		<-exited
	case <-ctx.Done():
		killed = true
		sup.terminate(exited, TerminationGrace)
		<-exited
	}
	// close(exited) happens before every read above, so waitErr and
	// cmd.ProcessState are safe to read from here on.

	// The sampler is stopped before its peak is read, and waited for, so that
	// the number reported is the last one it took rather than whichever one it
	// happened to have finished. There is exactly one call — every path out of
	// the select arrives here — so nothing has to make it idempotent.
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
	// And once more for a child the *kernel* ended while over the line: a tree
	// can cross the bound and die inside one sampling tick, and on Windows the
	// job object's own limit is what stops it there. See [exceededAtExit] for
	// the three conditions that keep this from reading an ordinary spike as a
	// kill, and for why such a result carries MemoryExceeded beside a real exit
	// code.
	result.MemoryExceeded = result.MemoryExceeded ||
		exceededAtExit(kernelBoundsMemory, spec.MemoryLimit, result.PeakMemory, result.ExitCode, killed)
	return result
}

// peakOf combines the two things that know how big the tree got — where both
// of them are telling the truth about the child.
//
// They are combined rather than chosen between because each sees something the
// other cannot. The platform's own accounting covers a child that grew and
// exited between two ticks of the sampler, which no sampler could have caught;
// the sampler covers a grandchild that outlived its parent, which POSIX's wait4
// accounting never attributes to anybody. The larger of the two is the honest
// answer to "how much of the machine did this take", and neither is a number
// go-mutants invented.
//
// Except on Linux, where the accounted number is max(the parent's high-water
// mark when it forked, the child's own): see [accountedPeakBelongsToTheChild].
// It is not consulted there at all. Recovering the half that is the child's —
// "an accounted number above the parent's mark can only be the child's" — was
// tried and withdrawn: the mark has to be read before the fork, a parent with
// other goroutines allocating (a test binary under the race detector, or the
// engine itself running discovery beside a mutant) moves it by hundreds of
// megabytes between the read and the fork, and the number that then clears
// the stale mark is the parent's after all. So on Linux the sampler is the
// only witness, and a run nobody sampled reports no peak rather than the
// parent's.
//
// It is called after the sampler has been stopped and waited for, and before
// the supervisor is released — which on Windows is where the accounting lives,
// and which happens in a defer at the end of [runProcess].
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
