// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package execute

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testflag"
	"github.com/P4suta/go-mutants/internal/testlog"
)

// testLogFlagName is the standard test-binary flag that makes a target write
// down what it consulted. cmd/go passes it ahead of the user's own arguments,
// and so does [startTarget].
const testLogFlagName = "-test.testlogfile"

// testLogUndefined is what the standard flag package prints for a flag the
// binary does not define, and it is how a target that predates or does not
// implement [testLogFlagName] announces itself.
const testLogUndefined = "flag provided but not defined: " + testLogFlagName

// testLogUnsupportedExit is the status the standard flag package exits with
// when it refuses an argument: flag.ExitOnError is os.Exit(2).
//
// It is a non-zero status, which is how [RunOne] recognises a kill — so the
// whole of [testLogUnsupported] exists to keep a status of 2 from being scored
// as a detection.
const testLogUnsupportedExit = 2

// testLogDirName is the subdirectory of a call's scratch the logs go in.
//
// A directory of its own because the scratch is the *target's* TMPDIR: a test
// that lists its own temporary directory finds one entry go-mutants put there
// rather than several, and what the entry is is written on it.
const testLogDirName = "testlogs"

// A TestLog is what one test binary recorded about the environment variables
// and files it consulted, read back after it exited.
//
// It names the binary rather than the file the log was written to, because the
// file lives in a directory the call removes and the import path is what
// outlives it. Dir is the directory the binary ran in: a relative name in the
// log is relative to that, and nothing here resolves one.
type TestLog struct {
	// Package is the import path of the test binary this log is about.
	Package string
	// Dir is the working directory that binary ran in, which every relative
	// Name in Entries is relative to until a chdir entry says otherwise.
	Dir string
	// Entries are the actions the binary reported, in order and verbatim.
	Entries []testlog.Entry
	// Complete reports that the log ends at a line boundary *and* that the
	// binary exited on its own.
	//
	// Both halves are needed and the bytes alone are not enough. The testing
	// package writes the log through a 4096-byte bufio.Writer that flushes
	// whenever it fills, so a chatty target the supervisor tore down leaves a
	// log ending in a newline that is nonetheless a fraction of what it
	// touched — which is why [RunOne], [RunProbe] and [RunControl] report false
	// for a timed-out or cancelled binary whatever the last byte is.
	Complete bool
	// Err is why there is no log to read, in one line, and empty when there is
	// one. It is a string rather than an error because it is a fact about one
	// binary carried inside a result, beside every other fact about it — a run
	// that could not record what a target touched is not a run that failed.
	Err string
}

// A testLogPlan is where the binaries of one call write their action logs, or
// why they write none.
//
// It is settled once per call rather than per binary, because every reason not
// to record is a fact about the call: the request did not ask, the call has no
// private directory to write into, or the target is a fuzz target. A per-binary
// decision would be the same answer computed as many times as there are
// binaries, with one more place for it to differ.
type testLogPlan struct {
	// record is what the request asked for. Nothing below matters when it is
	// false, and the call adds no flag and reports no logs at all.
	record bool
	// dir is the directory inside the call's scratch that the logs go in, and
	// is empty whenever nothing is recorded.
	dir string
	// reason is why this call records nothing despite having been asked, and is
	// empty when it records. It is carried on every binary's record, so that a
	// caller reading a result is told why rather than handed an empty
	// measurement.
	reason string
}

// planTestLog settles one call's plan.
//
// Three conditions stop a recording that was asked for, and all three are
// stated as reasons rather than as errors: a missing measurement is not a
// failed run.
//
// The fuzz condition is the one worth writing down, because it is a fact about
// the Go source rather than a policy. internal/fuzz starts every worker with
// the coordinator's own arguments — `append([]string{"-test.fuzzworker"},
// os.Args[1:]...)` — so a worker inherits this flag; each worker's first call
// to M.Run reaches the os.Create in testing's m.before(), truncating the file
// the coordinator is writing, and several processes then append to one path at
// offsets of their own. cmd/go never combines the two either: `-test.fuzz` is
// not a cacheable test argument, so it disables the test cache and the flag is
// not passed at all.
//
// cmd/go's *other* companion flag, `-test.paniconexit0`, is deliberately not
// passed with this one. It makes a test that calls os.Exit(0) panic instead,
// which is how cmd/go stops such a test from cutting the log short — but it
// changes what the binary does, and this layer measures the program the user
// wrote. The consequence is stated rather than fixed: a target that exits by
// itself skips the deferred flush at the end of M.Run and leaves a log that
// stops wherever the last one did.
func planTestLog(record bool, scratch string, args []string) testLogPlan {
	plan := testLogPlan{record: record}
	switch {
	case !record:
		return plan
	case scratch == "":
		plan.reason = "go-mutants has no private directory to record the test log in"
		return plan
	case hasFuzzArgument(args):
		plan.reason = "go-mutants records no test log for a fuzz target: the coordinator's" +
			" workers are started with the same arguments and would each recreate the file"
		return plan
	}
	dir := filepath.Join(scratch, testLogDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		plan.reason = "the directory for the test log could not be created: " + err.Error()
		return plan
	}
	plan.dir = dir
	return plan
}

// recording reports whether this call really does hand its binaries the flag.
//
// It is the question every branch that acts on the flag has to ask, and it is
// not the same question as "did the request ask for a log": a fuzz target and a
// call with no scratch were asked and record nothing, and a branch that
// confused the two would act on a flag that was never on the command line.
func (p testLogPlan) recording() bool { return p.record && p.reason == "" }

// path is where the binary at index writes its log, or "" when this call
// records none.
//
// One file per binary, named by launch order. A shared path would be several
// processes appending to one log, and a log two binaries wrote cannot be
// attributed to either — the same rule the probe pass's own log follows, in
// the opposite direction and for the opposite reason.
func (p testLogPlan) path(index int) string {
	if !p.recording() {
		return ""
	}
	return filepath.Join(p.dir, "testlog-"+strconv.Itoa(index)+".txt")
}

// read is the record of what one binary touched.
//
// A path of "" is this call recording nothing, and the plan's reason is carried
// in its place. Every failure to read is a reason too: the point of the field
// is that a caller can tell "the target consulted nothing" from "nobody can say
// what the target consulted", and those two are one value apart.
//
// The result is here for one clause alone, and it is the clause that keeps
// [TestLog.Complete] honest: a log the supervisor interrupted is never
// complete, however tidily its bytes happen to end.
func (p testLogPlan) read(bin TestBinary, path string, result runner.Result) TestLog {
	record := TestLog{Package: bin.ImportPath, Dir: bin.Dir}
	if path == "" {
		record.Err = p.reason
		return record
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		record.Err = "the test binary wrote no test log"
		return record
	case err != nil:
		record.Err = "the test log could not be read: " + err.Error()
		return record
	case len(data) == 0:
		// The ordinary shape of a killed target, and worth its own sentence.
		// The testing package creates this file while it parses its flags and
		// buffers everything after it, flushing from the deferred call at the
		// end of M.Run — so a tree the supervisor tore down leaves the file it
		// made and nothing in it. "Malformed" would be true and useless.
		record.Err = "the test binary wrote nothing into its test log; it was killed or exited" +
			" before the testing package flushed one"
		return record
	}

	log, err := testlog.Parse(bytes.NewReader(data))
	if err != nil {
		record.Err = "the test log could not be read: " + err.Error()
		return record
	}
	record.Entries = log.Entries
	// The bytes are only half of the test. A 4096-byte buffer flushed mid-run
	// ends at a line boundary, so a chatty target the supervisor killed leaves
	// a log that parses as finished and holds a prefix of what it touched —
	// which is worse than no log, because it looks like an answer. The entries
	// are kept: they are what the target is *known* to have consulted.
	record.Complete = log.Complete && !supervisorEnded(result)
	return record
}

// supervisorEnded reports a child go-mutants took down rather than one that
// exited on its own: the budget expired, or the context was cancelled.
//
// internal/runner reports no exit status at all for a tree it killed, which is
// what [runner.ExitCodeUnavailable] means, and TimedOut says which of the two
// it was. Neither is a status the child chose, and a target that did not choose
// its own ending did not finish writing anything.
func supervisorEnded(result runner.Result) bool {
	return result.TimedOut || result.ExitCode == runner.ExitCodeUnavailable
}

// testLogUnsupported reports a binary that refused [testLogFlagName] rather
// than a suite that failed.
//
// Four conditions, and the first is the one that is easy to leave out. The
// status and the sentence are not go-mutants': a target that exits 2 having
// printed that line for its own reasons — a test asserting on the flag
// package's own diagnostics, a suite running a binary of its own — is a kill,
// and so is every exit 2 from a call that handed out no flag at all, which is
// what a fuzz target and a call with no scratch are. So the branch is asked
// only about a binary this call really did give the flag to.
//
// The rest keep a real test failure out of it. The flag package's refusal
// happens during flag parsing, before the log is opened, so a binary that
// refused has necessarily written nothing; a suite that printed the same
// sentence on its way to failing still wrote its log.
func testLogUnsupported(path string, result runner.Result, record TestLog) bool {
	return path != "" &&
		result.ExitCode == testLogUnsupportedExit &&
		strings.Contains(string(result.Output), testLogUndefined) &&
		!record.Complete && len(record.Entries) == 0
}

// testLogUnsupportedError is the failure a refused flag produces, named by the
// binary that refused it.
//
// It carries [testlog.ErrUnsupported] as its cause, which is the sentinel the
// public API republishes: a consumer that asked for a log and cannot have one
// needs to know that it is the *request* it has to change, and no code alone
// says which request.
func testLogUnsupportedError(what string, bin TestBinary, spec runner.Spec, result runner.Result) error {
	return &Error{
		Code: CodeTestLogUnsupported,
		Message: "the " + what + "'s test binary for " + bin.ImportPath + " does not accept " +
			testLogFlagName + ", so what the target touched cannot be recorded",
		Output:     tail(result.Output),
		Err:        testlog.ErrUnsupported,
		Invocation: runner.CommandOf(spec, result),
		Package:    bin.ImportPath,
	}
}

// hasFuzzArgument reports whether a target's arguments name a fuzz target, in
// which case the coordinator will start workers carrying every one of them.
func hasFuzzArgument(args []string) bool {
	return slices.ContainsFunc(args, func(argument string) bool {
		return testflag.Match(argument, "test.fuzz")
	})
}

// suppliesTestLog reports whether a target's arguments set the flag a recording
// call sets for itself.
//
// The rule is one function and the sentences are three, exactly as
// [overridesTimeout]'s are, and it is a second lock on a door the public
// session already locks: two of these flags are not two logs, since the
// standard flag package keeps the last value it sees, so a caller of this
// package that is not the session would otherwise compose a command line whose
// log the engine then read back as its own.
func suppliesTestLog(args []string) bool {
	return slices.ContainsFunc(args, func(argument string) bool {
		return testflag.Match(argument, "test.testlogfile")
	})
}
