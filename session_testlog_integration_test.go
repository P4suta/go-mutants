// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

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

const testLogHeader = "# test log\n"

const testLogFlagPrefix = "-test.testlogfile="

func hasEntry(log gomutants.TestLog, op gomutants.TestLogOp, name string) bool {
	return slices.ContainsFunc(log.Entries, func(entry gomutants.TestLogEntry) bool {
		return entry.Op == op && entry.Name == name
	})
}

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

func withoutTestLogFlag(argv []string) []string {
	return slices.DeleteFunc(slices.Clone(argv), func(argument string) bool {
		return strings.HasPrefix(argument, testLogFlagPrefix)
	})
}

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
