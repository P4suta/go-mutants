// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
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
			name:  "a log this catalogue's runtime did not write",
			write: func(t *testing.T, path string) { writeFile(t, path, "gomutants-infection-v1 other 3\n") },
		},
		{
			name: "a log whose last line was never finished",
			write: func(t *testing.T, path string) {
				writeFile(t, path, "gomutants-infection-v1 "+strings.Repeat("a", 64)+" 3\n1")
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
