// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const execHelperEnv = "TESTKIT_EXEC_HELPER"

func TestExecHelperProcess(t *testing.T) {
	SkipUnlessHelper(t, execHelperEnv)
	status := os.Getenv(execHelperEnv)
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

func helperArgv(extra ...string) []string {
	return append([]string{os.Args[0], "-test.run=^TestExecHelperProcess$", "--"}, extra...)
}

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

func TestRequireExitReportsAnAbandonedChild(t *testing.T) {
	t.Parallel()

	rec := expectFatal(t, func(tb testing.TB) {
		RequireExit(tb, Result{Argv: []string{"go", "test"}, Abandoned: true, ExitCode: ExitCodeUnavailable}, 0, "the suite")
	})
	if report := rec.first(t, "RequireExit on an abandoned child"); !strings.Contains(report, "abandoned") {
		t.Errorf("the report does not say the caller walked away:\n%s", report)
	}
}

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

func TestRequireExitQuotesTheChildOutputOnMismatch(t *testing.T) {
	t.Parallel()

	rec := expectFatal(t, func(tb testing.TB) {
		RequireExit(tb, Result{
			Argv:     []string{"go", "test", "./..."},
			ExitCode: 1,
			Output:   []byte("--- FAIL: TestSomething\n\tsomething_test.go:12: 2 != 3\n"),
		}, 0, "the fixture's suite")
	})

	report := rec.first(t, "RequireExit on a mismatched status")
	for _, want := range []string{"the fixture's suite", "exited 1", "want 0", "2 != 3"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not mention %q:\n%s", want, report)
		}
	}
}

func TestRequireExitReportsATimeoutRatherThanAnExitStatus(t *testing.T) {
	t.Parallel()

	rec := expectFatal(t, func(tb testing.TB) {
		RequireExit(tb, Result{
			Argv:     []string{"go", "test", "./..."},
			TimedOut: true,
			ExitCode: ExitCodeUnavailable,
			Output:   []byte("=== RUN   TestHangs\n"),
			Duration: DefaultTimeout,
		}, 0, "the fixture's suite")
	})

	report := rec.first(t, "RequireExit on a child that ran out of time")
	for _, want := range []string{"did not finish", DefaultTimeout.String(), "TestHangs"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not mention %q:\n%s", want, report)
		}
	}
}

func TestRequireExitReportsAChildThatCouldNotBeStarted(t *testing.T) {
	t.Parallel()

	rec := expectFatal(t, func(tb testing.TB) {
		RequireExit(tb, Result{
			Argv:     []string{"git", "commit"},
			ExitCode: ExitCodeUnavailable,
			Err:      errors.New("exec: \"git\": executable file not found in $PATH"),
		}, 0, "the commit")
	})

	if report := rec.first(t, "RequireExit on a child that never started"); !strings.Contains(report, "could not be run") {
		t.Errorf("the report reads as a failing child rather than a failing harness:\n%s", report)
	}
}

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

func TestExecRefusesAnEmptyArgv(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rec := expectFatal(t, func(tb testing.TB) { Exec(tb, dir, nil) })
	rec.first(t, "Exec with no argv")
}

type recorder struct {
	testing.TB
	stop   bool
	fatals []string
	errors []string
	logs   []string
	skips  []string
}

func expectFatal(t testing.TB, call func(testing.TB)) *recorder {
	t.Helper()
	rec := &recorder{TB: t, stop: true}
	done := make(chan struct{})
	go func() {
		defer close(done)
		call(rec)
	}()
	<-done
	return rec
}

func (r *recorder) first(t testing.TB, what string) string {
	t.Helper()
	if len(r.fatals) == 0 {
		t.Fatalf("%s reported nothing, want a refusal", what)
	}
	return r.fatals[0]
}

func (r *recorder) Helper() {}

func (r *recorder) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
	if r.stop {
		runtime.Goexit()
	}
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

func (r *recorder) Context() context.Context { return context.Background() }
