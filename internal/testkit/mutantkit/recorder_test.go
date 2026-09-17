// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit_test

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

type recorder struct {
	testing.TB
	name     string
	stop     bool
	failed   bool
	cleanups []func()
	fatals   []string
	errors   []string
	logs     []string
}

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

func (r *recorder) first(t testing.TB, what string) string {
	t.Helper()
	if len(r.fatals) == 0 {
		t.Fatalf("%s reported nothing, want a refusal", what)
	}
	return r.fatals[0]
}

func (r *recorder) Helper() {}

func (r *recorder) Fatalf(format string, args ...any) {
	r.failed = true
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
	if r.stop {
		runtime.Goexit()
	}
}

func (r *recorder) Errorf(format string, args ...any) {
	r.failed = true
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recorder) Logf(format string, args ...any) {
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *recorder) Context() context.Context { return context.Background() }

func (r *recorder) Name() string { return r.name }

func (r *recorder) Failed() bool { return r.failed }

func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

func (r *recorder) run(body func(testing.TB)) {
	r.stop = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		body(r)
	}()
	<-done
}

func (r *recorder) finish() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
	r.cleanups = nil
}

func (r *recorder) log() string { return strings.Join(r.logs, "\n") }
