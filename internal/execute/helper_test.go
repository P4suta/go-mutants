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

type call struct {
	Argv        []string
	Dir         string
	Env         []string
	Timeout     time.Duration
	Kind        string
	Subject     string
	OutputLimit int
}

func (c call) active() string {
	for _, entry := range c.Env {
		if key, value, ok := strings.Cut(entry, "="); ok && key == instrument.ActiveEnv {
			return value
		}
	}
	return ""
}

func (c call) program() string {
	if len(c.Argv) == 0 {
		return ""
	}
	return c.Argv[0]
}

type fake struct {
	respond func(ctx context.Context, c call) runner.Result

	record bool

	mu    sync.Mutex
	calls []call
}

func (f *fake) run(ctx context.Context, spec runner.Spec) runner.Result {
	c := call{
		Argv:        slices.Clone(spec.Argv),
		Dir:         spec.Dir,
		Env:         slices.Clone(spec.Env),
		Timeout:     spec.Timeout,
		Kind:        spec.Kind,
		Subject:     spec.Subject,
		OutputLimit: spec.OutputLimit,
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()

	var result runner.Result
	if f.respond != nil {
		result = f.respond(ctx, c)
	}
	if f.record {
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

func (f *fake) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fake) programs() []string {
	seen := f.seen()
	out := make([]string, len(seen))
	for i, c := range seen {
		out[i] = c.program()
	}
	return out
}

func options(f *fake, jobs int) execute.Options {
	return execute.WithRunner(execute.Options{Jobs: jobs}, f.run)
}

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

func traced(t *testing.T, f *fake, opts execute.Options) (execute.Options, *trace.MemorySink) {
	t.Helper()
	recorder, sink := recording(t)
	f.record = true
	opts.Trace = recorder
	return opts, sink
}

func eventsOf(sink *trace.MemorySink, eventType string) []trace.Event {
	var found []trace.Event
	for _, event := range sink.Events() {
		if event.Type == eventType {
			found = append(found, event)
		}
	}
	return found
}

func execSeqs(sink *trace.MemorySink) []int64 {
	events := eventsOf(sink, trace.TypeExec)
	seqs := make([]int64, len(events))
	for i, event := range events {
		seqs[i] = event.Seq
	}
	return seqs
}

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

func mutants(timeout time.Duration, ids ...string) []execute.MutantRun {
	out := make([]execute.MutantRun, len(ids))
	for i, id := range ids {
		out[i] = execute.MutantRun{ID: id, Timeout: timeout}
	}
	return out
}

func failed(output string) runner.Result {
	return runner.Result{ExitCode: 1, Duration: time.Millisecond, Output: []byte(output)}
}

func passed() runner.Result {
	return runner.Result{ExitCode: 0, Duration: time.Millisecond, Output: []byte("PASS\n")}
}

func timedOut() runner.Result {
	return runner.Result{
		ExitCode: runner.ExitCodeUnavailable,
		TimedOut: true,
		Duration: time.Millisecond,
		Output:   []byte("panic: test timed out\n"),
	}
}

func staleCatalog() runner.Result {
	return runner.Result{
		ExitCode: instrument.UnknownMutantExit,
		Duration: time.Millisecond,
		Output:   []byte("go-mutants: unknown mutant, stale catalog\n"),
	}
}

func cancelled() runner.Result {
	return runner.Result{ExitCode: runner.ExitCodeUnavailable, Duration: time.Millisecond}
}

func isCancellation(err error) bool { return errors.Is(err, context.Canceled) }

func envValue(env []string, name string) string {
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func statDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func unstartable() runner.Result {
	return runner.Result{
		ExitCode: runner.ExitCodeUnavailable,
		Err:      &runner.Error{Code: runner.CodeProcessStartFailed, Message: "could not start it"},
	}
}

func probeUnavailable() runner.Result {
	return runner.Result{
		ExitCode: instrument.ProbeUnavailableExit,
		Duration: time.Millisecond,
		Output:   []byte("go-mutants: cannot open the infection log\n"),
	}
}
