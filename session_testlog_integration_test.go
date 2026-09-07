// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

// Recording what a target touched: `RecordTestLog` on the three requests, and
// the `TestLogs` the three results carry.
//
// The engine hands a target the standard `-test.testlogfile`, reads the file
// back after the binary exits, and returns what the testing package wrote:
// which environment variables the target read, which files it opened or
// stat-ed, and where it changed directory to. Nothing is resolved and nothing
// is interpreted — a consumer deciding whether a cached result is still valid
// is the one that knows what the names mean.
//
// The tests reuse the shared sessions, because every claim here is about the
// answers *one* prepared session gives.

package gomutants_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/trace"
)

// testLogHeader is the first line the testing package writes into an action
// log, spelled out here rather than imported: what a consumer reads is a file
// a Go test binary wrote, and this is the byte sequence cmd/go itself checks
// for.
const testLogHeader = "# test log\n"

// testLogFlagPrefix is the flag the engine adds for itself when a request asks
// for the log, and the one a caller may not supply while it does.
const testLogFlagPrefix = "-test.testlogfile="

// hasEntry reports whether a recorded log names one operation on one name.
func hasEntry(log gomutants.TestLog, op gomutants.TestLogOp, name string) bool {
	return slices.ContainsFunc(log.Entries, func(entry gomutants.TestLogEntry) bool {
		return entry.Op == op && entry.Name == name
	})
}

// requireOneCompleteLog is what a target that ran to the end must produce: one
// record for the one binary that ran, complete, with nothing to explain away.
func requireOneCompleteLog(t *testing.T, logs []gomutants.TestLog, pkg string) gomutants.TestLog {
	t.Helper()
	if len(logs) != 1 {
		t.Fatalf("TestLogs = %+v, want one record for the one binary the call started", logs)
	}
	log := logs[0]
	if log.Package != pkg {
		t.Errorf("TestLogs[0].Package = %q, want %q", log.Package, pkg)
	}
	if log.Dir == "" {
		t.Error("TestLogs[0].Dir is empty; a relative name in the log resolves against it")
	}
	if log.Err != "" {
		t.Fatalf("TestLogs[0].Err = %q, want a log that could be read", log.Err)
	}
	if !log.Complete {
		t.Errorf("TestLogs[0].Complete = false for a binary that exited on its own; the testing"+
			" package flushes the log from m.after(), so a finished binary leaves a finished log: %+v", log)
	}
	return log
}

// execEventAt is the `exec` event one execution's children were recorded at.
func execEventAt(t *testing.T, events []trace.Event, seq int64) trace.ExecRecord {
	t.Helper()
	for _, event := range events {
		if event.Seq == seq && event.Type == trace.TypeExec {
			return *event.Exec
		}
	}
	t.Fatalf("the recording holds no exec event at seq %d", seq)
	return trace.ExecRecord{}
}

// firstExecOfAttempt is the `exec` event of the first binary a `mutant-exec`
// event names, which is how a result reaches the argument vector its child
// really received.
func firstExecOfAttempt(t *testing.T, events []trace.Event, attemptSeq int64) trace.ExecRecord {
	t.Helper()
	for _, event := range events {
		if event.Seq != attemptSeq || event.Mutant == nil {
			continue
		}
		if len(event.Mutant.ExecSeqs) == 0 {
			t.Fatalf("the attempt at seq %d names no executions", attemptSeq)
		}
		return execEventAt(t, events, event.Mutant.ExecSeqs[0])
	}
	t.Fatalf("the recording holds no mutant-exec event at seq %d", attemptSeq)
	return trace.ExecRecord{}
}

// withoutTestLogFlag is an argument vector with the engine's own flag taken
// out, so that two runs of one target can be compared element for element.
func withoutTestLogFlag(argv []string) []string {
	return slices.DeleteFunc(slices.Clone(argv), func(argument string) bool {
		return strings.HasPrefix(argument, testLogFlagPrefix)
	})
}

// TestExecRecordsTheTestLog is the feature in one assertion, plus the two
// claims about the recording that keep it honest.
//
// The injected TestSessionEnvironment reads EXPECT_CLEAN, so a log naming it is
// a fact the target produced from inside the child process rather than
// something the engine could have written for it. It is read *unset* here — an
// execution activates a mutant, and that target is red when EXPECT_CLEAN says
// the environment should be clean — which is the more useful half of the claim
// anyway: a variable a test consulted and did not find is still an input, and a
// consumer deciding whether a cached verdict still holds has to be told the
// same day somebody exports it.
//
// The second half runs the very same target with recording off and compares the
// two `exec` events: the argument vectors must differ by exactly the one flag,
// and the environment the child could see must not differ at all. The log is
// named on the command line and never in the environment, which is what lets a
// consumer join a recording against a run that recorded and one that did not.
func TestExecRecordsTheTestLog(t *testing.T) {
	prepared := controlled(t)
	mutant := survivingMutant(t, prepared)
	args := []string{"-test.run=^TestSessionEnvironment$"}

	recorded, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:        mutant.ID,
		Package:       killableModule,
		Args:          args,
		RecordTestLog: true,
	})
	if err != nil {
		t.Fatalf("executing with the log recorded: %v", err)
	}
	if recorded.Outcome != gomutants.OutcomeSurvived {
		t.Fatalf("outcome = %s, want a survivor:\n%s", recorded.Outcome, recorded.OutputTail)
	}
	log := requireOneCompleteLog(t, recorded.TestLogs, killableModule)
	if !hasEntry(log, gomutants.TestLogGetenv, "EXPECT_CLEAN") {
		t.Errorf("the log does not name the variable the target read: %+v", log.Entries)
	}

	plain, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  mutant.ID,
		Package: killableModule,
		Args:    args,
	})
	if err != nil {
		t.Fatalf("executing without the log: %v", err)
	}
	if plain.TestLogs != nil {
		t.Errorf("TestLogs = %+v for a request that asked for none, want nil", plain.TestLogs)
	}

	events := recordingOf(t, prepared.workspace)
	with := firstExecOfAttempt(t, events, recorded.TraceSeq)
	without := firstExecOfAttempt(t, events, plain.TraceSeq)
	if len(with.Argv) != len(without.Argv)+1 {
		t.Errorf("argv with the log = %q and without = %q, want exactly one more argument",
			with.Argv, without.Argv)
	}
	if got := withoutTestLogFlag(with.Argv); !slices.Equal(got, without.Argv) {
		t.Errorf("argv without the flag = %q, want the vector the same target got when nothing"+
			" was recorded: %q", got, without.Argv)
	}
	if !slices.Equal(with.EnvNames, without.EnvNames) {
		t.Errorf("env_names = %q with the log and %q without; the log is named on the command"+
			" line and nothing about the child's environment changes", with.EnvNames, without.EnvNames)
	}
}

// TestControlRecordsTheTestLog is the same claim for the original program.
//
// A consumer that caches a mutant's verdict caches the control beside it, and
// the inputs the control read are what tell it whether either is still valid.
// A control that could not answer that would send the consumer back to the
// second workspace Session.Control exists to replace.
func TestControlRecordsTheTestLog(t *testing.T) {
	prepared := controlled(t)

	control, err := prepared.session.Control(t.Context(), gomutants.ControlRequest{
		Package:       killableModule,
		Args:          []string{"-test.run=^TestSessionEnvironment$"},
		Env:           []string{"EXPECT_CLEAN=yes", "FROZEN_AT_OPEN=before"},
		RecordTestLog: true,
	})
	if err != nil {
		t.Fatalf("running the control: %v", err)
	}
	if control.ExitCode != 0 || control.TimedOut {
		t.Fatalf("control = exit %d timeout=%v:\n%s", control.ExitCode, control.TimedOut, control.Output)
	}
	log := requireOneCompleteLog(t, control.TestLogs, killableModule)
	if !hasEntry(log, gomutants.TestLogGetenv, "EXPECT_CLEAN") {
		t.Errorf("the log does not name the variable the target read: %+v", log.Entries)
	}
}

// TestProbeRecordsTheTestLog is the third call, over the probe tree.
//
// It is the same target and the same reading, and it is here because the probe
// tree is a *different* tree: a pass records against binaries built from
// instrumented sources in a snapshot of their own, so a record naming the
// mutant tree's directory would be a fact about the wrong program.
func TestProbeRecordsTheTestLog(t *testing.T) {
	prepared := probeable(t)

	pass, err := prepared.session.Probe(t.Context(), gomutants.ProbeRequest{
		Package:       probeableModule,
		Args:          []string{"-test.run=^TestFlagged$"},
		RecordTestLog: true,
	})
	if err != nil {
		t.Fatalf("probing with the log recorded: %v", err)
	}
	if pass.Outcome != gomutants.ProbeMeasured {
		t.Fatalf("probe outcome = %s, want a measured pass:\n%s", pass.Outcome, pass.Output)
	}
	log := requireOneCompleteLog(t, pass.TestLogs, probeableModule)
	if !hasEntry(log, gomutants.TestLogGetenv, "PROBEABLE_FAIL") {
		t.Errorf("the log does not name the variable the target read: %+v", log.Entries)
	}
}

// TestKilledBinaryLeavesAnIncompleteLog is the honest half of the feature, in a
// real process.
//
// The testing package buffers the log and flushes it from the deferred
// m.after(), so this quiet target — killed a second into a minute's sleep —
// leaves the file it created and nothing in it, and the record says so with an
// Err rather than with an empty measurement.
//
// What must never happen is Complete on a log nothing finished, and it is the
// engine and not the bytes that rules that out: the buffer flushes whenever it
// fills, so a *chatty* killed target leaves a log that ends at a line boundary
// and holds a prefix of what it touched. A binary the supervisor tore down
// therefore reports Complete false whatever it wrote, which is
// TestKilledTargetNeverReportsACompleteLog in internal/execute — where a fake
// runner can leave exactly the log a mid-run flush leaves. This is the same
// claim against a process the operating system really killed.
func TestKilledBinaryLeavesAnIncompleteLog(t *testing.T) {
	prepared := controlled(t)

	result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:        survivingMutant(t, prepared).ID,
		Package:       killableModule,
		Args:          []string{"-test.run=^TestSessionBlocks$"},
		Env:           []string{sessionBlockEnv + "=yes"},
		Timeout:       2 * time.Second,
		RecordTestLog: true,
	})
	if err != nil {
		t.Fatalf("executing the blocking target: %v", err)
	}
	if result.Outcome != gomutants.OutcomeTimedOut {
		t.Fatalf("outcome = %s, want the supervisor to have killed the target", result.Outcome)
	}
	if len(result.TestLogs) != 1 {
		t.Fatalf("TestLogs = %+v, want one record for the binary that was started", result.TestLogs)
	}
	log := result.TestLogs[0]
	if log.Complete {
		t.Errorf("TestLogs[0].Complete = true for a binary the supervisor killed: %+v", log)
	}
	if log.Err == "" && len(log.Entries) == 0 {
		t.Error("TestLogs[0] reports neither entries nor a reason, so a caller cannot tell an" +
			" unfinished log from a target that touched nothing")
	}
}

// TestRecordingRefusesACallerSuppliedTestLogFlag is the collision, refused
// before anything is started.
//
// Two -test.testlogfile arguments are not two logs: the standard flag package
// keeps the last value it sees, so whichever of the caller and the engine came
// second would silently win and the other would report on a file nobody wrote.
// The refusal names the flag and the call, so a consumer composing one request
// for an execution and the control beside it is told which of them said no.
func TestRecordingRefusesACallerSuppliedTestLogFlag(t *testing.T) {
	killable := controlled(t)
	probed := probeable(t)
	caller := testLogFlagPrefix + filepath.Join(t.TempDir(), "caller.log")

	t.Run("exec", func(t *testing.T) {
		_, err := killable.session.Exec(t.Context(), gomutants.ExecRequest{
			Mutant:        survivingMutant(t, killable).ID,
			Package:       killableModule,
			Args:          []string{"-test.run=^TestClamp$", caller},
			RecordTestLog: true,
		})
		if err == nil {
			t.Fatal("Exec accepted a request that supplies the flag the recording owns")
		}
		assertReservedFlag(t, "exec", err, "-test.testlogfile")
	})
	t.Run("control", func(t *testing.T) {
		_, err := killable.session.Control(t.Context(), gomutants.ControlRequest{
			Package:       killableModule,
			Args:          []string{"-test.run=^TestClamp$", caller},
			RecordTestLog: true,
		})
		if err == nil {
			t.Fatal("Control accepted a request that supplies the flag the recording owns")
		}
		assertReservedFlag(t, "control", err, "-test.testlogfile")
	})
	t.Run("probe", func(t *testing.T) {
		_, err := probed.session.Probe(t.Context(), gomutants.ProbeRequest{
			Package:       probeableModule,
			Args:          []string{"-test.run=^TestFlagged$", caller},
			RecordTestLog: true,
		})
		if err == nil {
			t.Fatal("Probe accepted a request that supplies the flag the recording owns")
		}
		assertReservedFlag(t, "probe", err, "-test.testlogfile")
	})
}

// TestCallerSuppliedTestLogFlagStillPassesThroughWithoutRecording pins the
// method this feature replaces, because a consumer is entitled to go on using
// it until it moves.
//
// goatest smuggles its own -test.testlogfile through Args and strips it again
// on both sides of its trace. That is not reserved, it is not rewritten, and it
// is not duplicated: the flag reaches the binary exactly as it was written, the
// binary writes the caller's file, and the engine adds nothing beside it.
func TestCallerSuppliedTestLogFlagStillPassesThroughWithoutRecording(t *testing.T) {
	prepared := controlled(t)
	path := filepath.Join(t.TempDir(), "caller.log")

	result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  survivingMutant(t, prepared).ID,
		Package: killableModule,
		Args:    []string{"-test.run=^TestSessionEnvironment$", testLogFlagPrefix + path},
	})
	if err != nil {
		t.Fatalf("executing with the caller's own flag: %v", err)
	}
	if result.Outcome != gomutants.OutcomeSurvived {
		t.Fatalf("outcome = %s, want a survivor:\n%s", result.Outcome, result.OutputTail)
	}
	if result.TestLogs != nil {
		t.Errorf("TestLogs = %+v, want nil: the caller asked the engine for nothing", result.TestLogs)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the caller's own log was not written: %v", err)
	}
	if !strings.HasPrefix(string(written), testLogHeader) {
		t.Fatalf("the caller's log = %q, want a test action log", written)
	}
	if got := strings.Count(string(written), testLogHeader); got != 1 {
		t.Errorf("the caller's log holds %d headers, want 1: the engine wrote into it too", got)
	}
	if !strings.Contains(string(written), "getenv EXPECT_CLEAN\n") {
		t.Errorf("the caller's log does not name the variable the target read:\n%s", written)
	}
}

// TestTestLogScratchIsRemoved is the promise every per-call temporary directory
// carries, applied to the file this feature adds to one.
//
// The log is private to the call: it lives in the directory the call already
// owns, and it goes when that directory goes. A record that outlived its call
// would be a file nobody removes, one per binary per execution, in a run that
// executes thousands.
func TestTestLogScratchIsRemoved(t *testing.T) {
	prepared := controlled(t)
	before := perCallScratch(t, prepared.parent)

	result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:        survivingMutant(t, prepared).ID,
		Package:       killableModule,
		Args:          []string{"-test.run=^TestClamp$"},
		RecordTestLog: true,
	})
	if err != nil {
		t.Fatalf("executing with the log recorded: %v", err)
	}
	if len(result.TestLogs) != 1 {
		t.Fatalf("TestLogs = %+v, want the one binary that ran", result.TestLogs)
	}

	expectScratchAfterCall(t, prepared, before, perCallScratch(t, prepared.parent))
}

// TestFuzzTargetTestLog is the one target the engine records nothing for, and
// the reason is in the Go source rather than in a policy.
//
// internal/fuzz starts every worker with the coordinator's own arguments —
// `args := append([]string{"-test.fuzzworker"}, os.Args[1:]...)` — so a worker
// inherits -test.testlogfile and, having its own first call to M.Run, reaches
// the os.Create in testing's m.before() and truncates the file the coordinator
// is writing. Several processes then append to one path at offsets of their
// own. cmd/go never combines the two either: -test.fuzz is not a cacheable
// test argument, so it disables the test cache and the flag is not passed.
//
// So the engine does not pass it, the child's argument vector says so, and the
// record carries a reason instead of a measurement — which is the one answer
// that cannot be mistaken for "this fuzz target touched nothing".
//
// The budget is one iteration rather than a duration, which is what
// `-test.fuzztime=1x` means. Nothing here is about fuzzing: what is under test
// is the command line a coordinator is started with and the record that comes
// back, and a target given a wall-clock budget is a target whose cost depends
// on how loaded the machine is — the one way this assertion could fail for a
// reason that has nothing to do with it.
func TestFuzzTargetTestLog(t *testing.T) {
	prepared := controlled(t)

	result, err := prepared.session.Exec(t.Context(), gomutants.ExecRequest{
		Mutant:  survivingMutant(t, prepared).ID,
		Package: killableModule,
		Args: []string{
			"-test.run=^$",
			"-test.fuzz=^FuzzSessionIdentity$",
			"-test.fuzztime=1x",
		},
		Timeout:       30 * time.Second,
		RecordTestLog: true,
	})
	if err != nil {
		t.Fatalf("fuzzing with the log recorded: %v", err)
	}
	if result.Outcome != gomutants.OutcomeSurvived {
		t.Fatalf("outcome = %s, want a survivor:\n%s", result.Outcome, result.OutputTail)
	}
	if len(result.TestLogs) != 1 {
		t.Fatalf("TestLogs = %+v, want one record for the binary that was started", result.TestLogs)
	}
	log := result.TestLogs[0]
	if log.Err == "" {
		t.Error("TestLogs[0].Err is empty for a fuzz target; a record with neither entries nor a" +
			" reason reads as a target that touched nothing")
	}
	if log.Complete || len(log.Entries) != 0 {
		t.Errorf("TestLogs[0] = %+v, want no measurement at all for a fuzz target", log)
	}

	argv := firstExecOfAttempt(t, recordingOf(t, prepared.workspace), result.TraceSeq).Argv
	if !slices.Equal(argv, withoutTestLogFlag(argv)) {
		t.Errorf("argv = %q, want no %s: a fuzz worker inherits it and recreates the file",
			argv, testLogFlagPrefix)
	}
}
