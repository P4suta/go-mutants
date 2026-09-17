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

const testLogFlagName = "-test.testlogfile"

const testLogUndefined = "flag provided but not defined: " + testLogFlagName

const testLogUnsupportedExit = 2

const testLogDirName = "testlogs"

type TestLog struct {
	Package  string
	Dir      string
	Entries  []testlog.Entry
	Complete bool
	Err      string
}

type testLogPlan struct {
	record bool
	dir    string
	reason string
}

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

func (p testLogPlan) recording() bool { return p.record && p.reason == "" }

func (p testLogPlan) path(index int) string {
	if !p.recording() {
		return ""
	}
	return filepath.Join(p.dir, "testlog-"+strconv.Itoa(index)+".txt")
}

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
	record.Complete = log.Complete && !supervisorEnded(result)
	return record
}

func supervisorEnded(result runner.Result) bool {
	return result.TimedOut || result.ExitCode == runner.ExitCodeUnavailable
}

func testLogUnsupported(path string, result runner.Result, record TestLog) bool {
	return path != "" &&
		result.ExitCode == testLogUnsupportedExit &&
		strings.Contains(string(result.Output), testLogUndefined) &&
		!record.Complete && len(record.Entries) == 0
}

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

func hasFuzzArgument(args []string) bool {
	return slices.ContainsFunc(args, func(argument string) bool {
		return testflag.Match(argument, "test.fuzz")
	})
}

func suppliesTestLog(args []string) bool {
	return slices.ContainsFunc(args, func(argument string) bool {
		return testflag.Match(argument, "test.testlogfile")
	})
}
