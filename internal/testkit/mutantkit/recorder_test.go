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
// what somebody reads in CI. The embedded TB supplies the interface's unexported
// methods and nothing else — every method a helper here calls is overridden
// below, so a helper that started calling another one panics on a nil embedded
// value rather than quietly passing.
//
// It is a copy of internal/testkit's own rather than an export of it, and
// deliberately so: a recorder any package could hand a real helper is a way to
// make a failing assertion pass.
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
	rec := &recorder{TB: t, stop: true}
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
