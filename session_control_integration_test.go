// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// Session.Control: the original program, run through the binaries a session
// already prepared.
//
// A consumer measuring a mutant wants the same tests run twice — once with the
// mutant on and once with it off — and until this call the only way to get the
// second was to freeze a second copy of the module and build a second set of
// binaries for it. The claim every test here circles is that the second copy
// was never needed: with nothing activated, the mutant tree's binaries *are*
// the user's program, so a control is an execution minus one environment entry.
//
// The tests therefore come in pairs wherever they can. What a control reports
// is stated beside what Exec reports about the same target, because "the
// control passed" is only worth anything if the same binary, the same
// arguments and the same budget produced a kill when the mutant was switched
// on.

package gomutants_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
	"github.com/P4suta/go-mutants/trace"
)

// controlFailEnv switches the injected fixture's failing test on. Its value,
// not merely its presence, is what the target reads, so that an inherited empty
// variable cannot turn it on by accident — the rule [sessionBlockEnv] follows
// and for the same reason.
const controlFailEnv = "CONTROL_FAIL"

// killableModule is the fixture's import path, as a request's Package takes it.
const killableModule = "fixture.example/killable"

// controlledFixture is the one session the control tests share: fixtures/killable
// with [killableExtraTests] written into it.
//
// It is killable rather than probeable because two of the claims below need
// targets only the injected source has — one that proves no activation reached
// the child from inside the process, and one that is red on the original
// program — and a shared session is what keeps them affordable: preparing one
// is the expensive thing this file does, and every assertion here is about the
// answers *one* prepared session gives.
//
// The environment is opened without the fixture's gate variables for the reason
// [hostEnvWithoutFixtureGates] exists: Open freezes an environment and every
// target inherits it, so a developer or a runner with SESSION_BLOCK already
// exported would have the injected sleeper cost this file a minute per target,
// and one with CONTROL_FAIL exported would make every control below red.
// Verification is skipped because nothing here needs the fixture's own suite to
// have been run against the instrumented tree, only binaries to run it in.
var controlledFixture = sync.OnceValue(func() *preparedFixture {
	return prepareFixtureWith("killable",
		map[string]string{"session_test.go": killableExtraTests},
		gomutants.OpenOptions{Env: hostEnvWithoutFixtureGates()},
		gomutants.PrepareOptions{
			Operators:     []string{"comparison"},
			SkipVerify:    true,
			MutantTimeout: 30 * time.Second,
		})
})

// controlled returns that session, failing the calling test if preparing it did
// not work.
func controlled(t *testing.T) *preparedFixture {
	t.Helper()
	prepared := controlledFixture()
	if prepared.err != nil {
		t.Fatalf("preparing the killable fixture for the control tests: %v", prepared.err)
	}
	return prepared
}

// survivingMutant is the fixture's mutant nothing calls, which is what a test
// asks for when it wants an execution that is *not* about the mutant: activate
// it and every target in the package still behaves exactly as the original.
func survivingMutant(t *testing.T, prepared *preparedFixture) gomutants.Mutant {
	t.Helper()
	return mutantkit.APIMutantAt(t, prepared.catalog, "untested.go", "neq-to-eq")
}

// TestControlRunsTheOriginalProgram is the feature in one assertion, and its
// second half is what makes the first mean anything.
//
// The injected TestSessionEnvironment fails when EXPECT_CLEAN is set and
// GO_MUTANTS_ACTIVE is not empty, so it is a target that reports from *inside
// the child process* whether an activation reached it. Under a control it
// passes, which is the claim; under an execution of the very same binary with
// the same arguments and the same overlay it fails and the mutant is killed,
// which is what rules out the control having passed because nothing ran, or
// because the target does not look, or because activation is broken generally.
func TestControlRunsTheOriginalProgram(t *testing.T) {
	prepared := controlled(t)
	args := []string{"-test.run=^TestSessionEnvironment$"}
	// FROZEN_AT_OPEN is the target's own second condition and is supplied as an
	// overlay rather than frozen at Open, because this file's session is shared
	// and one test may not decide what the whole of it inherits.
	env := []string{expectCleanEnv + "=yes", "FROZEN_AT_OPEN=before"}

	control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		Package: killableModule,
		Args:    args,
		Env:     env,
	})
	if err != nil {
		t.Fatalf("running the control: %v", err)
	}
	if control.ExitCode != 0 || control.TimedOut {
		t.Fatalf("control = exit %d timeout=%v, want the original program to pass:\n%s",
			control.ExitCode, control.TimedOut, control.Output)
	}
	if control.Package != "" {
		t.Errorf("Package = %q, want no deciding binary named for a control every binary passed",
			control.Package)
	}
	if !slices.Equal(control.Binaries, []string{killableModule}) {
		t.Errorf("Binaries = %v, want the one prepared binary %q", control.Binaries, killableModule)
	}
	if control.TraceSeq == 0 {
		t.Error("TraceSeq = 0 for a control that ran, so its recorded summary cannot be found")
	}

	killed, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  survivingMutant(t, prepared).ID,
		Package: killableModule,
		Args:    args,
		Env:     env,
	})
	if err != nil {
		t.Fatalf("executing the same target with a mutant active: %v", err)
	}
	if killed.Outcome != gomutants.OutcomeKilled {
		t.Fatalf("the same target under Exec = %s, want a kill: the target reports an activation it"+
			" can see, so a control that passed while this also passed proves nothing:\n%s",
			killed.Outcome, killed.OutputTail)
	}
}

// TestControlTimesOutLikeExec is the paired-timeout guarantee applied to a
// control, and it proves the same two things Exec's own timeout test does.
//
// The bound rules out a call that *returned late* — a supervisor that waited
// for a tree it should have killed — and [requireTargetIsGone] rules out the
// other half, which no elapsed check can: a call that came back promptly while
// the process tree it started went on sleeping. Both are asserted for the
// control and for the execution beside it, because a control that was bounded
// by something other than the session's supervisor would be a second timeout
// mechanism nobody wrote down.
func TestControlTimesOutLikeExec(t *testing.T) {
	prepared := controlled(t)
	// Two seconds, for the reason the blocking executions in
	// api_integration_test.go use it: a freshly written test binary is scanned
	// before it runs on some machines, and a budget that expired inside the
	// loader would leave the pid unrecorded and the proof vacuous. It is two
	// orders below the minute the target sleeps for.
	const budget = 2 * time.Second
	controlPID := filepath.Join(t.TempDir(), "control.pid")
	execPID := filepath.Join(t.TempDir(), "exec.pid")
	args := []string{"-test.run=^TestSessionBlocks$"}
	blocking := func(pidFile string) []string {
		return []string{sessionBlockEnv + "=yes", sessionBlockPIDFileEnv + "=" + pidFile}
	}

	started := time.Now()
	control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		Package: killableModule,
		Args:    args,
		Env:     blocking(controlPID),
		Timeout: budget,
	})
	if err != nil {
		t.Fatalf("running the bounded control: %v", err)
	}
	if !control.TimedOut {
		t.Errorf("control = %+v, want a timeout", control)
	}
	if control.ExitCode >= 0 {
		t.Errorf("ExitCode = %d, want a negative status: a tree the supervisor killed returned"+
			" none, and a zero there reads as green to a caller that forgot TimedOut",
			control.ExitCode)
	}
	if control.Package != killableModule {
		t.Errorf("Package = %q, want the binary that hung %q", control.Package, killableModule)
	}
	if elapsed := time.Since(started); elapsed > cleanupBound {
		t.Errorf("the bounded control returned after %s, want prompt process-tree cleanup", elapsed)
	}
	requireTargetIsGone(t, controlPID, "bounded control")

	started = time.Now()
	execution, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  survivingMutant(t, prepared).ID,
		Package: killableModule,
		Args:    args,
		Env:     blocking(execPID),
		Timeout: budget,
	})
	if err != nil {
		t.Fatalf("executing the bounded target: %v", err)
	}
	if execution.Outcome != gomutants.OutcomeTimedOut {
		t.Errorf("the same target under Exec = %s, want %s: the two calls are bounded by one"+
			" supervisor and must agree", execution.Outcome, gomutants.OutcomeTimedOut)
	}
	if elapsed := time.Since(started); elapsed > cleanupBound {
		t.Errorf("the bounded execution returned after %s, want prompt process-tree cleanup", elapsed)
	}
	requireTargetIsGone(t, execPID, "bounded execution")
}

// TestControlReportsAFailingSuite is the two answers a control gives, and the
// pair is the whole diagnostic value of the call.
//
// A mutant's suite going red is only evidence about the mutant if the same
// suite is green on the original program. So a control of the target that kills
// a known-killed mutant has to pass — that is the first half — and a control of
// a target that is red without any mutant has to say so, with the output,
// rather than come back as an infrastructure failure. A consumer that could not
// tell those two apart would report the repository's own broken test as a
// mutation score.
func TestControlReportsAFailingSuite(t *testing.T) {
	prepared := controlled(t)
	clamp := mutantkit.APIMutantAt(t, prepared.catalog, "clamp.go", "lt-to-le")

	t.Run("the target that kills a mutant passes without one", func(t *testing.T) {
		args := []string{"-test.run=^TestClamp$"}
		killed, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
			Mutant:  clamp.ID,
			Package: killableModule,
			Args:    args,
		})
		if err != nil {
			t.Fatalf("executing %s: %v", clamp.DisplayID, err)
		}
		if killed.Outcome != gomutants.OutcomeKilled {
			t.Fatalf("%s = %s, want a kill:\n%s", clamp.DisplayID, killed.Outcome, killed.OutputTail)
		}

		control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
			Package: killableModule,
			Args:    args,
		})
		if err != nil {
			t.Fatalf("running the control: %v", err)
		}
		if control.ExitCode != 0 || control.TimedOut {
			t.Fatalf("the control of the killing target = exit %d timeout=%v, want it green:"+
				" the kill would be the repository's own red suite:\n%s",
				control.ExitCode, control.TimedOut, control.Output)
		}
	})

	t.Run("a target that is red on the original program", func(t *testing.T) {
		control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
			Package: killableModule,
			Args:    []string{"-test.run=^TestControlFails$"},
			Env:     []string{controlFailEnv + "=yes"},
		})
		if err != nil {
			t.Fatalf("a failing suite came back as an error rather than a result: %v", err)
		}
		if control.ExitCode != 1 {
			t.Errorf("ExitCode = %d, want the failing binary's 1", control.ExitCode)
		}
		if control.Package != killableModule {
			t.Errorf("Package = %q, want the binary that failed %q", control.Package, killableModule)
		}
		if !bytes.Contains(control.Output, []byte("CONTROL_FAIL asked this test to fail")) {
			t.Errorf("Output = %q, want the failing target's own words: a red control that could not"+
				" be shown to a user is a finding nobody can act on", control.Output)
		}
		if control.TotalBytes < int64(len(control.Output)) {
			t.Errorf("TotalBytes = %d, want at least the %d bytes that were kept",
				control.TotalBytes, len(control.Output))
		}
	})
}

// TestControlRefusesWhatExecRefuses keeps the two calls one request vocabulary.
//
// A consumer composing arguments for a mutant run hands the same arguments to
// the control beside it, so a flag one refuses and the other accepts would be a
// difference discovered at run time, in the call that was supposed to be the
// trustworthy half. Every row therefore asserts the same typed refusal from
// both, and that the refusal names which call made it.
func TestControlRefusesWhatExecRefuses(t *testing.T) {
	prepared := controlled(t)
	mutant := survivingMutant(t, prepared)

	for _, test := range []struct {
		name    string
		pkg     string
		args    []string
		env     []string
		assert  func(t *testing.T, call string, err error)
		timeout time.Duration
		memory  int64
	}{
		{
			name: "the reserved timeout flag",
			args: []string{"-test.timeout=1s"},
			assert: func(t *testing.T, call string, err error) {
				assertReservedFlag(t, call, err, "-test.timeout")
			},
		},
		{
			name: "the reserved fuzz cache directory",
			args: []string{"-test.fuzzcachedir=/tmp/somewhere"},
			assert: func(t *testing.T, call string, err error) {
				assertReservedFlag(t, call, err, "-test.fuzzcachedir")
			},
		},
		{
			name: "the reserved fuzz worker flag",
			args: []string{"-test.fuzzworker"},
			assert: func(t *testing.T, call string, err error) {
				assertReservedFlag(t, call, err, "-test.fuzzworker")
			},
		},
		{
			name: "the reserved activation variable",
			env:  []string{"GO_MUTANTS_ACTIVE=stolen"},
			assert: func(t *testing.T, _ string, err error) {
				t.Helper()
				var reserved *gomutants.ReservedError
				if !errors.As(err, &reserved) {
					t.Fatalf("err = %v, want a *ReservedError", err)
				}
				if reserved.Variable != "GO_MUTANTS_ACTIVE" || reserved.Flag != "" {
					t.Errorf("err = %+v, want the variable refused", reserved)
				}
			},
		},
		{
			name: "the reserved temporary directory variable",
			env:  []string{"TMPDIR=/tmp/somewhere"},
			assert: func(t *testing.T, _ string, err error) {
				t.Helper()
				var reserved *gomutants.ReservedError
				if !errors.As(err, &reserved) {
					t.Fatalf("err = %v, want a *ReservedError", err)
				}
				if reserved.Variable != "TMPDIR" {
					t.Errorf("err = %+v, want TMPDIR refused", reserved)
				}
			},
		},
		{
			name: "a package this session prepared no binary for",
			pkg:  "fixture.example/killable/nowhere",
			assert: func(t *testing.T, call string, err error) {
				t.Helper()
				var missing *gomutants.PackageNotPreparedError
				if !errors.As(err, &missing) {
					t.Fatalf("err = %v, want a *PackageNotPreparedError", err)
				}
				if missing.Call != call {
					t.Errorf("Call = %q, want %q", missing.Call, call)
				}
				if missing.Package != "fixture.example/killable/nowhere" {
					t.Errorf("Package = %q, want the request's own spelling", missing.Package)
				}
			},
		},
		{
			name:    "a negative timeout",
			timeout: -time.Second,
			assert: func(t *testing.T, call string, err error) {
				t.Helper()
				if want := "gomutants: session " + call + ": timeout is negative"; err.Error() != want {
					t.Errorf("err = %q, want %q", err, want)
				}
			},
		},
		{
			// The budget's other half, refused in the same shape and by both
			// calls. A negative bound is not a small one: it would be a budget
			// every process is over, so a session that accepted it would kill
			// every mutant it started and call each one detected.
			name:   "a negative memory limit",
			memory: -1,
			assert: func(t *testing.T, call string, err error) {
				t.Helper()
				if want := "gomutants: session " + call + ": memory limit is negative"; err.Error() != want {
					t.Errorf("err = %q, want %q", err, want)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, execErr := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
				Mutant:      mutant.ID,
				Package:     test.pkg,
				Args:        test.args,
				Env:         test.env,
				Timeout:     test.timeout,
				MemoryLimit: test.memory,
			})
			if execErr == nil {
				t.Fatal("Exec accepted the request, so there is nothing for Control to agree with")
			}
			test.assert(t, "exec", execErr)

			_, controlErr := prepared.session.Control(t.Context(), gomutants.ControlRequest{
				Package:     test.pkg,
				Args:        test.args,
				Env:         test.env,
				Timeout:     test.timeout,
				MemoryLimit: test.memory,
			})
			if controlErr == nil {
				t.Fatal("Control accepted a request Exec refused")
			}
			test.assert(t, "control", controlErr)
		})
	}
}

// assertReservedFlag is the refusal a reserved test flag produces, named by the
// call that made it.
func assertReservedFlag(t *testing.T, call string, err error, flag string) {
	t.Helper()
	var reserved *gomutants.ReservedError
	if !errors.As(err, &reserved) {
		t.Fatalf("err = %v, want a *ReservedError", err)
	}
	if reserved.Flag != flag || reserved.Variable != "" {
		t.Errorf("err = %+v, want the flag %q refused", reserved, flag)
	}
	if reserved.Call != call {
		t.Errorf("Call = %q, want %q: a consumer composing one request for both calls has to be"+
			" told which of them said no", reserved.Call, call)
	}
}

// TestControlLeavesNoScratchBehind is the promise every per-call temporary
// directory carries: a session that was not asked to keep anything holds
// nothing after the call returns.
//
// It is a set comparison rather than a count, because what has to be true is
// that no directory *this call* made survived it — a session that leaked one
// per control would fill a machine over a run, and a count could be satisfied
// by a leak and a removal happening to cancel out.
//
// The comparison itself is [expectScratchAfterCall], because the answer depends
// on the keep policy the suite is running under: under one, this shared session
// is opened with KeepTemp and the control's own scratch is kept on purpose. The
// promise being checked is the same either way — a call owns exactly the
// directory it made — and stating it in one place is what keeps the two answers
// from drifting into two tests.
func TestControlLeavesNoScratchBehind(t *testing.T) {
	prepared := controlled(t)
	before := perCallScratch(t, prepared.parent)

	control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		Package: killableModule,
		Args:    []string{"-test.run=^TestClamp$"},
	})
	if err != nil {
		t.Fatalf("running the control: %v", err)
	}
	if control.ExitCode != 0 {
		t.Fatalf("control = exit %d, want the original program to pass:\n%s", control.ExitCode, control.Output)
	}

	expectScratchAfterCall(t, prepared, before, perCallScratch(t, prepared.parent))
}

// perCallScratch is every per-call scratch directory under a workspace's
// temporary parent, sorted, whichever of the session's calls made it.
//
// It walks rather than reading one directory because the per-call scratch is
// nested: it lives inside the session's, which lives inside the workspace's,
// which is the arrangement that keeps it out of reach of any sweep.
func perCallScratch(t *testing.T, parent string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(parent, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A directory that vanished under the walk is another call tidying
			// up after itself, which is the behaviour under test rather than a
			// reason to fail.
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "exec-") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", parent, err)
	}
	slices.Sort(found)
	return found
}

// TestControlAndExecShareEveryLaunchFact is the guarantee stated where a
// consumer can check it: in the recording.
//
// A control is evidence about a mutant run only if the two started the same
// program. The `exec` events carry everything that decides which program that
// is — the argument vector, the directory a Go test resolves testdata relative
// to, the paired timeout, and the names of the variables the child could see —
// so comparing them field for field is the strongest form of the claim
// available from outside the engine. The *only* difference allowed is the
// activation variable, and the kind and subject that say which call it was.
func TestControlAndExecShareEveryLaunchFact(t *testing.T) {
	prepared := controlled(t)
	args := []string{"-test.run=^TestClamp$"}
	const budget = 20 * time.Second

	control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		Package: killableModule,
		Args:    args,
		Timeout: budget,
	})
	if err != nil {
		t.Fatalf("running the control: %v", err)
	}
	if control.ExitCode != 0 || control.TimedOut {
		t.Fatalf("control = exit %d timeout=%v, so the events below are not a comparison of two"+
			" runs that both happened:\n%s", control.ExitCode, control.TimedOut, control.Output)
	}
	execution, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  survivingMutant(t, prepared).ID,
		Package: killableModule,
		Args:    args,
		Timeout: budget,
	})
	if err != nil {
		t.Fatalf("executing the same target: %v", err)
	}
	if execution.Outcome != gomutants.OutcomeSurvived {
		t.Fatalf("the execution = %s, want a survivor:\n%s", execution.Outcome, execution.OutputTail)
	}

	events := recordingOf(t, prepared.workspace)
	// Through the published validator, because a new command kind is a new line
	// shape: `control-run` has to be in the schema's enum or every recording
	// carrying one stops validating for a consumer decoding it strictly.
	validateRecording(t, events)
	controlExec := lastExecOfKind(t, events, trace.ExecKindControlRun)
	mutantExec := lastExecOfKind(t, events, trace.ExecKindMutantRun)

	if !slices.Equal(controlExec.Argv, mutantExec.Argv) {
		t.Errorf("the control ran %q and the execution %q", controlExec.Argv, mutantExec.Argv)
	}
	if controlExec.Dir != mutantExec.Dir {
		t.Errorf("the control ran in %q and the execution in %q", controlExec.Dir, mutantExec.Dir)
	}
	if controlExec.TimeoutMS != mutantExec.TimeoutMS {
		t.Errorf("the control was given %d ms and the execution %d ms",
			controlExec.TimeoutMS, mutantExec.TimeoutMS)
	}
	if controlExec.Subject != killableModule {
		t.Errorf("the control's subject = %q, want the package it ran %q",
			controlExec.Subject, killableModule)
	}
	if slices.Contains(controlExec.EnvNames, activationVariable) {
		t.Errorf("the control's child could see %s, so it was not the original program",
			activationVariable)
	}
	if !slices.Contains(mutantExec.EnvNames, activationVariable) {
		t.Fatalf("the execution's child could not see %s either, so two agreeing environments"+
			" would mean nothing", activationVariable)
	}
	want := withoutActivationName(mutantExec.EnvNames)
	if !slices.Equal(controlExec.EnvNames, want) {
		t.Errorf("the control's environment is not the execution's minus the activation:\n got %v\nwant %v",
			controlExec.EnvNames, want)
	}
}

// activationVariable is the one entry a control's environment may lack. It is
// spelled out rather than imported, because internal/instrument is not a
// package a consumer of this API can name and the whole claim is one a consumer
// has to be able to make.
const activationVariable = "GO_MUTANTS_ACTIVE"

// lastExecOfKind is the most recent `exec` event of one kind in a recording,
// which is the one the call that just returned recorded.
func lastExecOfKind(t *testing.T, events []trace.Event, kind string) trace.ExecRecord {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == trace.TypeExec && events[i].Exec.Kind == kind {
			return *events[i].Exec
		}
	}
	t.Fatalf("the recording holds no exec event of kind %q", kind)
	return trace.ExecRecord{}
}

// TestControlHonoursOutputLimit is the output budget applied to a control, and
// it is the same claim [TestSessionExecHonoursOutputLimit] makes about an
// execution: a caller says how much it is willing to hold, and what was dropped
// is reported rather than left to be guessed at from a notice line.
func TestControlHonoursOutputLimit(t *testing.T) {
	prepared := controlled(t)
	const limit = 4096

	control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		Package:     killableModule,
		Args:        []string{"-test.run=^TestPrintsALot$"},
		OutputLimit: limit,
	})
	if err != nil {
		t.Fatalf("running the chatty control: %v", err)
	}
	if control.ExitCode != 0 || control.TimedOut {
		t.Fatalf("control = exit %d timeout=%v:\n%s", control.ExitCode, control.TimedOut, control.Output)
	}
	if !control.Truncated {
		t.Errorf("Truncated = false although the target prints 64 KiB into a %d-byte budget", limit)
	}
	if control.TotalBytes <= limit {
		t.Errorf("TotalBytes = %d, want more than the %d-byte budget", control.TotalBytes, limit)
	}
	if len(control.Output) > limit {
		t.Errorf("len(Output) = %d, want at most %d: the notice is paid for out of the budget",
			len(control.Output), limit)
	}
	if !bytes.HasPrefix(control.Output, []byte(gomutants.OutputTruncatedPrefix)) {
		t.Errorf("Output begins %q, want the exported prefix %q",
			string(control.Output[:min(len(control.Output), 120)]), gomutants.OutputTruncatedPrefix)
	}
}

// TestControlTraceSeqPointsAtItsRecord is the join, and it is the one place the
// shape of a control's recording is pinned.
//
// The trace contract's event `type` enum is closed and holds no payload for a
// control, so the call is recorded as its per-binary `exec` events plus one
// `note` summarising them — and the note is what the result names. A consumer
// holding this number has to land on that note rather than on whatever event
// happened to be recorded next.
func TestControlTraceSeqPointsAtItsRecord(t *testing.T) {
	prepared := controlled(t)

	control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		Package: killableModule,
		Args:    []string{"-test.run=^TestClamp$"},
	})
	if err != nil {
		t.Fatalf("running the control: %v", err)
	}

	events := recordingOf(t, prepared.workspace)
	event := eventAt(t, events, control.TraceSeq)
	if event.Type != trace.TypeNote {
		t.Fatalf("ControlResult.TraceSeq points at a %s, want a %s", event.Type, trace.TypeNote)
	}
	if event.Note.Kind != trace.NoteControl {
		t.Errorf("the note is of kind %q, want %q", event.Note.Kind, trace.NoteControl)
	}
	if !strings.Contains(event.Note.Detail, killableModule) {
		t.Errorf("the note's detail = %q, want the binaries the control ran", event.Note.Detail)
	}
	if event.Note.Code != "" {
		t.Errorf("the note carries the diagnostic code %q for a control that ran", event.Note.Code)
	}

	// And the join down to the commands is a *field* rather than the note's
	// prose. A note has no `exec_seqs` of its own — `mutant-exec` and
	// `probe-exec` do — so without this a consumer would be parsing a sentence
	// written for a person to find the executions its control ran.
	if len(control.ExecSeqs) != len(control.Binaries) {
		t.Fatalf("ExecSeqs = %v for binaries %v, want one sequence per binary started",
			control.ExecSeqs, control.Binaries)
	}
	for i, seq := range control.ExecSeqs {
		child := eventAt(t, events, seq)
		if child.Type != trace.TypeExec || child.Exec.Kind != trace.ExecKindControlRun {
			t.Errorf("ExecSeqs[%d] points at a %s, want an exec of kind %q",
				i, child.Type, trace.ExecKindControlRun)
			continue
		}
		if child.Exec.Subject != control.Binaries[i] {
			t.Errorf("ExecSeqs[%d] is about %q and Binaries[%d] is %q",
				i, child.Exec.Subject, i, control.Binaries[i])
		}
		if seq >= control.TraceSeq {
			t.Errorf("ExecSeqs[%d] = %d is not before the note at %d, so the note does not"+
				" summarise executions that had already been recorded", i, seq, control.TraceSeq)
		}
	}
}

// TestControlWithoutAPackageRunsEveryPreparedBinary pins the whole-package
// request, which is the one an ordinary consumer sends.
//
// An empty Package selects every compiled test package, exactly as it does for
// [gomutants.ExecRequest] — the control of "the whole suite" is the control a
// caller wants beside a mutant run it did not narrow either — and a call that
// quietly ran none of them would report the original program as passing having
// started nothing.
func TestControlWithoutAPackageRunsEveryPreparedBinary(t *testing.T) {
	prepared := controlled(t)

	control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		Args: []string{"-test.run=^TestClamp$"},
	})
	if err != nil {
		t.Fatalf("running the whole-session control: %v", err)
	}
	if !slices.Equal(control.Binaries, prepared.catalog.TestPackages) {
		t.Errorf("Binaries = %v, want every prepared test package %v",
			control.Binaries, prepared.catalog.TestPackages)
	}
	if control.ExitCode != 0 || control.TimedOut {
		t.Fatalf("control = exit %d timeout=%v:\n%s", control.ExitCode, control.TimedOut, control.Output)
	}
}

// TestControlOnAFuzzTargetRunsInAPrivateCopy is the isolation an execution gets,
// applied to a control.
//
// A fuzz target writes: a corpus entry, a cache, a crasher. Under Exec that
// lands in a private copy of the snapshot, so the tree every later mutant is
// measured against does not move underneath the run — and a control that ran a
// fuzz target in the shared tree would drift it in exactly the way the drift
// gate exists to catch, while looking like nothing at all.
//
// The launch shape is compared here too, and for a fuzz target two of its parts
// have to differ: the working directory, because each call runs in a copy of the
// tree of its own, and the value of `-test.fuzzcachedir`, because each call
// caches inside its own scratch. Those two differing *is* the isolation. The
// executable is the same file — the binaries live outside the snapshot and are
// not copied — and everything else must match flag for flag, which is what
// [fuzzLaunchShape] compares.
func TestControlOnAFuzzTargetRunsInAPrivateCopy(t *testing.T) {
	prepared := controlled(t)
	args := []string{
		"-test.run=^$",
		"-test.fuzz=^FuzzSessionIdentity$",
		"-test.fuzztime=100ms",
	}
	const budget = 30 * time.Second

	// Where an *ordinary* control runs, first, so that "in a copy of its own"
	// is a comparison against something rather than a name in a path. An
	// ordinary target runs in the package's directory inside the prepared
	// snapshot; a fuzz target must not.
	if _, plainErr := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		Package: killableModule,
		Args:    []string{"-test.run=^TestClamp$"},
	}); plainErr != nil {
		t.Fatalf("running the plain control: %v", plainErr)
	}
	snapshotDir := lastExecOfKind(t, recordingOf(t, prepared.workspace), trace.ExecKindControlRun).Dir

	control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		Package: killableModule,
		Args:    args,
		Timeout: budget,
	})
	if err != nil {
		t.Fatalf("running the fuzz control: %v", err)
	}
	if control.ExitCode != 0 || control.TimedOut {
		t.Fatalf("fuzz control = exit %d timeout=%v:\n%s", control.ExitCode, control.TimedOut, control.Output)
	}
	execution, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  survivingMutant(t, prepared).ID,
		Package: killableModule,
		Args:    args,
		Timeout: budget,
	})
	if err != nil {
		t.Fatalf("fuzzing the same target with a mutant active: %v", err)
	}
	if execution.Outcome != gomutants.OutcomeSurvived {
		t.Fatalf("the fuzz execution = %s, want a survivor:\n%s", execution.Outcome, execution.OutputTail)
	}

	// The private copy, stated as the thing it exists to protect: the prepared
	// snapshot is exactly as Prepare left it.
	changes, err := prepared.session.Changes()
	if err != nil {
		t.Fatalf("checking the snapshot after the fuzz control: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("the fuzz control changed the prepared snapshot: %+v", changes)
	}

	events := recordingOf(t, prepared.workspace)
	controlExec := lastExecOfKind(t, events, trace.ExecKindControlRun)
	mutantExec := lastExecOfKind(t, events, trace.ExecKindMutantRun)

	controlCache := fuzzCacheDir(t, controlExec.Argv)
	mutantCache := fuzzCacheDir(t, mutantExec.Argv)
	if controlCache == mutantCache {
		t.Errorf("both calls fuzzed into %q; a shared cache is two runs seeding each other", controlCache)
	}
	if controlExec.Dir == snapshotDir {
		t.Errorf("the fuzz control ran in %q, which is where an ordinary control runs: a fuzz"+
			" target that writes there moves the tree every later mutant is measured against",
			controlExec.Dir)
	}
	if mutantExec.Dir == snapshotDir {
		t.Fatalf("the fuzz execution also ran in %q, so the check above is not comparing a"+
			" control against an isolated execution", mutantExec.Dir)
	}
	if controlExec.Dir == mutantExec.Dir {
		t.Errorf("both calls fuzzed in %q; a fuzz target runs in a copy of the tree of its own,"+
			" or one run's corpus is the other's input", controlExec.Dir)
	}
	if controlExec.Argv[0] != mutantExec.Argv[0] {
		t.Errorf("the control started %q and the execution %q; the prepared binaries live outside"+
			" the snapshot and are not copied, so a control runs the same file",
			controlExec.Argv[0], mutantExec.Argv[0])
	}
	if got, want := fuzzLaunchShape(controlExec.Argv), fuzzLaunchShape(mutantExec.Argv); !slices.Equal(got, want) {
		t.Errorf("the control was launched as %v and the execution as %v", got, want)
	}
	if controlExec.TimeoutMS != mutantExec.TimeoutMS {
		t.Errorf("the control was given %d ms and the execution %d ms",
			controlExec.TimeoutMS, mutantExec.TimeoutMS)
	}
	if got, want := controlExec.EnvNames, withoutActivationName(mutantExec.EnvNames); !slices.Equal(got, want) {
		t.Errorf("the control's environment is not the execution's minus the activation:\n got %v\nwant %v",
			got, want)
	}
}

// fuzzLaunchShape is one fuzz argument vector with the one part a private
// scratch necessarily moves taken out: the value of `-test.fuzzcachedir`, which
// the session points at the call's own directory. Everything else — the
// executable, the harness-owned timeout, and every flag the caller wrote, in
// order — has to match between a control and an execution of the same target.
func fuzzLaunchShape(argv []string) []string {
	shape := make([]string, 0, len(argv))
	for _, argument := range argv {
		if strings.HasPrefix(argument, fuzzCacheFlag) {
			shape = append(shape, fuzzCacheFlag+"<scratch>")
			continue
		}
		shape = append(shape, argument)
	}
	return shape
}

// fuzzCacheFlag is the flag the session owns and appends for a fuzz target. It
// is spelled here rather than imported because a consumer cannot import the
// engine's internals, and because it is a *reserved* flag: the value of this
// test is that the session sets it and the caller may not.
const fuzzCacheFlag = "-test.fuzzcachedir="

// fuzzCacheDir is the directory one launch was told to keep its fuzz cache in.
func fuzzCacheDir(t *testing.T, argv []string) string {
	t.Helper()
	for _, argument := range argv {
		if value, ok := strings.CutPrefix(argument, fuzzCacheFlag); ok {
			return value
		}
	}
	t.Fatalf("no %s in %v; the session did not isolate the fuzz cache", fuzzCacheFlag, argv)
	return ""
}

// withoutActivationName is one recorded environment's names with the activation
// removed, which is the whole of what a control's may differ by.
func withoutActivationName(names []string) []string {
	return slices.DeleteFunc(slices.Clone(names), func(name string) bool {
		return name == activationVariable
	})
}

// TestControlScratchIsKeptUnderKeepTemp holds a control to the promise every
// per-call directory carries.
//
// KeepTemp exists to answer the one question a removed directory cannot — what
// did the tree this ran in actually look like — and a control's scratch is half
// of that answer for the run a consumer compares a mutant against: it is where
// the target's TMPDIR pointed and where a fuzz cache lived. A keep that covered
// Exec and Probe and quietly dropped Control would be an escape hatch with a
// hole in exactly the call added last.
//
// The artifact kind is the execution's, deliberately: a kept control scratch is
// the same kind of directory as a kept execution scratch, and inventing a
// fourth kind would make a consumer branch on a distinction that changes
// nothing about what the directory holds.
func TestControlScratchIsKeptUnderKeepTemp(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "killable")
	if err := copyFixtureTree("killable", root); err != nil {
		t.Fatal(err)
	}
	workspace, err := gomutants.Open(t.Context(), root, gomutants.OpenOptions{
		TempDirectory: parent,
		KeepTemp:      true,
		Env:           hostEnvWithoutFixtureGates(),
	})
	if err != nil {
		t.Fatalf("opening workspace: %v", err)
	}
	session, err := workspace.Prepare(t.Context(), gomutants.PrepareOptions{
		Operators:  []string{"comparison"},
		SkipVerify: true,
	})
	if err != nil {
		t.Fatalf("preparing session: %v", err)
	}
	if _, controlErr := session.Control(t.Context(), gomutants.ControlRequest{
		Package: killableModule,
		Args:    []string{"-test.run=^TestClamp$"},
	}); controlErr != nil {
		t.Fatalf("running the control: %v", controlErr)
	}
	if closeErr := workspace.Close(); closeErr != nil {
		t.Fatalf("closing workspace: %v", closeErr)
	}

	scratch := artifactsOfKind(recordingOf(t, workspace), trace.ArtifactKeptExecScratch)
	if len(scratch) != 1 {
		t.Fatalf("the recording names %v as kept per-call scratch, want the one directory the"+
			" control ran in", scratch)
	}
	if directory, statErr := statDirectory(scratch[0]); statErr != nil || !directory {
		t.Errorf("%s is recorded as kept and is not a directory on disk: %v", scratch[0], statErr)
	}
	if !slices.Contains(workspace.Preserved(), scratch[0]) {
		t.Errorf("Preserved() = %v and does not name the kept control scratch %s",
			workspace.Preserved(), scratch[0])
	}
}

// statDirectory reports whether path exists and is a directory.
func statDirectory(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// TestAFailedControlStillPointsAtItsRecord is the one case a consumer most
// wants to read about and the one it could otherwise not find.
//
// A control whose child a cancellation killed establishes nothing — no status,
// no timeout, no output — but it *did* reach an execution, so it is in the
// recording. Handing back a zero sequence there would leave the failure with no
// account at all, and the binaries and their executions are the first question
// a cut-off run raises: which ones had already run?
func TestAFailedControlStillPointsAtItsRecord(t *testing.T) {
	prepared := controlled(t)
	pidFile := filepath.Join(t.TempDir(), "cancelled.pid")

	cancelContext, cancel := context.WithCancel(t.Context())
	timer := time.AfterFunc(2*time.Second, cancel)
	control, err := prepared.session.Control(cancelContext, gomutants.ControlRequest{
		Package: killableModule,
		Args:    []string{"-test.run=^TestSessionBlocks$"},
		Timeout: 30 * time.Second,
		Env: []string{
			sessionBlockEnv + "=yes",
			sessionBlockPIDFileEnv + "=" + pidFile,
		},
	})
	timer.Stop()
	cancel()

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled control = %v, want a reachable context.Canceled", err)
	}
	var failure *gomutants.ExecutionError
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want an *ExecutionError", err)
	}
	if failure.Call != "control" {
		t.Errorf("Call = %q, want %q", failure.Call, "control")
	}
	if failure.Code != "GOM7520" {
		t.Errorf("Code = %q, want the interruption code GOM7520", failure.Code)
	}
	if !slices.Equal(control.Binaries, []string{killableModule}) {
		t.Errorf("Binaries = %v, want the binary that was cut off", control.Binaries)
	}
	if len(control.ExecSeqs) != 1 {
		t.Errorf("ExecSeqs = %v, want the one execution the control had made", control.ExecSeqs)
	}
	if control.TraceSeq == 0 {
		t.Fatal("a failed control carries TraceSeq 0, so its recorded account cannot be found")
	}
	if control.ExitCode != 0 || control.TimedOut || control.Output != nil {
		t.Errorf("a failed control reports exit %d timeout=%v with %d bytes of output, want no"+
			" verdict at all", control.ExitCode, control.TimedOut, len(control.Output))
	}

	events := recordingOf(t, prepared.workspace)
	note := eventAt(t, events, control.TraceSeq)
	if note.Type != trace.TypeNote || note.Note.Kind != trace.NoteControl {
		t.Fatalf("TraceSeq points at a %s, want a %s of kind %q", note.Type, trace.TypeNote, trace.NoteControl)
	}
	if note.Note.Code != failure.Code {
		t.Errorf("the note carries code %q and the error %q; a reader of the recording and a"+
			" reader of the error must be looking at one failure", note.Note.Code, failure.Code)
	}
	for _, seq := range control.ExecSeqs {
		if child := eventAt(t, events, seq); child.Type != trace.TypeExec {
			t.Errorf("ExecSeqs names a %s, want the execution that was cut off", child.Type)
		}
	}
	requireTargetIsGone(t, pidFile, "cancelled control")
}
