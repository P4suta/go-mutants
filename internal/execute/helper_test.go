// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

// A call is one invocation the fake runner saw, captured by value so that a
// later invocation cannot rewrite what an earlier assertion is about.
type call struct {
	Argv    []string
	Dir     string
	Env     []string
	Timeout time.Duration
	// Kind and Subject are the label the call site put on the execution. They
	// are captured here, beside the argv, because a missing label is invisible
	// in everything else a call carries: the command runs, the run is right, and
	// the recording is the only thing that is poorer for it.
	Kind    string
	Subject string
}

// active returns the activation identity this call carried, or "" if it carried
// none.
func (c call) active() string {
	for _, entry := range c.Env {
		if key, value, ok := strings.Cut(entry, "="); ok && key == instrument.ActiveEnv {
			return value
		}
	}
	return ""
}

// program is the executable this call started, without its directory. It is
// what the tests below name a binary by.
func (c call) program() string {
	if len(c.Argv) == 0 {
		return ""
	}
	return c.Argv[0]
}

// fake is a [runner.Run] stand-in that records every call and answers from a
// caller-supplied rule.
//
// Injecting here rather than compiling fixture programs is deliberate. What
// these tests are about — which binary is tried next, which timeout is retried,
// what two disagreeing attempts mean — is entirely a function of what the
// runner returns, and a fixture that produced exit 97 on two platforms would be
// testing the fixture.
type fake struct {
	// respond decides one call's result. It runs on worker goroutines and must
	// be safe for concurrent use.
	respond func(ctx context.Context, c call) runner.Result

	// record makes the fake do the one thing [runner.Run] does besides starting
	// a process: hand the execution to the spec's recorder and return the
	// sequence it was recorded at. It is opt-in so that a test which is not
	// about the recording sees exactly the runner it always saw, and so that a
	// test which is about it can assert that an attempt's sequences are the
	// sequences of the commands underneath it.
	record bool

	mu    sync.Mutex
	calls []call
}

// run is the function handed to [execute.WithRunner].
func (f *fake) run(ctx context.Context, spec runner.Spec) runner.Result {
	c := call{
		Argv:    slices.Clone(spec.Argv),
		Dir:     spec.Dir,
		Env:     slices.Clone(spec.Env),
		Timeout: spec.Timeout,
		Kind:    spec.Kind,
		Subject: spec.Subject,
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()

	var result runner.Result
	if f.respond != nil {
		result = f.respond(ctx, c)
	}
	if f.record {
		// The reduction of the environment to names and of the output to a
		// digest is the recorder's, exactly as it is for the real runner, so a
		// fake cannot record something the production path could not.
		result.TraceSeq = spec.Trace.Exec(trace.ExecRecord{
			Kind:       spec.Kind,
			Subject:    spec.Subject,
			Argv:       spec.Argv,
			Dir:        spec.Dir,
			EnvNames:   spec.Env,
			TimeoutMS:  spec.Timeout.Milliseconds(),
			ExitCode:   result.ExitCode,
			TimedOut:   result.TimedOut,
			DurationMS: result.Duration.Milliseconds(),
			Output:     result.Output,
		})
	}
	return result
}

// seen returns the calls recorded so far, in order.
func (f *fake) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// programs returns the executables the fake was asked to start, in order.
func (f *fake) programs() []string {
	seen := f.seen()
	out := make([]string, len(seen))
	for i, c := range seen {
		out[i] = c.program()
	}
	return out
}

// options wires a fake into an otherwise empty [execute.Options].
func options(f *fake, jobs int) execute.Options {
	return execute.WithRunner(execute.Options{Jobs: jobs}, f.run)
}

// recording opens a recorder over an unbounded ring and returns both, so a test
// can assert on exactly what this package handed it.
func recording(t *testing.T) (*trace.Recorder, *trace.MemorySink) {
	t.Helper()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, time.Now, trace.StartRecord{
		Kind:        trace.StartKindRun,
		ToolVersion: "test",
		PID:         os.Getpid(),
		Root:        t.TempDir(),
	})
	if recorder == nil {
		t.Fatal("trace.New returned no recorder for a real sink")
	}
	return recorder, sink
}

// traced hands options a recorder and tells the fake to record its calls into
// it the way [runner.Run] would.
func traced(t *testing.T, f *fake, opts execute.Options) (execute.Options, *trace.MemorySink) {
	t.Helper()
	recorder, sink := recording(t)
	f.record = true
	opts.Trace = recorder
	return opts, sink
}

// eventsOf returns every event of one type the sink kept, in order.
func eventsOf(sink *trace.MemorySink, eventType string) []trace.Event {
	var found []trace.Event
	for _, event := range sink.Events() {
		if event.Type == eventType {
			found = append(found, event)
		}
	}
	return found
}

// execSeqs returns the sequence numbers of the exec events the sink kept, in
// order, so an attempt's own list can be compared against the commands the
// recording actually holds.
func execSeqs(sink *trace.MemorySink) []int64 {
	events := eventsOf(sink, trace.TypeExec)
	seqs := make([]int64, len(events))
	for i, event := range events {
		seqs[i] = event.Seq
	}
	return seqs
}

// testBins builds a run's worth of test binaries named after their import
// paths, so that an assertion can name one by the string it was created with.
func testBins(importPaths ...string) []execute.TestBinary {
	out := make([]execute.TestBinary, len(importPaths))
	for i, path := range importPaths {
		out[i] = execute.TestBinary{
			ImportPath: path,
			Dir:        "/snapshot/" + path,
			BinPath:    path + ".test",
		}
	}
	return out
}

// mutants builds a queue of runs that all share one timeout.
func mutants(timeout time.Duration, ids ...string) []execute.MutantRun {
	out := make([]execute.MutantRun, len(ids))
	for i, id := range ids {
		out[i] = execute.MutantRun{ID: id, Timeout: timeout}
	}
	return out
}

// failed is the result of a test binary whose tests failed.
func failed(output string) runner.Result {
	return runner.Result{ExitCode: 1, Duration: time.Millisecond, Output: []byte(output)}
}

// passed is the result of a test binary whose tests all passed.
func passed() runner.Result {
	return runner.Result{ExitCode: 0, Duration: time.Millisecond, Output: []byte("PASS\n")}
}

// timedOut is what internal/runner reports for a child it had to kill on the
// timeout: no exit status, TimedOut set, no error.
func timedOut() runner.Result {
	return runner.Result{
		ExitCode: runner.ExitCodeUnavailable,
		TimedOut: true,
		Duration: time.Millisecond,
		Output:   []byte("panic: test timed out\n"),
	}
}

// staleCatalog is the generated runtime refusing an identity it does not know.
func staleCatalog() runner.Result {
	return runner.Result{
		ExitCode: instrument.UnknownMutantExit,
		Duration: time.Millisecond,
		Output:   []byte("go-mutants: unknown mutant, stale catalog\n"),
	}
}

// cancelled is what internal/runner reports for a child killed by a cancelled
// context: no exit status, no timeout, no error.
func cancelled() runner.Result {
	return runner.Result{ExitCode: runner.ExitCodeUnavailable, Duration: time.Millisecond}
}

// isCancellation reports whether err carries a cancellation, which is how
// internal/engine tells a Ctrl-C apart from a broken run.
func isCancellation(err error) bool { return errors.Is(err, context.Canceled) }

// envValue reads one variable out of a captured child environment. The lookup
// is case-insensitive because Windows environment names are, and a child
// composed on Windows may well have inherited "Path" rather than "PATH".
func envValue(env []string, name string) string {
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

// writeFile puts a regular file where a test needs one, most often so that a
// directory creation has something in its way.
func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// statDir reports whether path exists and is a directory.
func statDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// unstartable is a process that could not be started at all.
func unstartable() runner.Result {
	return runner.Result{
		ExitCode: runner.ExitCodeUnavailable,
		Err:      &runner.Error{Code: runner.CodeProcessStartFailed, Message: "could not start it"},
	}
}

// probeUnavailable is the generated probe runtime refusing to run because the
// log it was told to write cannot be opened.
func probeUnavailable() runner.Result {
	return runner.Result{
		ExitCode: instrument.ProbeUnavailableExit,
		Duration: time.Millisecond,
		Output:   []byte("go-mutants: cannot open the infection log\n"),
	}
}
