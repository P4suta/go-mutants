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

func controlRun(args ...string) execute.ControlRun {
	return execute.ControlRun{Timeout: mutantTimeout, Args: args}
}

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

func withoutActivation(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return key == instrument.ActiveEnv
	})
}

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
