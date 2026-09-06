// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit_test

import (
	"context"
	"fmt"
	"runtime"
	"testing"
)

// recorder is a [testing.TB] that records what a helper reported instead of
// failing the test.
//
// It is how an assertion helper's *message* gets tested, which is the only part
// of it that matters: every helper here exists because the message it prints is
// what somebody reads in CI.
//
// The embedded TB is left nil, and that is the load-bearing part. It is there to
// supply the interface's unexported methods and nothing else: every method the
// helpers in this package reach for — Helper, Fatalf, Errorf, Logf, Context — is
// overridden below, so a helper that grew a call to a sixth one panics here,
// loudly, naming the line. Embedding the parent's *testing.T instead would route
// that call to the real test, where a Fatalf inside a recorded call would fail
// the very test that was checking a helper refuses — quietly turning a
// behavioural assertion into whatever the parent did next.
//
// It is written out rather than shared with internal/testkit's, deliberately: a
// recorder any package could hand to a real helper is a way to make a failing
// assertion pass. The harness's own copy does embed its parent, because two of
// its call sites drive [testkit.Env], which reaches for t.TempDir and t.Setenv.
type recorder struct {
	testing.TB
	stop   bool
	fatals []string
	errors []string
	logs   []string
}

// expectFatal runs a call that is expected to end the test, and returns what it
// reported.
//
// The call runs on a goroutine of its own so that the recorder can end it with
// runtime.Goexit, which is how testing.T's own FailNow stops a test: a helper's
// Fatalf never returns under a real testing.T, so everything written after it is
// written on the assumption that it is unreachable, and a fake that let it run
// would carry straight on into the work the helper had just refused.
func expectFatal(t testing.TB, call func(testing.TB)) *recorder {
	t.Helper()
	rec := &recorder{stop: true}
	done := make(chan struct{})
	go func() {
		defer close(done)
		call(rec)
	}()
	<-done
	return rec
}

// first returns the first fatal report, failing the test when there was none.
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

// Context is what the harness derives a child's timeout from; a recorder
// outlives no test, so its context is never cancelled.
func (r *recorder) Context() context.Context { return context.Background() }
