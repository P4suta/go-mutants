// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/trace"
)

// probeRun builds one probe pass over a log path that does not exist, which is
// the ordinary shape here: the fake runner starts no process, so nothing writes
// a log, and a missing log is the empty set of infections.
func probeRun(logPath string) execute.ProbeRun {
	return execute.ProbeRun{
		Timeout: mutantTimeout,
		LogPath: logPath,
		Digest:  strings.Repeat("a", 64),
		Mutants: 3,
	}
}

// TestRunProbeMapsExitCodesToOutcomes is the classification the whole layer
// rests on: exactly one exit status is a measurement, and every other end of a
// probe process is an outcome that carries no infection facts at all.
//
// Nothing here may be read as "nothing was infected". A failing suite, a
// supervisor's kill, a runtime that could not open its log and a process that
// never started are four different things and none of them is evidence that a
// site was never reached — which is what an empty set of indices would say, and
// what would license skipping the very executions that find the kills.
func TestRunProbeMapsExitCodesToOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		result  func() runner.Result
		outcome execute.ProbeOutcome
		code    execute.Code
		facts   bool
	}{
		{
			name:    "a suite that passed is the one measurement",
			result:  passed,
			outcome: execute.ProbeMeasured,
			facts:   true,
		},
		{
			name:    "a suite that failed proves nothing",
			result:  func() runner.Result { return failed("--- FAIL: TestA\n") },
			outcome: execute.ProbeTestFailed,
		},
		{
			name:    "a target the supervisor killed proves nothing",
			result:  timedOut,
			outcome: execute.ProbeTimedOut,
		},
		{
			name:    "a runtime that could not record proves nothing",
			result:  probeUnavailable,
			outcome: execute.ProbeUnavailable,
		},
		{
			name:   "a process that would not start is infrastructure trouble",
			result: unstartable,
			code:   execute.CodeProbeStart,
		},
		{
			name:   "a child with no exit status is a cancelled pass",
			result: cancelled,
			code:   execute.CodeInterrupted,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{respond: func(context.Context, call) runner.Result { return c.result() }}
			attempt := execute.RunProbe(t.Context(), options(f, 1),
				probeRun(filepath.Join(t.TempDir(), "infection.log")), testBins("example.com/a"))

			if attempt.Outcome != c.outcome {
				t.Errorf("outcome = %q, want %q (%v)", attempt.Outcome, c.outcome, attempt.Err)
			}
			if got := execute.CodeOf(attempt.Err); got != c.code {
				t.Errorf("code = %q, want %q (%v)", got, c.code, attempt.Err)
			}
			if c.code == "" && len(attempt.Output) == 0 {
				t.Error("probe output is empty")
			}
			switch {
			case c.facts && attempt.Infected == nil:
				t.Error("a measured pass carries nil rather than a set of indices")
			case !c.facts && attempt.Infected != nil:
				t.Errorf("infected = %v, want nil: this outcome carries no facts", attempt.Infected)
			}
		})
	}
}

// TestRunProbeStopsAtTheFirstBinaryThatProvesNothing pins the short-circuit,
// which is a soundness rule rather than a saving: the indices the remaining
// binaries would append cannot be combined with a pass that already failed, so
// running them would only produce a subset that looks like a complete answer.
func TestRunProbeStopsAtTheFirstBinaryThatProvesNothing(t *testing.T) {
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if c.program() == "example.com/b.test" {
			return failed("--- FAIL: TestB\n")
		}
		return passed()
	}}

	attempt := execute.RunProbe(t.Context(), options(f, 1),
		probeRun(filepath.Join(t.TempDir(), "infection.log")),
		testBins("example.com/a", "example.com/b", "example.com/c"))

	if attempt.Outcome != execute.ProbeTestFailed {
		t.Errorf("outcome = %q, want %q", attempt.Outcome, execute.ProbeTestFailed)
	}
	if attempt.Infected != nil {
		t.Errorf("infected = %v, want nil", attempt.Infected)
	}
	if want := 2; len(f.seen()) != want {
		t.Errorf("started %d binaries, want %d: the binary after the failure must not run", len(f.seen()), want)
	}
}

// TestRunProbeTreatsAMissingLogAsEmpty is the one absence that is a fact.
//
// The generated runtime writes its header in init, before any test code runs,
// so a binary that exited zero having written no log is a binary that never
// linked a probe — and one that never linked a probe cannot have run a probed
// site. The set has to be non-nil, because nil is what every outcome that
// proves nothing carries and the two must not be confused.
func TestRunProbeTreatsAMissingLogAsEmpty(t *testing.T) {
	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}

	attempt := execute.RunProbe(t.Context(), options(f, 1),
		probeRun(filepath.Join(t.TempDir(), "never-written.log")), testBins("example.com/a"))

	if attempt.Outcome != execute.ProbeMeasured {
		t.Fatalf("outcome = %q, want %q (%v)", attempt.Outcome, execute.ProbeMeasured, attempt.Err)
	}
	if attempt.Infected == nil {
		t.Fatal("infected is nil, want the empty set: a missing log is a binary that linked no probe")
	}
	if len(attempt.Infected) != 0 {
		t.Errorf("infected = %v, want empty", attempt.Infected)
	}
}

// TestRunProbeReportsAnUnreadableLog is the other side of that door. A log that
// exists and cannot be read against this catalogue is a measurement that
// happened and cannot be interpreted, and reading it as the empty set would
// turn a damaged file into a licence to skip every execution.
func TestRunProbeReportsAnUnreadableLog(t *testing.T) {
	cases := []struct {
		name  string
		write func(t *testing.T, path string)
	}{
		{
			name: "a log this catalogue's runtime did not write",
			write: func(t *testing.T, path string) {
				testkit.WriteFile(t, path, []byte("gomutants-infection-v1 other 3\n"))
			},
		},
		{
			name: "a log whose last line was never finished",
			write: func(t *testing.T, path string) {
				testkit.WriteFile(t, path, []byte("gomutants-infection-v1 "+strings.Repeat("a", 64)+" 3\n1"))
			},
		},
		{
			name: "a log that is not a file at all",
			write: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatalf("creating %s: %v", path, err)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "infection.log")
			c.write(t, log)

			f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
			attempt := execute.RunProbe(t.Context(), options(f, 1), probeRun(log), testBins("example.com/a"))

			if got := execute.CodeOf(attempt.Err); got != execute.CodeProbeLog {
				t.Errorf("code = %q, want %q (%v)", got, execute.CodeProbeLog, attempt.Err)
			}
			if attempt.Infected != nil {
				t.Errorf("infected = %v, want nil: an unreadable log yields no facts", attempt.Infected)
			}
		})
	}
}

// TestRunProbeReadsTheLogItsBinariesWrote is the whole point of the pass: the
// indices come back sorted, distinct, and bounded by the catalogue they were
// minted against.
func TestRunProbeReadsTheLogItsBinariesWrote(t *testing.T) {
	digest := strings.Repeat("b", 64)
	log := filepath.Join(t.TempDir(), "infection.log")
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		path := envValue(c.Env, instrument.ProbeEnv)
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return failed(err.Error())
		}
		defer func() { _ = file.Close() }()
		body := "gomutants-infection-v1 " + digest + " 4\n"
		if c.program() == "example.com/a.test" {
			body += "2\n0\n2\n"
		} else {
			body += "3\n"
		}
		if _, err := file.WriteString(body); err != nil {
			return failed(err.Error())
		}
		return passed()
	}}

	run := probeRun(log)
	run.Digest = digest
	run.Mutants = 4
	attempt := execute.RunProbe(t.Context(), options(f, 1), run,
		testBins("example.com/a", "example.com/b"))

	if attempt.Outcome != execute.ProbeMeasured {
		t.Fatalf("outcome = %q, want %q (%v)", attempt.Outcome, execute.ProbeMeasured, attempt.Err)
	}
	want := []uint32{0, 2, 3}
	if len(attempt.Infected) != len(want) {
		t.Fatalf("infected = %v, want %v", attempt.Infected, want)
	}
	for i, index := range want {
		if attempt.Infected[i] != index {
			t.Fatalf("infected = %v, want %v", attempt.Infected, want)
		}
	}
}

// TestRunProbeRefusesAPassItCannotMeasure covers the fail-closed refusals. Each
// of them would otherwise end as an empty set of indices, which is the one
// answer that must never be produced by having measured nothing.
func TestRunProbeRefusesAPassItCannotMeasure(t *testing.T) {
	log := filepath.Join(t.TempDir(), "infection.log")
	cases := []struct {
		name string
		run  execute.ProbeRun
		bins []execute.TestBinary
	}{
		{
			name: "no timeout",
			run:  execute.ProbeRun{LogPath: log},
			bins: testBins("example.com/a"),
		},
		{
			name: "no log to record into",
			run:  execute.ProbeRun{Timeout: mutantTimeout},
			bins: testBins("example.com/a"),
		},
		{
			name: "no test binaries",
			run:  probeRun(log),
			bins: nil,
		},
		{
			name: "a target overriding the harness timeout",
			run: execute.ProbeRun{
				Timeout: mutantTimeout,
				LogPath: log,
				Args:    []string{"-test.timeout=0"},
			},
			bins: testBins("example.com/a"),
		},
		{
			name: "an empty subset of the binaries",
			run: execute.ProbeRun{
				Timeout:  mutantTimeout,
				LogPath:  log,
				Binaries: []int{},
			},
			bins: testBins("example.com/a"),
		},
		{
			name: "a binary index the run does not have",
			run: execute.ProbeRun{
				Timeout:  mutantTimeout,
				LogPath:  log,
				Binaries: []int{4},
			},
			bins: testBins("example.com/a"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{}
			attempt := execute.RunProbe(t.Context(), options(f, 1), c.run, c.bins)

			if got := execute.CodeOf(attempt.Err); got != execute.CodeProbeInvalid {
				t.Errorf("code = %q, want %q (%v)", got, execute.CodeProbeInvalid, attempt.Err)
			}
			if attempt.Infected != nil {
				t.Errorf("infected = %v, want nil", attempt.Infected)
			}
			if got := len(f.seen()); got != 0 {
				t.Errorf("started %d processes, want none", got)
			}
		})
	}
}

// TestProbeEnvSetsTheLogAndNoActiveMutant pins the two halves of a probe
// process's environment.
//
// The log variable is what turns the linked-in runtime from a nil check into a
// recorder. The activation variable's *absence* is the other half and is the
// one that matters: a probe tree has no activation runtime, so a mutant name
// there could only come from a developer's shell, and the whole claim of the
// pass is that what ran is the program the user wrote.
func TestProbeEnvSetsTheLogAndNoActiveMutant(t *testing.T) {
	scratch := t.TempDir()
	log := filepath.Join(scratch, "infection.log")

	t.Setenv(instrument.ActiveEnv, "an-identity-from-the-users-shell")
	t.Setenv("GO_MUTANTS_SOMETHING_ELSE", "also-scrubbed")
	t.Setenv("TMP", t.TempDir())
	t.Setenv("TEMP", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())

	env := execute.ProbeEnv(scratch, log)

	if got := envValue(env, instrument.ProbeEnv); got != log {
		t.Errorf("%s = %q, want %q", instrument.ProbeEnv, got, log)
	}
	if got := envValue(env, instrument.ActiveEnv); got != "" {
		t.Errorf("%s = %q, want it absent: a probe tree activates nothing", instrument.ActiveEnv, got)
	}
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GO_MUTANTS_") && key != instrument.ProbeEnv {
			t.Errorf("the child inherited %q; every GO_MUTANTS_ variable but the log must be scrubbed", entry)
		}
	}
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		if got := envValue(env, key); got != scratch {
			t.Errorf("%s = %q, want the worker's scratch directory %q", key, got, scratch)
		}
	}
}

// TestRunProbeComposesTheChildInvocation pins that a probe process is started
// exactly as a mutant's is — same working directory, same paired timeouts —
// because the point of the pass is that the same tests run the same way.
func TestRunProbeComposesTheChildInvocation(t *testing.T) {
	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	run := probeRun(filepath.Join(t.TempDir(), "infection.log"))
	run.Args = []string{"-test.run=^TestRoundTrip$"}

	execute.RunProbe(t.Context(), options(f, 1), run, testBins("example.com/a"))

	seen := f.seen()
	if len(seen) != 1 {
		t.Fatalf("started %d processes, want 1", len(seen))
	}
	want := []string{"example.com/a.test", "-test.timeout=14s", "-test.run=^TestRoundTrip$"}
	if len(seen[0].Argv) != len(want) {
		t.Fatalf("argv = %q, want %q", seen[0].Argv, want)
	}
	for i, argument := range want {
		if seen[0].Argv[i] != argument {
			t.Fatalf("argv = %q, want %q", seen[0].Argv, want)
		}
	}
	if seen[0].Timeout != mutantTimeout {
		t.Errorf("supervisor timeout = %s, want %s", seen[0].Timeout, mutantTimeout)
	}
	if want := "/snapshot/example.com/a"; seen[0].Dir != want {
		t.Errorf("working directory = %q, want %q", seen[0].Dir, want)
	}
}

// TestRunProbePassesTheOutputLimit is [RunOne]'s output budget applied to the
// probe pass, and it is the same claim for the same reason: a pass whose
// binaries print in a loop must not take a run's memory with it, and a caller
// that raised or lowered the cap must be the one deciding that.
//
// The pass also reports what it could not keep. A probe's capture is what a
// reader of a `test-failed` or a `timed-out` pass has to work from, and one
// silently missing a megabyte reads exactly like a suite that said little.
func TestRunProbePassesTheOutputLimit(t *testing.T) {
	deciding := runner.Result{
		ExitCode:    1,
		Duration:    time.Millisecond,
		Output:      []byte(runner.OutputTruncatedPrefix + ": …\n--- FAIL: TestA\n"),
		OutputBytes: 1 << 20,
		Truncated:   true,
	}
	f := &fake{respond: func(context.Context, call) runner.Result { return deciding }}
	run := probeRun(filepath.Join(t.TempDir(), "infection.log"))
	run.OutputLimit = 777

	attempt := execute.RunProbe(t.Context(), options(f, 1), run, testBins("example.com/a"))

	seen := f.seen()
	if len(seen) != 1 {
		t.Fatalf("started %d processes, want 1", len(seen))
	}
	if seen[0].OutputLimit != 777 {
		t.Errorf("spec OutputLimit = %d, want the pass's 777", seen[0].OutputLimit)
	}
	if attempt.Outcome != execute.ProbeTestFailed {
		t.Fatalf("outcome = %s, want %s (%v)", attempt.Outcome, execute.ProbeTestFailed, attempt.Err)
	}
	if !attempt.Truncated {
		t.Error("Truncated = false although the deciding capture lost bytes")
	}
	if attempt.OutputBytes != deciding.OutputBytes {
		t.Errorf("OutputBytes = %d, want the deciding binary's %d", attempt.OutputBytes, deciding.OutputBytes)
	}
}

// TestRunProbeAndRunOneShareTheProcessCore keeps the two passes measuring the
// same program: a mutant's binary and a probe's are started with the same
// argument vector but for the activation flag, so a change to one that did not
// reach the other would silently make the probe a measurement of something
// else.
func TestRunProbeAndRunOneShareTheProcessCore(t *testing.T) {
	args := []string{"-test.run=^TestRoundTrip$", "-test.count=1"}

	mutantFake := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	execute.RunOne(t.Context(), options(mutantFake, 1), execute.MutantRun{
		ID:      "abc123",
		Timeout: mutantTimeout,
		Args:    args,
	}, testBins("example.com/a"))

	probeFake := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	run := probeRun(filepath.Join(t.TempDir(), "infection.log"))
	run.Args = args
	execute.RunProbe(t.Context(), options(probeFake, 1), run, testBins("example.com/a"))

	mutantCalls, probeCalls := mutantFake.seen(), probeFake.seen()
	if len(mutantCalls) != 1 || len(probeCalls) != 1 {
		t.Fatalf("started %d mutant and %d probe processes, want one each", len(mutantCalls), len(probeCalls))
	}
	if strings.Join(mutantCalls[0].Argv, " ") != strings.Join(probeCalls[0].Argv, " ") {
		t.Errorf("mutant argv %q and probe argv %q differ", mutantCalls[0].Argv, probeCalls[0].Argv)
	}
	if mutantCalls[0].Dir != probeCalls[0].Dir || mutantCalls[0].Timeout != probeCalls[0].Timeout {
		t.Errorf("mutant ran in %q for %s and the probe in %q for %s",
			mutantCalls[0].Dir, mutantCalls[0].Timeout, probeCalls[0].Dir, probeCalls[0].Timeout)
	}
	if mutantCalls[0].active() == "" {
		t.Error("the mutant process carried no activation identity, so the two argument vectors agreeing means nothing")
	}
	if probeCalls[0].active() != "" {
		t.Errorf("the probe process carried the activation identity %q", probeCalls[0].active())
	}
}

// TestRunProbeNamesTheBinaryACancellationCutOff is [RunOne]'s rule applied to
// the probe pass, and it is the same argument: a pass that was cut off names
// the binary it was in, because that is what a reader of a Ctrl-C is looking
// for, and one that was cancelled before it started anything names nothing
// rather than the command it was about to run.
func TestRunProbeNamesTheBinaryACancellationCutOff(t *testing.T) {
	t.Run("a child the cancellation killed", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		f := &fake{respond: func(context.Context, call) runner.Result {
			cancel()
			return cancelled()
		}}
		bins := testBins("example.com/a")

		attempt := execute.RunProbe(ctx, options(f, 1),
			probeRun(filepath.Join(t.TempDir(), "infection.log")), bins)

		if got := execute.CodeOf(attempt.Err); got != execute.CodeInterrupted {
			t.Fatalf("code = %q, want %q (%v)", got, execute.CodeInterrupted, attempt.Err)
		}
		var failure *execute.Error
		if !errors.As(attempt.Err, &failure) {
			t.Fatalf("err = %v, want an *execute.Error", attempt.Err)
		}
		command := failure.Command()
		if command == nil {
			t.Fatal("Command() = nil, want the probe binary that was cut off")
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
	})

	t.Run("a pass cancelled before anything started", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		f := &fake{}

		attempt := execute.RunProbe(ctx, options(f, 1),
			probeRun(filepath.Join(t.TempDir(), "infection.log")), testBins("example.com/a"))

		if got := execute.CodeOf(attempt.Err); got != execute.CodeInterrupted {
			t.Fatalf("code = %q, want %q (%v)", got, execute.CodeInterrupted, attempt.Err)
		}
		if len(f.seen()) != 0 {
			t.Fatalf("the fake was asked to start %d processes, want none", len(f.seen()))
		}
		var failure *execute.Error
		if !errors.As(attempt.Err, &failure) {
			t.Fatalf("err = %v, want an *execute.Error", attempt.Err)
		}
		if command := failure.Command(); command != nil {
			t.Errorf("Command() = %+v, want nil: nothing had been started", command)
		}
		if failure.Package != "" {
			t.Errorf("Package = %q, want empty: no binary had been started to be about", failure.Package)
		}
	})
}

// TestRunProbeLabelsEachBinaryStartAsProbeRun keeps the two passes over one
// tree apart in the account of a run.
//
// A probe process and a mutant process are the same binary started the same way
// — that is the whole point of them sharing [startTarget] — so in a recording
// they are told apart by their label and by nothing else. The subject names the
// package the pass is *about*, which is a fact about the pass rather than about
// each child: a pass narrowed to one binary is a measurement of that package,
// and a pass over several is a measurement of no single one, so it names none.
func TestRunProbeLabelsEachBinaryStartAsProbeRun(t *testing.T) {
	t.Parallel()

	t.Run("a pass over several binaries names no package", func(t *testing.T) {
		t.Parallel()

		f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
		opts, sink := traced(t, f, options(f, 1))

		attempt := execute.RunProbe(t.Context(), opts, probeRun(filepath.Join(t.TempDir(), "infection.log")),
			testBins("example.com/a", "example.com/b"))

		if attempt.Err != nil {
			t.Fatalf("RunProbe: %v", attempt.Err)
		}
		events := eventsOf(sink, trace.TypeExec)
		if len(events) != 2 {
			t.Fatalf("the recording holds %d exec events, want one per binary", len(events))
		}
		for i, event := range events {
			if event.Exec.Kind != trace.ExecKindProbeRun {
				t.Errorf("exec %d is labelled %q, want %q", i, event.Exec.Kind, trace.ExecKindProbeRun)
			}
			if event.Exec.Subject != "" {
				t.Errorf("exec %d is about %q, want no package: the pass ran several", i, event.Exec.Subject)
			}
		}
		if want := []string{"example.com/a", "example.com/b"}; !slices.Equal(attempt.Binaries, want) {
			t.Errorf("Binaries = %q, want %q", attempt.Binaries, want)
		}
		if got := execSeqs(sink); !slices.Equal(attempt.ExecSeqs, got) {
			t.Errorf("ExecSeqs = %v, want the recording's own %v", attempt.ExecSeqs, got)
		}
	})

	t.Run("a pass narrowed to one binary names its package", func(t *testing.T) {
		t.Parallel()

		f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
		opts, sink := traced(t, f, options(f, 1))
		run := probeRun(filepath.Join(t.TempDir(), "infection.log"))
		run.Binaries = []int{1}

		attempt := execute.RunProbe(t.Context(), opts, run, testBins("example.com/a", "example.com/b"))

		if attempt.Err != nil {
			t.Fatalf("RunProbe: %v", attempt.Err)
		}
		events := eventsOf(sink, trace.TypeExec)
		if len(events) != 1 {
			t.Fatalf("the recording holds %d exec events, want the one binary the pass selected", len(events))
		}
		if want := "example.com/b"; events[0].Exec.Subject != want {
			t.Errorf("the exec is about %q, want %q", events[0].Exec.Subject, want)
		}
		if want := []string{"example.com/b"}; !slices.Equal(attempt.Binaries, want) {
			t.Errorf("Binaries = %q, want %q", attempt.Binaries, want)
		}
	})
}

// TestRunProbeNamesTheBinariesItStartedWhenAPassCannotBeMade keeps the account
// of a pass that failed as complete as the account of one that worked.
//
// A pass whose second binary could not be started is exactly the pass somebody
// has to diagnose, and "which binaries had already run" is the first thing they
// need: the first binary ran a whole test suite, and a report that named none
// of it would describe a pass that started nothing. The sequences go with them,
// because the output of the binary that did run is in the recording and this is
// what points at it.
func TestRunProbeNamesTheBinariesItStartedWhenAPassCannotBeMade(t *testing.T) {
	t.Parallel()

	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if c.program() == "example.com/b.test" {
			return unstartable()
		}
		return passed()
	}}
	opts, sink := traced(t, f, options(f, 1))

	attempt := execute.RunProbe(t.Context(), opts, probeRun(filepath.Join(t.TempDir(), "infection.log")),
		testBins("example.com/a", "example.com/b"))

	if execute.CodeOf(attempt.Err) != execute.CodeProbeStart {
		t.Fatalf("RunProbe failed with %q, want %q: %v",
			execute.CodeOf(attempt.Err), execute.CodeProbeStart, attempt.Err)
	}
	if attempt.Infected != nil {
		t.Errorf("Infected = %v, want nil: a pass that could not be made carries no facts", attempt.Infected)
	}
	want := []string{"example.com/a", "example.com/b"}
	if !slices.Equal(attempt.Binaries, want) {
		t.Errorf("Binaries = %q, want %q — both were started, and one of them would not run", attempt.Binaries, want)
	}
	// The failure names the binary that would not start, and neither the first
	// one tried nor the pass's subject: a caller reporting the package has to be
	// pointed at the one that broke.
	var failure *execute.Error
	if !errors.As(attempt.Err, &failure) {
		t.Fatalf("err = %v, want an *execute.Error", attempt.Err)
	}
	if failure.Package != "example.com/b" {
		t.Errorf("Package = %q, want example.com/b, the binary that would not start", failure.Package)
	}
	// One sequence: the binary that ran. The one that never became a process is
	// recorded too, by internal/runner, and this fake records what it is given.
	if got := execSeqs(sink); !slices.Equal(attempt.ExecSeqs, got) {
		t.Errorf("ExecSeqs = %v, want the recording's own %v", attempt.ExecSeqs, got)
	}
	if len(attempt.ExecSeqs) == 0 {
		t.Error("ExecSeqs is empty, want the executions the pass made before it stopped")
	}
}
