// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
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
)

// mutantTimeout is the per-mutant budget every test here uses. It is never
// waited on — the fake runner answers immediately — so its only job is to be a
// value the assertions can recognise.
const mutantTimeout = 7 * time.Second

// TestRunOneStopsAtTheFirstFailingBinary pins the short-circuit: the binaries
// are tried in order, the first failure ends the attempt, and the binaries
// after it are never started.
//
// The order and the stopping are one claim. A run that tried every binary
// anyway would still report the right outcome, and would spend the whole
// mutation run's time budget doing it.
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
	// Two children at a millisecond each. The attempt's duration is the time its
	// children took, not the time the loop around them took.
	if want := 2 * time.Millisecond; attempt.Duration != want {
		t.Errorf("duration = %s, want %s", attempt.Duration, want)
	}
}

// TestRunOneSurvivesWhenEveryBinaryPasses is the other half: a mutant that gets
// through every binary survived, and every binary really was run.
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

// TestRunOnePassesTargetArgumentsAndUsesTheSuppliedEnvironment is the bridge
// contract: one compiled binary can be reused for a named TestX or FuzzX, and a
// long-lived caller can freeze the environment instead of consulting global
// process state at every execution.
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

// TestRunOneReportsAStaleCatalogRatherThanAKill pins the exit status that must
// never be mistaken for a detection.
//
// Exit 97 is the generated runtime refusing an identity it has never heard of.
// It is non-zero, so the cheap reading is "the tests failed" — and that reading
// would let a catalogue that no longer matches the instrumented tree report a
// perfect score.
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

// TestRunOneReportsATimeoutWithoutCallingItDetection pins that one timeout is
// an observation and not a verdict: the attempt says timed out, and deciding
// what that means is [execute.Schedule]'s job.
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

// TestRunOneReportsAProcessThatWouldNotStart proves a start failure is
// infrastructure trouble rather than a fact about the tests, and that the
// runner's own GOM72xx code stays reachable underneath this package's.
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

// TestRunOneReportsACancelledChildAsNotRun pins the one case that is easy to
// get wrong: a child killed by a cancelled context comes back with no exit
// status, no timeout and no error, which is exactly the shape of a failure
// unless the context is asked.
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

// TestRunOneKeepsAResultThatLandedBeforeTheCancellation is the counterpart. The
// case order is Err, TimedOut, context, exit status precisely so that a child
// which really did fail is still reported as a kill even though the run is
// shutting down.
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

// TestRunOneRefusesAMutantItCannotMeasure covers the three fail-closed
// refusals, each of which would otherwise become a wrong number in a report.
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
			// Reporting this as survived is the flattering green the whole tool
			// exists to refuse: nothing ran, so nothing was measured.
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

// TestRunOneComposesTheChildInvocation pins everything about how a test binary
// is started: the argument vector, the working directory, and the environment.
//
// Each of these is load-bearing somewhere else. The working directory is what
// makes a test's testdata paths resolve; the activation variable is the entire
// dispatch mechanism; the in-process deadline is the insurance underneath the
// supervisor; and the scrubbed environment is what stops a developer's exported
// GO_MUTANTS_ACTIVE from quietly activating a second mutant.
func TestRunOneComposesTheChildInvocation(t *testing.T) {
	// Both directories are claimed before anything is exported, and both are
	// real. t.TempDir creates under os.TempDir, which reads these very
	// variables on POSIX, so a test that exports a made-up path and then asks
	// for a temporary directory is asking the testing package to stat a
	// directory that was never there — which is what this test used to do, and
	// why it failed on Linux and macOS while passing on Windows, where
	// os.TempDir reads TMP and TEMP instead. The inherited value has to be a
	// directory rather than a name for the same reason: it is what the child
	// would have got, and a child that used it would have to be able to.
	scratch := t.TempDir()
	inherited := t.TempDir()

	t.Setenv("GO_MUTANTS_ACTIVE", "an-identity-from-the-users-shell")
	t.Setenv("GO_MUTANTS_SOMETHING_ELSE", "also-scrubbed")
	// All three spellings, so the assertion below means the same thing on every
	// platform rather than depending on which one this one reads.
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

	want := []string{"example.com/a.test", "-test.timeout=14s"}
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

// TestRunOneCreatesTheWorkerScratchDirectory proves the temporary directory the
// environment points at actually exists by the time a child could use it.
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

// TestRunOneResolvesARelativeScratchDirectory pins the other half of the
// resolution rule, and it is the more dangerous half.
//
// The scratch directory is handed to the child as TMP, TEMP and TMPDIR, and the
// child runs with its working directory set to a package directory *inside the
// snapshot*. A relative scratch directory therefore used to mean two different
// places at once: go-mutants created it beside its own working directory, while
// a test that wrote to TMPDIR wrote it into the tree every later mutant is
// measured against — the exact drift the scratch directory exists to prevent,
// arriving through the option meant to prevent it.
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

// TestRunOneLeavesTheInheritedTempAloneWithoutAScratchDir documents the
// standalone case: no scratch parent means no redirection, which is right for a
// single call and is why [execute.Schedule] always supplies one.
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
