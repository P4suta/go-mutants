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

const DefaultTimeout = 5 * time.Minute

const ExitCodeUnavailable = -1

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

func Exec(t testing.TB, dir string, env []string, argv ...string) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), DefaultTimeout)
	defer cancel()
	return ExecContext(ctx, t, dir, env, argv...)
}

func ExecContext(ctx context.Context, t testing.TB, dir string, env []string, argv ...string) Result {
	t.Helper()
	if len(argv) == 0 {
		t.Fatalf("Exec was given no argv to run in %s", dir)
		return Result{Dir: dir, ExitCode: ExitCodeUnavailable}
	}

	var merged mergedOutput
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = io.MultiWriter(&merged, &stdout)
	cmd.Stderr = &merged
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
	rememberChild(t, result)
	return result
}

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

func RequireOutput(t testing.TB, r Result, what string, needles ...string) {
	t.Helper()
	out := string(r.Output)
	for _, needle := range needles {
		if !strings.Contains(out, needle) {
			t.Errorf("%s did not print %q:\n%s", what, needle, out)
		}
	}
}

func RequireNoOutput(t testing.TB, r Result, what string, needles ...string) {
	t.Helper()
	out := string(r.Output)
	for _, needle := range needles {
		if strings.Contains(out, needle) {
			t.Errorf("%s printed %q, which it should not have:\n%s", what, needle, out)
		}
	}
}

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

func (r Result) command() string {
	quoted := make([]string, 0, len(r.Argv))
	for _, arg := range r.Argv {
		quoted = append(quoted, strconv.Quote(arg))
	}
	return "$ (in " + strconv.Quote(r.Dir) + ") " + strings.Join(quoted, " ")
}
