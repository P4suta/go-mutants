// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// DefaultTimeout bounds every child a test starts through this package.
//
// Sixty seconds is far longer than any single step a test drives — a fixture's
// whole suite, a `go test -c`, a `git commit` — and far shorter than the
// per-package alarm `go test` fires, which is the point: a step that hangs
// should fail as a named step with its output quoted, not as a ten-minute panic
// with every goroutine in the binary dumped after it.
const DefaultTimeout = 60 * time.Second

// ExitCodeUnavailable is the status of a child that never ran or was killed.
//
// The status a terminated process leaves behind disagrees between platforms — a
// Windows job termination code against a POSIX 128+SIGKILL — and none of it says
// anything the caller did not already know from having asked for the kill. It
// mirrors internal/runner's convention so that a test reading either one reads
// the same value.
const ExitCodeUnavailable = -1

// Result is what a child did.
//
// The three failures a caller has to tell apart are kept apart:
//
//   - A child that ran and failed is not an error. [Result.Err] is reserved for a
//     failure to start or supervise the process; a non-zero [Result.ExitCode] is
//     a fact about the child, and half the assertions in this repository are
//     about a non-zero one.
//   - A child that ran out of time reports [Result.TimedOut], and a child whose
//     caller walked away reports [Result.Abandoned]. Both have
//     [ExitCodeUnavailable] and a nil Err, because neither is a failure the child
//     reported — but they have different causes and different remedies, so they
//     are different fields rather than one.
//   - Output is stdout and stderr merged, in the order the two streams arrived.
//     Stdout is captured a second time on its own, because a caller that parses a
//     child's answer — `git rev-parse HEAD`, `go env GOMODCACHE` — must not have
//     the child's hints and progress lines mixed into the value. Telling the
//     streams apart costs a pipe each, so the merge is as exact as the two pipes'
//     scheduling rather than byte-exact; internal/runner, whose subject is what a
//     mutant printed, keeps its single pipe for that reason.
type Result struct {
	Argv      []string
	Dir       string
	ExitCode  int
	TimedOut  bool
	Abandoned bool
	Output    []byte
	Stdout    []byte
	Err       error
	Duration  time.Duration
}

// Exec runs one child in dir with env, and returns what it did.
//
// The argv is an argument vector, not a command line: it is handed to os/exec
// unchanged and is never expanded, split, quoted or interpreted by a shell. That
// is the same promise internal/runner makes about the commands a run executes,
// and it is worth making here too — a fixture directory with a space in its
// name is a fixture this project wants to be able to add, and a harness that
// expanded its own arguments would turn that into two arguments and a mystery.
//
// A nil env means the process's own environment, which is what a test that has
// already called [Env] wants. Everything else composes one with
// [Environment.Vars], [Environment.With] or [Compose].
//
// The deadline is [DefaultTimeout], and it is deliberately *not* derived from
// the test's cancellation. t.Context() is cancelled before a test's cleanups
// run, and cleanups are where the harness's most important children live — a
// snapshot removed, a repository torn down, a temporary tree swept — so a child
// started from one would be killed before it had run an instruction. A caller
// that wants a shorter deadline, or one that wants the child to stop when the
// caller does, uses [ExecContext].
func Exec(t testing.TB, dir string, env []string, argv ...string) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), DefaultTimeout)
	defer cancel()
	return ExecContext(ctx, t, dir, env, argv...)
}

// ExecContext is [Exec] with the caller's own deadline and cancellation.
//
// It is what a step with a budget of its own uses — a baseline measurement, a
// mutant's test binary — and what a test of the timeout path uses, since waiting
// out [DefaultTimeout] to observe it would cost a minute. A context that is
// cancelled rather than expired produces [Result.Abandoned]: the caller walked
// away, which is not the child running out of time and does not mean the same
// thing to whoever reads the failure.
func ExecContext(ctx context.Context, t testing.TB, dir string, env []string, argv ...string) Result {
	t.Helper()
	if len(argv) == 0 {
		t.Fatalf("Exec was given no argv to run in %s", dir)
		return Result{Dir: dir, ExitCode: ExitCodeUnavailable}
	}

	// os/exec runs the two streams on goroutines of their own as soon as they
	// are different writers, so the merged one is locked: a bytes.Buffer written
	// from both would lose output and race.
	var merged mergedOutput
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = io.MultiWriter(&merged, &stdout)
	cmd.Stderr = &merged
	// A descendant that outlives the child and still holds the pipe would
	// otherwise make the read block past the timeout that was supposed to bound
	// it. The captured output is returned as it stands.
	cmd.WaitDelay = 5 * time.Second

	started := time.Now()
	err := cmd.Run()
	result := Result{
		Argv:     argv,
		Dir:      dir,
		Output:   merged.Bytes(),
		Stdout:   stdout.Bytes(),
		Duration: time.Since(started),
		ExitCode: ExitCodeUnavailable,
	}

	var exited *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.TimedOut = true
	case errors.Is(ctx.Err(), context.Canceled):
		result.Abandoned = true
	case errors.As(err, &exited):
		result.ExitCode = exited.ExitCode()
	case err != nil:
		result.Err = err
	default:
		result.ExitCode = 0
	}
	return result
}

// RequireExit ends the step unless the child ran to completion with the status
// the step expects, and quotes the child's output whenever it did not.
//
// Quoting is the whole point. A `go test -c` that failed, a suite that went red
// under a mutant, a `git` that refused a commit — every one of them explains
// itself in its output, and an assertion that reported only the status turns a
// two-second diagnosis into a re-run with the command copied out by hand. In CI
// there is no re-run: the log is all there is.
func RequireExit(t testing.TB, r Result, want int, what string) {
	t.Helper()
	switch {
	case r.Err != nil:
		t.Fatalf("%s could not be run: %v\n%s\n%s", what, r.Err, r.command(), r.Output)
	case r.TimedOut:
		t.Fatalf("%s did not finish within its deadline (%v elapsed):\n%s\n%s", what, r.Duration.Truncate(time.Millisecond), r.command(), r.Output)
	case r.Abandoned:
		t.Fatalf("%s was abandoned before it finished, because its caller's context was cancelled "+
			"(%v elapsed):\n%s\n%s", what, r.Duration.Truncate(time.Millisecond), r.command(), r.Output)
	case r.ExitCode != want:
		t.Fatalf("%s exited %d, want %d:\n%s\n%s", what, r.ExitCode, want, r.command(), r.Output)
	}
}

// RequireOutput fails the step for each needle the child did not print, quoting
// the whole output once per miss so a failure is readable without re-running.
//
// It reports rather than ends the step, because a step that expected four lines
// and printed two should say which two are missing in one run.
func RequireOutput(t testing.TB, r Result, what string, needles ...string) {
	t.Helper()
	out := string(r.Output)
	for _, needle := range needles {
		if !strings.Contains(out, needle) {
			t.Errorf("%s did not print %q:\n%s", what, needle, out)
		}
	}
}

// RequireNoOutput fails the step for each needle the child did print.
//
// An absence is a claim like any other — "the run reported no warning", "the
// listing named no skipped file" — and a claim needs the text that broke it.
func RequireNoOutput(t testing.TB, r Result, what string, needles ...string) {
	t.Helper()
	out := string(r.Output)
	for _, needle := range needles {
		if strings.Contains(out, needle) {
			t.Errorf("%s printed %q, which it should not have:\n%s", what, needle, out)
		}
	}
}

// mergedOutput collects both of a child's streams into one buffer.
type mergedOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (m *mergedOutput) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.Write(p)
}

func (m *mergedOutput) Bytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return bytes.Clone(m.buf.Bytes())
}

// command renders the child as a line somebody can paste, so a failure names the
// command as well as its output.
//
// Every element is quoted, because an argv is a vector and the line has to say
// so: a fixture directory called `two words`, or a `-run` pattern with a `|` in
// it, is one argument, and a line that ran them together would name a different
// command from the one that failed.
func (r Result) command() string {
	quoted := make([]string, 0, len(r.Argv))
	for _, arg := range r.Argv {
		quoted = append(quoted, strconv.Quote(arg))
	}
	return "$ (in " + strconv.Quote(r.Dir) + ") " + strings.Join(quoted, " ")
}
