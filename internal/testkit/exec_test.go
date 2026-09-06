// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// execHelperEnv both switches the helper child on and names the status it
// should exit with. It does not begin with GO_MUTANTS_, because the environment
// policy strips that whole prefix from every child it composes.
const execHelperEnv = "TESTKIT_EXEC_HELPER"

// TestExecHelperProcess is not a test. It is the child every test in this file
// starts: the test binary re-executed with one test selected, which is the only
// portable way to get a process that prints known bytes and exits with a chosen
// status without compiling a program first.
//
// It returns silently when it was not asked for, so a normal run neither runs it
// nor reports it as skipped.
func TestExecHelperProcess(t *testing.T) {
	status, wanted := os.LookupEnv(execHelperEnv)
	if !wanted {
		return
	}
	// The writes are unchecked deliberately: the parent reads what arrived, and
	// a helper that reported a write failure to nobody would only hide it.
	_, _ = fmt.Fprintf(os.Stdout, "argv=%q\n", os.Args[1:])
	if cwd, err := os.Getwd(); err == nil {
		_, _ = fmt.Fprintf(os.Stdout, "cwd=%s\n", cwd)
	}
	_, _ = fmt.Fprintln(os.Stderr, "the helper wrote this to stderr")
	if nap := os.Getenv(execHelperEnv + "_SLEEP"); nap != "" {
		delay, err := time.ParseDuration(nap)
		if err != nil {
			delay = time.Minute
		}
		time.Sleep(delay)
	}
	code, err := strconv.Atoi(status)
	if err != nil {
		code = 97
	}
	os.Exit(code)
}

// helperArgv is the command that re-executes this test binary as the helper.
func helperArgv(extra ...string) []string {
	return append([]string{os.Args[0], "-test.run=^TestExecHelperProcess$", "--"}, extra...)
}

// TestExecCapturesExitCodeAndOutput states what a caller gets back: the status
// as data rather than as a failure, and both streams in the order the child
// wrote them.
//
// A child that ran and failed is not an error — every caller of a mutation
// runner needs a non-zero status to be a fact about the test rather than
// something that ends the run — so [Result.Err] is reserved for a failure to
// start or supervise the process. That is the same separation internal/runner
// makes, and a helper that conflated them would make a test asserting on exit 1
// impossible to write.
func TestExecCapturesExitCodeAndOutput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	env := withEntries(Compose(t, t.TempDir()), execHelperEnv+"=3")
	result := Exec(t, dir, env, helperArgv("first", "second")...)

	if result.Err != nil {
		t.Fatalf("the helper could not be run: %v\n%s", result.Err, result.Output)
	}
	if result.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", result.ExitCode)
	}
	if result.TimedOut {
		t.Error("TimedOut is set on a child that exited on its own")
	}
	if got := string(result.Output); !strings.Contains(got, "the helper wrote this to stderr") {
		t.Errorf("the captured output has no stderr in it:\n%s", got)
	}
	if got := string(result.Output); !strings.Contains(got, `"first" "second"`) {
		t.Errorf("the captured output does not show the arguments:\n%s", got)
	}
	if !SamePath(result.Dir, dir) {
		t.Errorf("Result.Dir = %s, want %s", result.Dir, dir)
	}
	if result.Duration <= 0 {
		t.Errorf("Duration = %v, want the time the child took", result.Duration)
	}
	if !strings.Contains(string(result.Output), "cwd="+resolvePath(dir)) &&
		!strings.Contains(string(result.Output), "cwd="+dir) {
		t.Errorf("the child did not run in the directory it was given:\n%s", result.Output)
	}
}

// TestExecRunsFromACleanup is why the deadline is not derived from the test's
// own context.
//
// t.Context() is cancelled *before* a test's cleanups run — that is what it is
// for — so a child started from a cleanup with a context derived from it would
// be killed before it had run an instruction. Cleanups are exactly where the
// harness's most important children live: a snapshot removed, a repository torn
// down, a temporary tree swept. So [Exec] detaches from the cancellation and
// keeps only the deadline.
func TestExecRunsFromACleanup(t *testing.T) {
	t.Parallel()

	env := withEntries(Compose(t, t.TempDir()), execHelperEnv+"=0")
	dir := t.TempDir()
	t.Cleanup(func() {
		result := Exec(t, dir, env, helperArgv("from-a-cleanup")...)
		if result.Err != nil {
			t.Errorf("a child started from a cleanup could not be run: %v\n%s", result.Err, result.Output)
		}
		if result.ExitCode != 0 {
			t.Errorf("a child started from a cleanup exited %d, want 0:\n%s", result.ExitCode, result.Output)
		}
		if result.Abandoned {
			t.Errorf("a child started from a cleanup was reported as abandoned:\n%s", result.Output)
		}
	})
}

// TestExecContextReportsATimeoutRatherThanAFailure drives the branch every
// caller's failure message depends on and nothing had ever reached: at the
// default sixty seconds a test of it would have taken a minute, so the deadline
// is the caller's to choose.
func TestExecContextReportsATimeout(t *testing.T) {
	t.Parallel()

	env := withEntries(Compose(t, t.TempDir()), execHelperEnv+"=0", execHelperEnv+"_SLEEP=60s")
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	result := ExecContext(ctx, t, t.TempDir(), env, helperArgv()...)

	if !result.TimedOut {
		t.Errorf("TimedOut is false for a child that outlived its deadline (exit %d, err %v):\n%s",
			result.ExitCode, result.Err, result.Output)
	}
	if result.Abandoned {
		t.Error("a child that ran out of time was reported as abandoned")
	}
	if result.Err != nil {
		t.Errorf("Err = %v, want nil: a timeout is a decision this package made rather than a failure the child reported", result.Err)
	}
	if result.ExitCode != ExitCodeUnavailable {
		t.Errorf("ExitCode = %d, want %d for a child that was killed", result.ExitCode, ExitCodeUnavailable)
	}
	if result.Duration > 30*time.Second {
		t.Errorf("Duration = %v, so the deadline did not bound the child", result.Duration)
	}
}

// TestExecContextReportsAnAbandonedChild keeps the two ways a child can be cut
// short apart. A deadline is this package deciding the child had long enough; a
// cancellation is the caller walking away, and the remedy — and the message — is
// different.
func TestExecContextReportsAnAbandonedChild(t *testing.T) {
	t.Parallel()

	env := withEntries(Compose(t, t.TempDir()), execHelperEnv+"=0", execHelperEnv+"_SLEEP=60s")
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	result := ExecContext(ctx, t, t.TempDir(), env, helperArgv()...)

	if !result.Abandoned {
		t.Errorf("Abandoned is false for a child whose caller cancelled (timed out %v, exit %d):\n%s",
			result.TimedOut, result.ExitCode, result.Output)
	}
	if result.TimedOut {
		t.Error("an abandoned child was reported as having run out of time")
	}
}

// TestRequireExitReportsAnAbandonedChild states the third message, so a
// cancelled run does not read as a test that failed.
func TestRequireExitReportsAnAbandonedChild(t *testing.T) {
	t.Parallel()

	rec := &recorder{TB: t}
	RequireExit(rec, Result{Argv: []string{"go", "test"}, Abandoned: true, ExitCode: ExitCodeUnavailable}, 0, "the suite")
	if len(rec.fatals) != 1 {
		t.Fatalf("RequireExit produced %d fatal report(s), want 1: %q", len(rec.fatals), rec.fatals)
	}
	if !strings.Contains(rec.fatals[0], "abandoned") {
		t.Errorf("the report does not say the caller walked away:\n%s", rec.fatals[0])
	}
}

// TestExecSeparatesStdoutFromTheCombinedOutput is what lets a caller parse a
// child's answer.
//
// `git rev-parse HEAD` writes a hash to stdout and every hint, advice and
// progress line to stderr, and a caller that got the two concatenated would
// parse the hints as part of the hash. The combined stream is still what a
// failure quotes, because the explanation is usually on stderr.
func TestExecSeparatesStdoutFromTheCombinedOutput(t *testing.T) {
	t.Parallel()

	env := withEntries(Compose(t, t.TempDir()), execHelperEnv+"=0")
	result := Exec(t, t.TempDir(), env, helperArgv()...)

	RequireExit(t, result, 0, "the helper")
	if got := string(result.Stdout); strings.Contains(got, "the helper wrote this to stderr") {
		t.Errorf("Result.Stdout carries what the child wrote to stderr:\n%s", got)
	}
	if !strings.Contains(string(result.Stdout), "argv=") {
		t.Errorf("Result.Stdout does not carry what the child wrote to stdout:\n%s", result.Stdout)
	}
	if !strings.Contains(string(result.Output), "the helper wrote this to stderr") {
		t.Errorf("Result.Output lost the child's stderr:\n%s", result.Output)
	}
}

// TestResultCommandQuotesEveryArgument keeps the reproduction line in a failure
// message paste-able: a fixture directory with a space in its name, or an
// argument with a `$` in it, is one argument, and a line that ran them together
// would be a different command from the one that failed.
func TestResultCommandQuotesEveryArgument(t *testing.T) {
	t.Parallel()

	got := Result{
		Dir:  filepath.Join("tmp", "two words"),
		Argv: []string{"go", "test", "-run", "Test A|Test B"},
	}.command()
	for _, want := range []string{`"go"`, `"test"`, `"-run"`, `"Test A|Test B"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the command line does not quote %s:\n%s", want, got)
		}
	}
}

// TestExecNeverUsesAShell pins the promise internal/runner makes about the same
// thing one layer down: an argument vector is a vector, and a path with a space,
// a `$` or a `;` in it is a path with a space, a `$` and a `;` in it.
//
// A test corpus with a directory called `two words` is a fixture this project
// wants to be able to add, and a harness that expanded its arguments would turn
// that fixture into two arguments and a mystery.
func TestExecNeverUsesAShell(t *testing.T) {
	t.Parallel()

	const nasty = "$HOME; echo pwned > /tmp/pwned && *"
	env := withEntries(Compose(t, t.TempDir()), execHelperEnv+"=0")
	result := Exec(t, t.TempDir(), env, helperArgv(nasty)...)

	RequireExit(t, result, 0, "the helper")
	if got := string(result.Output); !strings.Contains(got, strconv.Quote(nasty)) {
		t.Errorf("the argument did not reach the child unchanged:\n%s", got)
	}
}

// TestExecReportsACommandThatCouldNotStart keeps a typo in an argv from looking
// like a test that failed.
func TestExecReportsACommandThatCouldNotStart(t *testing.T) {
	t.Parallel()

	result := Exec(t, t.TempDir(), nil, filepath.Join(t.TempDir(), "no-such-program"))
	if result.Err == nil {
		t.Fatalf("Exec of a program that does not exist returned no error (exit %d)", result.ExitCode)
	}
	if result.ExitCode != ExitCodeUnavailable {
		t.Errorf("ExitCode = %d, want %d for a child that never ran", result.ExitCode, ExitCodeUnavailable)
	}
}

// TestRequireExitQuotesTheChildOutputOnMismatch is the assertion helper's whole
// reason for existing.
//
// A `go test -c` that failed, a suite that went red under a mutant, a `git` that
// refused a commit — every one of them explains itself on stdout, and a helper
// that reported only the status turns a two-second diagnosis into a re-run with
// the command copied out by hand. In CI there is no re-run: the log is all there
// is.
func TestRequireExitQuotesTheChildOutputOnMismatch(t *testing.T) {
	t.Parallel()

	rec := &recorder{TB: t}
	RequireExit(rec, Result{
		Argv:     []string{"go", "test", "./..."},
		ExitCode: 1,
		Output:   []byte("--- FAIL: TestSomething\n\tsomething_test.go:12: 2 != 3\n"),
	}, 0, "the fixture's suite")

	if len(rec.fatals) != 1 {
		t.Fatalf("RequireExit produced %d fatal report(s), want 1: %q", len(rec.fatals), rec.fatals)
	}
	for _, want := range []string{"the fixture's suite", "exited 1", "want 0", "2 != 3"} {
		if !strings.Contains(rec.fatals[0], want) {
			t.Errorf("the report does not mention %q:\n%s", want, rec.fatals[0])
		}
	}
}

// TestRequireExitReportsATimeoutRatherThanAnExitStatus keeps the two apart in
// the message, because they have different causes and different remedies: a
// status is the child's opinion, and a timeout means nobody has one.
func TestRequireExitReportsATimeoutRatherThanAnExitStatus(t *testing.T) {
	t.Parallel()

	rec := &recorder{TB: t}
	RequireExit(rec, Result{
		Argv:     []string{"go", "test", "./..."},
		TimedOut: true,
		ExitCode: ExitCodeUnavailable,
		Output:   []byte("=== RUN   TestHangs\n"),
		Duration: DefaultTimeout,
	}, 0, "the fixture's suite")

	if len(rec.fatals) != 1 {
		t.Fatalf("RequireExit produced %d fatal report(s), want 1: %q", len(rec.fatals), rec.fatals)
	}
	for _, want := range []string{"did not finish", DefaultTimeout.String(), "TestHangs"} {
		if !strings.Contains(rec.fatals[0], want) {
			t.Errorf("the report does not mention %q:\n%s", want, rec.fatals[0])
		}
	}
}

// TestRequireExitReportsAChildThatCouldNotBeStarted separates the third case:
// the harness itself failed, and no assertion about the child means anything.
func TestRequireExitReportsAChildThatCouldNotBeStarted(t *testing.T) {
	t.Parallel()

	rec := &recorder{TB: t}
	RequireExit(rec, Result{
		Argv:     []string{"git", "commit"},
		ExitCode: ExitCodeUnavailable,
		Err:      errors.New("exec: \"git\": executable file not found in $PATH"),
	}, 0, "the commit")

	if len(rec.fatals) != 1 {
		t.Fatalf("RequireExit produced %d fatal report(s), want 1: %q", len(rec.fatals), rec.fatals)
	}
	if !strings.Contains(rec.fatals[0], "could not be run") {
		t.Errorf("the report reads as a failing child rather than a failing harness:\n%s", rec.fatals[0])
	}
}

// TestRequireExitSaysNothingWhenTheStatusMatches keeps the helpers quiet on the
// happy path: a passing test that logged its child's output would bury the one
// that did not.
func TestRequireExitSaysNothingWhenTheStatusMatches(t *testing.T) {
	t.Parallel()

	rec := &recorder{TB: t}
	RequireExit(rec, Result{ExitCode: 2, Output: []byte("noise")}, 2, "the child")
	RequireOutput(rec, Result{Output: []byte("a b c")}, "the child", "a", "c")
	RequireNoOutput(rec, Result{Output: []byte("a b c")}, "the child", "d")

	if len(rec.fatals)+len(rec.errors) != 0 {
		t.Errorf("the helpers reported on a match: fatals %q, errors %q", rec.fatals, rec.errors)
	}
}

// TestRequireOutputNamesEveryMissingNeedle reports with Errorf rather than
// Fatalf on purpose: a step that expected four lines and got two should say
// which two are missing in one run.
func TestRequireOutputNamesEveryMissingNeedle(t *testing.T) {
	t.Parallel()

	rec := &recorder{TB: t}
	RequireOutput(rec, Result{Output: []byte("only this line\n")}, "the listing", "first", "second")

	if len(rec.errors) != 2 {
		t.Fatalf("RequireOutput produced %d report(s), want one per missing needle: %q", len(rec.errors), rec.errors)
	}
	for i, want := range []string{"first", "second"} {
		if !strings.Contains(rec.errors[i], strconv.Quote(want)) {
			t.Errorf("report %d does not name %q:\n%s", i, want, rec.errors[i])
		}
		if !strings.Contains(rec.errors[i], "only this line") {
			t.Errorf("report %d does not quote the output:\n%s", i, rec.errors[i])
		}
	}
}

// TestRequireNoOutputNamesTheNeedleThatAppeared is the other direction, and it
// is the one an absence-based assertion needs: "the run printed no warning" is a
// claim, and a claim needs the text that broke it.
func TestRequireNoOutputNamesTheNeedleThatAppeared(t *testing.T) {
	t.Parallel()

	rec := &recorder{TB: t}
	RequireNoOutput(rec, Result{Output: []byte("GOM7901 coverage failed open\n")}, "the run", "GOM7901", "GOM7902")

	if len(rec.errors) != 1 {
		t.Fatalf("RequireNoOutput produced %d report(s), want 1: %q", len(rec.errors), rec.errors)
	}
	if !strings.Contains(rec.errors[0], "GOM7901") {
		t.Errorf("the report does not name the needle that appeared:\n%s", rec.errors[0])
	}
}

// TestExecRefusesAnEmptyArgv keeps a caller that built its argv from a slice
// that turned out to be empty from waiting sixty seconds for nothing.
func TestExecRefusesAnEmptyArgv(t *testing.T) {
	t.Parallel()

	rec := &recorder{TB: t}
	Exec(rec, t.TempDir(), nil)
	if len(rec.fatals) != 1 {
		t.Errorf("Exec with no argv produced %d fatal report(s), want 1: %q", len(rec.fatals), rec.fatals)
	}
}

// recorder is a [testing.TB] that records what a helper reported instead of
// failing the test.
//
// It is how an assertion helper's *message* gets tested, which is the only part
// of it that matters: every one of these helpers exists because the message it
// prints is what somebody reads in CI. The embedded TB supplies the interface's
// unexported methods and nothing else — every method a helper here calls is
// overridden below, and a helper that started calling another one would panic on
// a nil embedded value rather than quietly pass.
type recorder struct {
	testing.TB
	fatals []string
	errors []string
	logs   []string
	skips  []string
}

func (r *recorder) Helper() {}

func (r *recorder) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recorder) Logf(format string, args ...any) {
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *recorder) Skipf(format string, args ...any) {
	r.skips = append(r.skips, fmt.Sprintf(format, args...))
}

// Context is what [Exec] derives its timeout from; the recorder's is never
// cancelled, because a recorder outlives no test.
func (r *recorder) Context() context.Context { return context.Background() }
