// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/execute"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/trace"
)

// controlRun is one control run with the budget every test here uses. It is
// never waited on — the fake runner answers immediately — so the timeout's only
// job is to be a value an assertion can recognise.
func controlRun(args ...string) execute.ControlRun {
	return execute.ControlRun{Timeout: mutantTimeout, Args: args}
}

// TestControlEnvCarriesNoActivationOrProbeVariable is the whole claim the
// control run rests on, stated about the environment it composes.
//
// The mutant tree's binaries are the user's program plus a switch, and the
// switch is one environment variable. So a control is only "the original
// program" if that variable is absent — and absent rather than empty, because
// the generated runtime reads it with os.Getenv and an empty value is not an
// identity it knows. The probe variable is here for the same reason in the
// other direction: a control that happened to inherit one would record
// infections into somebody else's log while claiming to be a plain run.
//
// The environment is compared against [execute.BaseEnv] entry for entry rather
// than only searched for the two names, because that is the actual contract: a
// control is the scrubbed base environment and nothing added to it, and a
// variable invented here later would be one more difference between the program
// the control ran and the program the mutant run ran.
func TestControlEnvCarriesNoActivationOrProbeVariable(t *testing.T) {
	t.Setenv(instrument.ActiveEnv, "from-the-users-shell")
	t.Setenv(instrument.ProbeEnv, "/tmp/somebody-elses-infection.log")
	t.Setenv("GOFLAGS", "-mod=readonly")

	env := execute.ControlEnv("/run/scratch/w0")

	if got := envValue(env, instrument.ActiveEnv); got != "" {
		t.Errorf("%s = %q, want it absent: a control that activates a mutant is not the original program",
			instrument.ActiveEnv, got)
	}
	if got := envValue(env, instrument.ProbeEnv); got != "" {
		t.Errorf("%s = %q, want it absent", instrument.ProbeEnv, got)
	}
	if got := envValue(env, "GOFLAGS"); got != "-mod=readonly" {
		t.Errorf("GOFLAGS = %q, want it inherited: a control that stripped the project's own"+
			" settings would be measuring a different project", got)
	}
	if want := execute.BaseEnv("/run/scratch/w0"); !slices.Equal(env, want) {
		t.Errorf("the control environment is not the scrubbed base environment:\n got %q\nwant %q", env, want)
	}
}

// TestRunControlStopsAtTheFirstFailure pins the short circuit, and here it is a
// statement about meaning rather than about cost.
//
// A control run asks one question — does the original program pass these tests?
// — and the first binary that answers no has answered it. Running the rest
// could not change the answer and would spend the run's time budget saying so
// again, and the attempt has to name the binary that decided, because "the
// control failed" without a package is a fact nobody can act on.
func TestRunControlStopsAtTheFirstFailure(t *testing.T) {
	f := &fake{respond: func(_ context.Context, c call) runner.Result {
		if c.program() == "example.com/b.test" {
			return failed("--- FAIL: TestB\n    b_test.go:9: got 2, want 1\n")
		}
		return passed()
	}}
	bins := testBins("example.com/a", "example.com/b", "example.com/c")

	attempt := execute.RunControl(t.Context(), options(f, 1), controlRun(), bins)

	if attempt.Err != nil {
		t.Fatalf("err = %v, want a failing suite reported as a result", attempt.Err)
	}
	if attempt.ExitCode != 1 {
		t.Errorf("exit code = %d, want the failing binary's 1", attempt.ExitCode)
	}
	if want := "example.com/b"; attempt.Package != want {
		t.Errorf("package = %q, want the binary that decided %q", attempt.Package, want)
	}
	if want := []string{"example.com/a.test", "example.com/b.test"}; !slices.Equal(f.programs(), want) {
		t.Errorf("started %q, want %q — the binary after the failure must not run", f.programs(), want)
	}
	if want := []string{"example.com/a", "example.com/b"}; !slices.Equal(attempt.Binaries, want) {
		t.Errorf("binaries = %q, want %q: naming the third would describe a run that never happened",
			attempt.Binaries, want)
	}
	if !strings.Contains(string(attempt.Output), "--- FAIL: TestB") {
		t.Errorf("output = %q, want the failing binary's own capture", attempt.Output)
	}
	if attempt.TimedOut {
		t.Error("TimedOut = true for a suite that exited on its own")
	}
}

// TestRunControlReportsTimeout is the other terminal answer, and the exit
// status is the half worth pinning.
//
// internal/runner reports no status at all for a tree it killed, and it says so
// with [runner.ExitCodeUnavailable] rather than by inventing one. That value is
// carried up unchanged, exactly as [CommandResult] carries it for a workspace
// command with the same field set: a zero there would read as *green* to a
// caller that forgot to look at TimedOut, and a status the child never returned
// is the one thing a status field must not claim.
func TestRunControlReportsTimeout(t *testing.T) {
	f := &fake{respond: func(context.Context, call) runner.Result { return timedOut() }}
	bins := testBins("example.com/a", "example.com/b")

	attempt := execute.RunControl(t.Context(), options(f, 1), controlRun(), bins)

	if attempt.Err != nil {
		t.Fatalf("err = %v, want a timeout reported as a result", attempt.Err)
	}
	if !attempt.TimedOut {
		t.Error("TimedOut = false although the supervisor killed the tree")
	}
	if attempt.ExitCode != runner.ExitCodeUnavailable {
		t.Errorf("exit code = %d, want %d: a killed tree has no status, and a zero there reads as"+
			" green to a caller that forgot TimedOut", attempt.ExitCode, runner.ExitCodeUnavailable)
	}
	if want := "example.com/a"; attempt.Package != want {
		t.Errorf("package = %q, want the binary that hung %q", attempt.Package, want)
	}
	if got := len(f.seen()); got != 1 {
		t.Errorf("started %d binaries, want 1: a control that timed out has its answer", got)
	}
}

// TestRunControlRefusesEmptyBinarySet refuses the two ways a control can be
// asked to run the original program in nothing at all.
//
// Both would come back as exit 0 having started no process, which is the same
// string of bytes as "the original program passes" — and a consumer comparing a
// mutant's failure against that control would read every mutant as killed by a
// suite that never ran.
func TestRunControlRefusesEmptyBinarySet(t *testing.T) {
	f := &fake{}

	for _, test := range []struct {
		name string
		run  execute.ControlRun
		bins []execute.TestBinary
	}{
		{
			name: "an empty subset of the binaries",
			run:  execute.ControlRun{Timeout: mutantTimeout, Binaries: []int{}},
			bins: testBins("example.com/a"),
		},
		{
			name: "no prepared binaries at all",
			run:  controlRun(),
			bins: nil,
		},
		{
			name: "a subset naming a binary this run does not have",
			run:  execute.ControlRun{Timeout: mutantTimeout, Binaries: []int{3}},
			bins: testBins("example.com/a"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempt := execute.RunControl(t.Context(), options(f, 1), test.run, test.bins)

			if got := execute.CodeOf(attempt.Err); got != execute.CodeControlInvalid {
				t.Fatalf("code = %q, want %q (%v)", got, execute.CodeControlInvalid, attempt.Err)
			}
			if attempt.ExitCode != 0 || attempt.Package != "" || attempt.Binaries != nil {
				t.Errorf("a refused control reports %+v, want no facts about a run it never made", attempt)
			}
		})
	}

	if started := f.seen(); len(started) != 0 {
		t.Errorf("a refused control started %d processes", len(started))
	}
}

// TestRunControlAndRunOneShareTheLaunchShape is the guarantee the whole feature
// is for: a control is the same binary, started the same way, minus the switch.
//
// A control is only evidence about a mutant run if the two ran the same
// program: same executable, same working directory — a Go test resolves
// testdata relative to where it runs — same paired timeouts, same arguments in
// the same order, and the same composed environment. So the two specs are
// compared field for field rather than on the two fields somebody remembered,
// and the *only* difference allowed is the activation variable. A change to one
// launch path that did not reach the other would silently turn the control into
// a measurement of something else, which is the failure this test exists to
// catch and the one nothing downstream could notice.
func TestRunControlAndRunOneShareTheLaunchShape(t *testing.T) {
	args := []string{"-test.run=^TestRoundTrip$", "-test.count=1"}
	base := execute.Options{Env: []string{"FROZEN=value", "PATH=/usr/bin"}}
	base.ScratchDir = t.TempDir()

	mutantFake := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	execute.RunOne(t.Context(), execute.WithRunner(base, mutantFake.run), execute.MutantRun{
		ID:      "abc123",
		Timeout: mutantTimeout,
		Args:    args,
	}, testBins("example.com/a"))

	controlFake := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	execute.RunControl(t.Context(), execute.WithRunner(base, controlFake.run),
		controlRun(args...), testBins("example.com/a"))

	mutantCalls, controlCalls := mutantFake.seen(), controlFake.seen()
	if len(mutantCalls) != 1 || len(controlCalls) != 1 {
		t.Fatalf("started %d mutant and %d control processes, want one each",
			len(mutantCalls), len(controlCalls))
	}
	mutant, control := mutantCalls[0], controlCalls[0]

	if !slices.Equal(mutant.Argv, control.Argv) {
		t.Errorf("mutant argv %q and control argv %q differ", mutant.Argv, control.Argv)
	}
	if mutant.Dir != control.Dir {
		t.Errorf("the mutant ran in %q and the control in %q", mutant.Dir, control.Dir)
	}
	if mutant.Timeout != control.Timeout {
		t.Errorf("the mutant was given %s and the control %s", mutant.Timeout, control.Timeout)
	}
	if mutant.active() == "" {
		t.Fatal("the mutant process carried no activation identity, so two agreeing" +
			" environments would mean nothing")
	}
	if control.active() != "" {
		t.Errorf("the control process carried the activation identity %q", control.active())
	}
	if want := withoutActivation(mutant.Env); !slices.Equal(control.Env, want) {
		t.Errorf("the control environment is not the mutant's minus the activation:\n got %q\nwant %q",
			control.Env, want)
	}
}

// withoutActivation is one child environment with the activation entry removed,
// which is the whole of what a control's may differ by.
func withoutActivation(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return key == instrument.ActiveEnv
	})
}

// TestRunControlInterruptedIsAnError keeps a Ctrl-C from being read as an
// answer.
//
// A cancelled child comes back from internal/runner with no exit status, and
// [runner.ExitCodeUnavailable] is very much non-zero — so reading it as an
// ordinary status would report the original program as *failing* whenever
// somebody stopped the run, which is the worst of the three possible wrong
// answers: a consumer comparing a mutant against that control would conclude
// the suite was already red and score nothing.
//
// The binary is named when there was one and left unnamed when there was not,
// which is [RunProbe]'s rule and is the same argument: a diagnostic that named
// a process that never existed sends a reader looking for it.
func TestRunControlInterruptedIsAnError(t *testing.T) {
	t.Run("a child the cancellation killed", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		f := &fake{respond: func(context.Context, call) runner.Result {
			cancel()
			return cancelled()
		}}
		bins := testBins("example.com/a")

		attempt := execute.RunControl(ctx, options(f, 1), controlRun(), bins)

		if got := execute.CodeOf(attempt.Err); got != execute.CodeInterrupted {
			t.Fatalf("code = %q, want %q (%v)", got, execute.CodeInterrupted, attempt.Err)
		}
		if !isCancellation(attempt.Err) {
			t.Errorf("err = %v, want a reachable context.Canceled", attempt.Err)
		}
		if attempt.ExitCode != 0 || attempt.TimedOut {
			t.Errorf("an interrupted control reports exit %d timeout=%v, want no verdict at all",
				attempt.ExitCode, attempt.TimedOut)
		}
		var failure *execute.Error
		if !errors.As(attempt.Err, &failure) {
			t.Fatalf("err = %v, want an *execute.Error", attempt.Err)
		}
		if failure.Package != bins[0].ImportPath {
			t.Errorf("Package = %q, want the binary that was cut off %q",
				failure.Package, bins[0].ImportPath)
		}
		if failure.Command() == nil {
			t.Error("Command() = nil, want the binary that was cut off")
		}
		if !slices.Equal(attempt.Binaries, []string{bins[0].ImportPath}) {
			t.Errorf("binaries = %q, want the one that had already started", attempt.Binaries)
		}
	})

	t.Run("a control cancelled before anything started", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		f := &fake{}

		attempt := execute.RunControl(ctx, options(f, 1), controlRun(), testBins("example.com/a"))

		if got := execute.CodeOf(attempt.Err); got != execute.CodeInterrupted {
			t.Fatalf("code = %q, want %q (%v)", got, execute.CodeInterrupted, attempt.Err)
		}
		var failure *execute.Error
		if !errors.As(attempt.Err, &failure) {
			t.Fatalf("err = %v, want an *execute.Error", attempt.Err)
		}
		if failure.Package != "" || failure.Command() != nil {
			t.Errorf("a control cancelled before it started anything names %q and %+v,"+
				" want neither", failure.Package, failure.Command())
		}
		if len(f.seen()) != 0 {
			t.Errorf("started %d processes after the context was already cancelled", len(f.seen()))
		}
	})
}

// TestRunControlHonoursTheOutputLimitAndReportsTruncation is the output budget
// applied to a control, and the reporting half is the one that matters here.
//
// A control's capture is what a consumer shows beside a mutant's failure, so
// one that silently lost the first megabyte — where a panic or a build failure
// would be — reads exactly like a suite that failed for the reason shown at the
// end.
func TestRunControlHonoursTheOutputLimitAndReportsTruncation(t *testing.T) {
	deciding := runner.Result{
		ExitCode:    1,
		Duration:    time.Millisecond,
		Output:      []byte(runner.OutputTruncatedPrefix + ": …\n--- FAIL: TestA\n"),
		OutputBytes: 1 << 20,
		Truncated:   true,
	}
	f := &fake{respond: func(context.Context, call) runner.Result { return deciding }}
	run := controlRun()
	run.OutputLimit = 777

	attempt := execute.RunControl(t.Context(), options(f, 1), run, testBins("example.com/a"))

	seen := f.seen()
	if len(seen) != 1 {
		t.Fatalf("started %d processes, want 1", len(seen))
	}
	if seen[0].OutputLimit != 777 {
		t.Errorf("spec OutputLimit = %d, want the control's 777", seen[0].OutputLimit)
	}
	if !attempt.Truncated {
		t.Error("Truncated = false although the capture lost bytes")
	}
	if attempt.OutputBytes != deciding.OutputBytes {
		t.Errorf("OutputBytes = %d, want everything the binary wrote, %d",
			attempt.OutputBytes, deciding.OutputBytes)
	}
	if !slices.Equal(attempt.Output, deciding.Output) {
		t.Errorf("output = %q, want the bytes the budget kept %q", attempt.Output, deciding.Output)
	}
}

// TestRunControlRecordsOneExecPerBinaryStarted joins a control to the commands
// underneath it.
//
// There is no summarising payload for a control in the trace contract, so the
// per-binary `exec` events *are* the account of one: they carry the argv, the
// directory, the environment names and the exit status, and their kind is what
// tells a control apart from the mutant run beside it. An attempt whose
// sequences did not match the recording's would leave a consumer's own
// recording joined to nothing.
func TestRunControlRecordsOneExecPerBinaryStarted(t *testing.T) {
	f := &fake{respond: func(context.Context, call) runner.Result { return passed() }}
	opts, sink := traced(t, f, options(f, 1))
	bins := testBins("example.com/a", "example.com/b", "example.com/c")

	attempt := execute.RunControl(t.Context(), opts, controlRun(), bins)

	if attempt.Err != nil {
		t.Fatalf("err = %v, want a control every binary passed", attempt.Err)
	}
	if attempt.ExitCode != 0 || attempt.Package != "" {
		t.Errorf("a control every binary passed reports exit %d in %q, want 0 and no deciding package",
			attempt.ExitCode, attempt.Package)
	}
	if want := []string{"example.com/a", "example.com/b", "example.com/c"}; !slices.Equal(attempt.Binaries, want) {
		t.Errorf("binaries = %q, want every one of %q", attempt.Binaries, want)
	}
	if got := execSeqs(sink); !slices.Equal(attempt.ExecSeqs, got) {
		t.Errorf("ExecSeqs = %v, want the recording's own exec sequences %v", attempt.ExecSeqs, got)
	}
	events := eventsOf(sink, trace.TypeExec)
	if len(events) != len(bins) {
		t.Fatalf("the recording holds %d exec events, want one per binary started (%d)",
			len(events), len(bins))
	}
	for i, event := range events {
		if event.Exec.Kind != trace.ExecKindControlRun {
			t.Errorf("exec %d is recorded as %q, want %q: a control and a mutant run are the same"+
				" binary started the same way, and the kind is what tells them apart",
				i, event.Exec.Kind, trace.ExecKindControlRun)
		}
		if event.Exec.Subject != bins[i].ImportPath {
			t.Errorf("exec %d is about %q, want the package it ran %q",
				i, event.Exec.Subject, bins[i].ImportPath)
		}
	}
}
