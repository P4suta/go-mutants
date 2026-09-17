// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

const mutantTimeout = 7 * time.Second

func TestRunOneStopsAtTheFirstFailingBinary(t *testing.T) {
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if c.program() == "example.com/b.test" {
			return failed("--- FAIL: TestB\n    b_test.go:9: got 2, want 1\n")
		}
		return passed()
	}}
	bins := testBins("example.com/a", "example.com/b", "example.com/c")

	attempt := execute.RunOne(t.Context(), options(f, 1),
		execute.MutantRun{ID: "abc123", Timeout: mutantTimeout}, bins)

	if attempt.Outcome != mutation.OutcomeKilled {
		t.Errorf("outcome = %s, want %s", attempt.Outcome, mutation.OutcomeKilled)
	}
	if want := "example.com/b"; attempt.KilledBy != want {
		t.Errorf("killed by %q, want %q", attempt.KilledBy, want)
	}
	if want := []string{"example.com/a.test", "example.com/b.test"}; !slices.Equal(f.programs(), want) {
		t.Errorf("started %q, want %q — the binary after the failure must not run", f.programs(), want)
	}
	if !strings.Contains(attempt.OutputTail, "--- FAIL: TestB") {
		t.Errorf("output tail = %q, want the failing binary's output", attempt.OutputTail)
	}
	if want := 2 * time.Millisecond; attempt.Duration != want {
		t.Errorf("duration = %s, want %s", attempt.Duration, want)
	}
}

func TestRunOneSurvivesWhenEveryBinaryPasses(t *testing.T) {
	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	bins := testBins("example.com/a", "example.com/b", "example.com/c")

	attempt := execute.RunOne(t.Context(), options(f, 1),
		execute.MutantRun{ID: "abc123", Timeout: mutantTimeout}, bins)

	if attempt.Outcome != mutation.OutcomeSurvived {
		t.Errorf("outcome = %s, want %s", attempt.Outcome, mutation.OutcomeSurvived)
	}
	if attempt.KilledBy != "" {
		t.Errorf("killed by %q, want no binary named for a survivor", attempt.KilledBy)
	}
	if attempt.OutputTail != "" {
		t.Errorf("output tail = %q, want empty: a survivor's output is thousands of lines of nothing wrong",
			attempt.OutputTail)
	}
	if got := len(f.seen()); got != len(bins) {
		t.Errorf("started %d binaries, want %d", got, len(bins))
	}
}

func TestRunOnePassesTargetArgumentsAndUsesTheSuppliedEnvironment(t *testing.T) {
	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts := options(f, 1)
	opts.Env = []string{
		"FROZEN=value",
		"GO_MUTANTS_ACTIVE=from-caller",
		"TMP=from-caller",
		"TEMP=from-caller",
		"TMPDIR=from-caller",
	}
	opts.ScratchDir = t.TempDir()

	attempt := execute.RunOne(t.Context(), opts, execute.MutantRun{
		ID:      "abc123",
		Timeout: mutantTimeout,
		Args:    []string{"-test.run=^TestRoundTrip$", "-test.count=1"},
	}, testBins("example.com/a"))
	if attempt.Outcome != mutation.OutcomeSurvived {
		t.Fatalf("outcome = %s, want %s (%v)", attempt.Outcome, mutation.OutcomeSurvived, attempt.Err)
	}
	seen := f.seen()
	if len(seen) != 1 {
		t.Fatalf("started %d processes, want 1", len(seen))
	}
	wantArgs := []string{
		"example.com/a.test",
		"-test.timeout=14s",
		execute.FailFastFlag,
		"-test.run=^TestRoundTrip$",
		"-test.count=1",
	}
	if !slices.Equal(seen[0].Argv, wantArgs) {
		t.Errorf("argv = %q, want %q", seen[0].Argv, wantArgs)
	}
	if got := envValue(seen[0].Env, "FROZEN"); got != "value" {
		t.Errorf("FROZEN = %q, want value", got)
	}
	if got := envValue(seen[0].Env, instrument.ActiveEnv); got != "abc123" {
		t.Errorf("%s = %q, want abc123", instrument.ActiveEnv, got)
	}
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		if got := envValue(seen[0].Env, key); got != opts.ScratchDir {
			t.Errorf("%s = %q, want %q", key, got, opts.ScratchDir)
		}
	}
}

func TestRunOnePassesTheOutputLimitAndKeepsTheDecidingOutput(t *testing.T) {
	t.Run("the caller's limit reaches the spec", func(t *testing.T) {
		f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}

		execute.RunOne(t.Context(), options(f, 1), execute.MutantRun{
			ID:          "abc123",
			Timeout:     mutantTimeout,
			OutputLimit: 777,
		}, testBins("example.com/a"))

		seen := f.seen()
		if len(seen) != 1 {
			t.Fatalf("started %d processes, want 1", len(seen))
		}
		if seen[0].OutputLimit != 777 {
			t.Errorf("spec OutputLimit = %d, want the run's 777", seen[0].OutputLimit)
		}
	})

	t.Run("a kill keeps the deciding binary's capture", func(t *testing.T) {
		deciding := runner.Result{
			ExitCode:    1,
			Duration:    time.Millisecond,
			Output:      []byte(runner.OutputTruncatedPrefix + ": …\n--- FAIL: TestB\n"),
			OutputBytes: 1 << 20,
			Truncated:   true,
		}
		f := &fake{respond: func(_ context.Context, c call) runner.Result {
			if c.program() == "example.com/b.test" {
				return deciding
			}
			return passed()
		}}

		attempt := execute.RunOne(t.Context(), options(f, 1),
			execute.MutantRun{ID: "abc123", Timeout: mutantTimeout, OutputLimit: 777},
			testBins("example.com/a", "example.com/b", "example.com/c"))

		if attempt.Outcome != mutation.OutcomeKilled {
			t.Fatalf("outcome = %s, want %s (%v)", attempt.Outcome, mutation.OutcomeKilled, attempt.Err)
		}
		if !bytes.Equal(attempt.Output, deciding.Output) {
			t.Errorf("Output = %q, want the deciding binary's capture %q", attempt.Output, deciding.Output)
		}
		if !attempt.Truncated {
			t.Error("Truncated = false although the deciding capture lost bytes")
		}
		if attempt.OutputBytes != deciding.OutputBytes {
			t.Errorf("OutputBytes = %d, want the deciding binary's %d", attempt.OutputBytes, deciding.OutputBytes)
		}
		if !strings.Contains(attempt.OutputTail, "--- FAIL: TestB") {
			t.Errorf("OutputTail = %q, want the deciding binary's own output", attempt.OutputTail)
		}
	})

	t.Run("a survivor keeps none of it", func(t *testing.T) {
		f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}

		attempt := execute.RunOne(t.Context(), options(f, 1),
			execute.MutantRun{ID: "abc123", Timeout: mutantTimeout, OutputLimit: 777},
			testBins("example.com/a", "example.com/b"))

		if attempt.Outcome != mutation.OutcomeSurvived {
			t.Fatalf("outcome = %s, want %s (%v)", attempt.Outcome, mutation.OutcomeSurvived, attempt.Err)
		}
		if attempt.Output != nil {
			t.Errorf("Output = %q, want nothing: a survivor's output is thousands of lines of nothing wrong",
				attempt.Output)
		}
		if attempt.OutputBytes != 0 || attempt.Truncated {
			t.Errorf("OutputBytes = %d and Truncated = %v, want a survivor to report neither",
				attempt.OutputBytes, attempt.Truncated)
		}
	})
}

func TestRunOneRefusesATargetTimeoutOverride(t *testing.T) {
	for _, argument := range []string{
		"-test.timeout=0",
		"-test.timeout",
		"--test.timeout=0",
		"--test.timeout",
	} {
		t.Run(argument, func(t *testing.T) {
			f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
			attempt := execute.RunOne(t.Context(), options(f, 1), execute.MutantRun{
				ID:      "abc123",
				Timeout: mutantTimeout,
				Args:    []string{argument},
			}, testBins("example.com/a"))
			if got := execute.CodeOf(attempt.Err); got != execute.CodeMutantInvalid {
				t.Errorf("code = %q, want %q (%v)", got, execute.CodeMutantInvalid, attempt.Err)
			}
			if got := len(f.seen()); got != 0 {
				t.Errorf("started %d processes, want none", got)
			}
		})
	}
}

func TestRunOneReportsAStaleCatalogRatherThanAKill(t *testing.T) {
	f := &fake{respond: func(context.Context, call) runner.Result { return staleCatalog() }}

	attempt := execute.RunOne(t.Context(), options(f, 1),
		execute.MutantRun{ID: strings.Repeat("0", 64), Timeout: mutantTimeout},
		testBins("example.com/a", "example.com/b"))

	if attempt.Outcome != mutation.OutcomeErrored {
		t.Errorf("outcome = %s, want %s", attempt.Outcome, mutation.OutcomeErrored)
	}
	if got := execute.CodeOf(attempt.Err); got != execute.CodeStaleCatalog {
		t.Errorf("code = %q, want %q (%v)", got, execute.CodeStaleCatalog, attempt.Err)
	}
	if attempt.KilledBy != "" {
		t.Errorf("killed by %q, want no binary credited with a detection that did not happen", attempt.KilledBy)
	}
	if got := len(f.seen()); got != 1 {
		t.Errorf("started %d binaries, want 1: a stale catalogue is not a per-package fact", got)
	}
}

func TestRunOneReportsATimeoutWithoutCallingItDetection(t *testing.T) {
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if c.program() == "example.com/b.test" {
			return timedOut()
		}
		return passed()
	}}

	attempt := execute.RunOne(t.Context(), options(f, 1),
		execute.MutantRun{ID: "abc123", Timeout: mutantTimeout},
		testBins("example.com/a", "example.com/b", "example.com/c"))

	if attempt.Outcome != mutation.OutcomeTimedOut {
		t.Errorf("outcome = %s, want %s", attempt.Outcome, mutation.OutcomeTimedOut)
	}
	if want := "example.com/b"; attempt.KilledBy != want {
		t.Errorf("timed out in %q, want %q", attempt.KilledBy, want)
	}
	if got := len(f.seen()); got != 2 {
		t.Errorf("started %d binaries, want 2: a timeout ends the attempt", got)
	}
}

func TestRunOneReportsAProcessThatWouldNotStart(t *testing.T) {
	f := &fake{respond: func(context.Context, call) runner.Result { return unstartable() }}

	attempt := execute.RunOne(t.Context(), options(f, 1),
		execute.MutantRun{ID: "abc123", Timeout: mutantTimeout}, testBins("example.com/a"))

	if attempt.Outcome != mutation.OutcomeErrored {
		t.Errorf("outcome = %s, want %s", attempt.Outcome, mutation.OutcomeErrored)
	}
	if got := execute.CodeOf(attempt.Err); got != execute.CodeMutantStart {
		t.Errorf("code = %q, want %q", got, execute.CodeMutantStart)
	}
	var runnerErr *runner.Error
	if !errors.As(attempt.Err, &runnerErr) || runnerErr.Code != runner.CodeProcessStartFailed {
		t.Errorf("the runner's own cause is not reachable through %v", attempt.Err)
	}
}

func TestRunOneReportsACancelledChildAsNotRun(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	f := &fake{respond: func(context.Context, call) runner.Result {
		cancel()
		return cancelled()
	}}

	attempt := execute.RunOne(ctx, options(f, 1),
		execute.MutantRun{ID: "abc123", Timeout: mutantTimeout},
		testBins("example.com/a", "example.com/b"))

	if attempt.Outcome != mutation.OutcomeNotRun {
		t.Errorf("outcome = %s, want %s", attempt.Outcome, mutation.OutcomeNotRun)
	}
	if got := len(f.seen()); got != 1 {
		t.Errorf("started %d binaries, want 1: a cancelled run stops rather than draining", got)
	}
}

func TestRunOneKeepsAResultThatLandedBeforeTheCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	f := &fake{respond: func(context.Context, call) runner.Result {
		cancel()
		return failed("--- FAIL: TestA\n")
	}}

	attempt := execute.RunOne(ctx, options(f, 1),
		execute.MutantRun{ID: "abc123", Timeout: mutantTimeout}, testBins("example.com/a"))

	if attempt.Outcome != mutation.OutcomeKilled {
		t.Errorf("outcome = %s, want %s: a child that answered is data, whatever the context says next",
			attempt.Outcome, mutation.OutcomeKilled)
	}
}

func TestRunOneRefusesAMutantItCannotMeasure(t *testing.T) {
	bins := testBins("example.com/a")
	cases := []struct {
		name string
		m    execute.MutantRun
		bins []execute.TestBinary
		code execute.Code
	}{
		{
			name: "no activation identity",
			m:    execute.MutantRun{Timeout: mutantTimeout},
			bins: bins,
			code: execute.CodeMutantInvalid,
		},
		{
			name: "no timeout",
			m:    execute.MutantRun{ID: "abc123"},
			bins: bins,
			code: execute.CodeMutantInvalid,
		},
		{
			name: "no test binaries",
			m:    execute.MutantRun{ID: "abc123", Timeout: mutantTimeout},
			bins: nil,
			code: execute.CodeNoTestBinaries,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{}
			attempt := execute.RunOne(t.Context(), options(f, 1), c.m, c.bins)

			if attempt.Outcome != mutation.OutcomeErrored {
				t.Errorf("outcome = %s, want %s", attempt.Outcome, mutation.OutcomeErrored)
			}
			if got := execute.CodeOf(attempt.Err); got != c.code {
				t.Errorf("code = %q, want %q (%v)", got, c.code, attempt.Err)
			}
			if got := len(f.seen()); got != 0 {
				t.Errorf("started %d processes, want none", got)
			}
		})
	}
}

func TestRunOneComposesTheChildInvocation(t *testing.T) {
	scratch := t.TempDir()
	inherited := t.TempDir()

	t.Setenv("GO_MUTANTS_ACTIVE", "an-identity-from-the-users-shell")
	t.Setenv("GO_MUTANTS_SOMETHING_ELSE", "also-scrubbed")
	t.Setenv("TMPDIR", inherited)
	t.Setenv("TMP", inherited)
	t.Setenv("TEMP", inherited)

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts := execute.WithRunner(execute.Options{ScratchDir: scratch}, f.run)

	execute.RunOne(t.Context(), opts,
		execute.MutantRun{ID: "deadbeef", Timeout: mutantTimeout}, testBins("example.com/a"))

	seen := f.seen()
	if len(seen) != 1 {
		t.Fatalf("started %d processes, want 1", len(seen))
	}
	c := seen[0]

	want := []string{"example.com/a.test", "-test.timeout=14s", execute.FailFastFlag}
	if !slices.Equal(c.Argv, want) {
		t.Errorf("argv = %q, want %q", c.Argv, want)
	}
	if c.Timeout != mutantTimeout {
		t.Errorf("supervisor timeout = %s, want %s", c.Timeout, mutantTimeout)
	}
	if want := "/snapshot/example.com/a"; c.Dir != want {
		t.Errorf("working directory = %q, want %q (testdata resolves relative to it)", c.Dir, want)
	}
	if got := c.active(); got != "deadbeef" {
		t.Errorf("%s = %q, want %q", instrument.ActiveEnv, got, "deadbeef")
	}
	for _, entry := range c.Env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GO_MUTANTS_") && key != instrument.ActiveEnv {
			t.Errorf("the child inherited %q; every GO_MUTANTS_ variable but the activation must be scrubbed", entry)
		}
	}
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		if got := envValue(c.Env, key); got != scratch {
			t.Errorf("%s = %q, want the worker's scratch directory %q", key, got, scratch)
		}
	}
}

func TestRunOneCreatesTheWorkerScratchDirectory(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "workers", "w3")
	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts := execute.WithRunner(execute.Options{ScratchDir: scratch}, f.run)

	attempt := execute.RunOne(t.Context(), opts,
		execute.MutantRun{ID: "abc123", Timeout: mutantTimeout}, testBins("example.com/a"))

	if attempt.Outcome != mutation.OutcomeSurvived {
		t.Fatalf("outcome = %s, want %s (%v)", attempt.Outcome, mutation.OutcomeSurvived, attempt.Err)
	}
	if info, err := statDir(scratch); err != nil || !info {
		t.Errorf("the scratch directory %s was not created: %v", scratch, err)
	}
}

func TestRunOneResolvesARelativeScratchDirectory(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts := execute.WithRunner(execute.Options{ScratchDir: filepath.Join("run", "tmp", "w0")}, f.run)

	attempt := execute.RunOne(t.Context(), opts,
		execute.MutantRun{ID: "abc123", Timeout: mutantTimeout}, testBins("example.com/a"))
	if attempt.Outcome != mutation.OutcomeSurvived {
		t.Fatalf("outcome = %s, want %s (%v)", attempt.Outcome, mutation.OutcomeSurvived, attempt.Err)
	}

	seen := f.seen()
	if len(seen) != 1 {
		t.Fatalf("started %d processes, want 1", len(seen))
	}
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		got := envValue(seen[0].Env, key)
		if !filepath.IsAbs(got) {
			t.Errorf("%s = %q, want an absolute path: the child runs in %q, where a relative one names a directory in the snapshot",
				key, got, seen[0].Dir)
		}
	}
	if ok, err := statDir(filepath.Join(work, "run", "tmp", "w0")); err != nil || !ok {
		t.Errorf("the scratch directory was not created under the working directory: %v", err)
	}
}

func TestRunOneLeavesTheInheritedTempAloneWithoutAScratchDir(t *testing.T) {
	t.Setenv("TMPDIR", "/the/users/temp")
	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}

	execute.RunOne(t.Context(), options(f, 1),
		execute.MutantRun{ID: "abc123", Timeout: mutantTimeout}, testBins("example.com/a"))

	seen := f.seen()
	if len(seen) != 1 {
		t.Fatalf("started %d processes, want 1", len(seen))
	}
	if got := envValue(seen[0].Env, "TMPDIR"); got != "/the/users/temp" {
		t.Errorf("TMPDIR = %q, want the inherited value untouched", got)
	}
}

func TestMutantStartFailureCarriesTheBinarysInvocation(t *testing.T) {
	cases := []struct {
		name   string
		result runner.Result
		code   execute.Code
	}{
		{"the binary could not be started", unstartable(), execute.CodeMutantStart},
		{"the runtime does not know the mutant", staleCatalog(), execute.CodeStaleCatalog},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{respond: func(context.Context, call) runner.Result { return c.result }}
			bins := testBins("example.com/a")

			attempt := execute.RunOne(t.Context(), options(f, 1),
				execute.MutantRun{ID: strings.Repeat("0", 64), Timeout: mutantTimeout}, bins)

			if got := execute.CodeOf(attempt.Err); got != c.code {
				t.Fatalf("code = %q, want %q (%v)", got, c.code, attempt.Err)
			}
			var failure *execute.Error
			if !errors.As(attempt.Err, &failure) {
				t.Fatalf("err = %v, want an *execute.Error", attempt.Err)
			}
			command := failure.Command()
			if command == nil {
				t.Fatal("Command() = nil, want the test binary that was started")
			}
			started := f.seen()
			if len(started) != 1 {
				t.Fatalf("the fake saw %d calls, want 1", len(started))
			}
			if !slices.Equal(command.Argv, started[0].Argv) {
				t.Errorf("Command().Argv = %q, want the argv the binary was started with %q",
					command.Argv, started[0].Argv)
			}
			if command.Dir != bins[0].Dir {
				t.Errorf("Command().Dir = %q, want the package directory %q", command.Dir, bins[0].Dir)
			}
			if failure.Package != bins[0].ImportPath {
				t.Errorf("Package = %q, want the failing binary's import path %q",
					failure.Package, bins[0].ImportPath)
			}
		})
	}
}

func TestRunOneNamesTheBinaryACancellationCutOff(t *testing.T) {
	t.Run("a child the cancellation killed", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		f := &fake{respond: func(context.Context, call) runner.Result {
			cancel()
			return runner.Result{
				ExitCode: runner.ExitCodeUnavailable,
				Duration: time.Millisecond,
				Output:   []byte("=== RUN   TestSlow\n"),
			}
		}}
		bins := testBins("example.com/a")

		attempt := execute.RunOne(ctx, options(f, 1),
			execute.MutantRun{ID: "abc123", Timeout: mutantTimeout}, bins)

		if attempt.Outcome != mutation.OutcomeNotRun {
			t.Fatalf("outcome = %s, want %s", attempt.Outcome, mutation.OutcomeNotRun)
		}
		if attempt.OutputTail != "" {
			t.Errorf("OutputTail = %q, want empty: this attempt decided nothing", attempt.OutputTail)
		}
		if got := execute.CodeOf(attempt.Err); got != execute.CodeInterrupted {
			t.Fatalf("code = %q, want %q (%v)", got, execute.CodeInterrupted, attempt.Err)
		}
		if !isCancellation(attempt.Err) {
			t.Errorf("the cancellation is not reachable through %v", attempt.Err)
		}
		var failure *execute.Error
		if !errors.As(attempt.Err, &failure) {
			t.Fatalf("err = %v, want an *execute.Error", attempt.Err)
		}
		command := failure.Command()
		if command == nil {
			t.Fatal("Command() = nil, want the binary that was cut off")
		}
		started := f.seen()
		if len(started) != 1 {
			t.Fatalf("the fake saw %d calls, want 1", len(started))
		}
		if !slices.Equal(command.Argv, started[0].Argv) || command.Dir != bins[0].Dir {
			t.Errorf("Command() = %+v, want the argv and directory the binary was started with %q in %q",
				command, started[0].Argv, bins[0].Dir)
		}
		if failure.Package != bins[0].ImportPath {
			t.Errorf("Package = %q, want the binary that was cut off %q", failure.Package, bins[0].ImportPath)
		}
		if got := failure.RetainedOutput(); !strings.Contains(got, "TestSlow") {
			t.Errorf("RetainedOutput() = %q, want what the killed child had printed", got)
		}
	})

	t.Run("a mutant cancelled before anything started", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		f := &fake{}

		attempt := execute.RunOne(ctx, options(f, 1),
			execute.MutantRun{ID: "abc123", Timeout: mutantTimeout}, testBins("example.com/a"))

		if attempt.Outcome != mutation.OutcomeNotRun {
			t.Fatalf("outcome = %s, want %s", attempt.Outcome, mutation.OutcomeNotRun)
		}
		if len(f.seen()) != 0 {
			t.Fatalf("the fake was asked to start %d processes, want none", len(f.seen()))
		}
		if attempt.Err != nil {
			t.Errorf("err = %v, want none: nothing had been started, so there is nothing to name", attempt.Err)
		}
	})
}

func TestRunOneLabelsEachBinaryStartAsMutantRunWithTheMutantAsSubject(t *testing.T) {
	t.Parallel()

	const id = "5f2b8c1d4e6a7b9c"
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if c.program() == "example.com/b.test" {
			return failed("--- FAIL: TestB\n")
		}
		return passed()
	}}
	opts, sink := traced(t, f, options(f, 1))

	attempt := execute.RunOne(t.Context(), opts,
		execute.MutantRun{ID: id, Timeout: mutantTimeout}, testBins("example.com/a", "example.com/b"))

	if attempt.Outcome != mutation.OutcomeKilled {
		t.Fatalf("outcome = %s, want %s", attempt.Outcome, mutation.OutcomeKilled)
	}
	for i, c := range f.seen() {
		if c.Kind != trace.ExecKindMutantRun {
			t.Errorf("call %d was labelled %q, want %q", i, c.Kind, trace.ExecKindMutantRun)
		}
		if c.Subject != id {
			t.Errorf("call %d was about %q, want the mutant %q", i, c.Subject, id)
		}
	}
	events := eventsOf(sink, trace.TypeExec)
	if len(events) != 2 {
		t.Fatalf("the recording holds %d exec events, want one per binary started", len(events))
	}
	for i, event := range events {
		if event.Exec.Kind != trace.ExecKindMutantRun || event.Exec.Subject != id {
			t.Errorf("exec %d = {kind: %q, subject: %q}, want {%q, %q}",
				i, event.Exec.Kind, event.Exec.Subject, trace.ExecKindMutantRun, id)
		}
	}
}

func TestRunOneReportsTheBinariesItTriedInOrder(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if c.program() == "example.com/b.test" {
			return failed("--- FAIL: TestB\n")
		}
		return passed()
	}}
	opts, sink := traced(t, f, options(f, 1))

	attempt := execute.RunOne(t.Context(), opts,
		execute.MutantRun{ID: "abc123", Timeout: mutantTimeout},
		testBins("example.com/a", "example.com/b", "example.com/c"))

	want := []string{"example.com/a", "example.com/b"}
	if !slices.Equal(attempt.Binaries, want) {
		t.Errorf("Binaries = %q, want %q — the binary after the kill was never started", attempt.Binaries, want)
	}
	if got := execSeqs(sink); !slices.Equal(attempt.ExecSeqs, got) {
		t.Errorf("ExecSeqs = %v, want the recording's own exec sequences %v", attempt.ExecSeqs, got)
	}
	if len(attempt.ExecSeqs) != len(want) {
		t.Errorf("ExecSeqs = %v, want one sequence per binary tried", attempt.ExecSeqs)
	}
}

func TestRunOneReportsNoSequencesWithoutARecorder(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}

	attempt := execute.RunOne(t.Context(), options(f, 1),
		execute.MutantRun{ID: "abc123", Timeout: mutantTimeout}, testBins("example.com/a", "example.com/b"))

	if want := []string{"example.com/a", "example.com/b"}; !slices.Equal(attempt.Binaries, want) {
		t.Errorf("Binaries = %q, want %q whether or not the run is traced", attempt.Binaries, want)
	}
	if len(attempt.ExecSeqs) != 0 {
		t.Errorf("ExecSeqs = %v, want none: nothing was recorded", attempt.ExecSeqs)
	}
}

func TestAPassStopsAtTheFirstFailureOnlyWhenOneFailureIsTheWholeAnswer(t *testing.T) {
	t.Parallel()

	const flag = "-test.failfast"
	bins := testBins("example.com/a")

	t.Run("a mutant run", func(t *testing.T) {
		t.Parallel()

		f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
		opts := execute.WithRunner(execute.Options{ScratchDir: t.TempDir()}, f.run)
		execute.RunOne(t.Context(), opts,
			execute.MutantRun{ID: "deadbeef", Timeout: mutantTimeout}, bins)
		assertFlag(t, f, flag, true)
	})

	t.Run("a control run", func(t *testing.T) {
		t.Parallel()

		f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
		opts := execute.WithRunner(execute.Options{ScratchDir: t.TempDir()}, f.run)
		execute.RunControl(t.Context(), opts, execute.ControlRun{Timeout: mutantTimeout}, bins)
		assertFlag(t, f, flag, true)
	})

	t.Run("a probe pass", func(t *testing.T) {
		t.Parallel()

		f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
		opts := execute.WithRunner(execute.Options{ScratchDir: t.TempDir()}, f.run)
		execute.RunProbe(t.Context(), opts,
			probeRun(filepath.Join(t.TempDir(), "infections.log")), bins)
		assertFlag(t, f, flag, false)
	})
}

func assertFlag(t *testing.T, f *fake, flag string, want bool) {
	t.Helper()
	seen := f.seen()
	if len(seen) != 1 {
		t.Fatalf("started %d processes, want 1", len(seen))
	}
	if got := slices.Contains(seen[0].Argv, flag); got != want {
		t.Errorf("argv %q carries %s = %v, want %v", seen[0].Argv, flag, got, want)
	}
}
