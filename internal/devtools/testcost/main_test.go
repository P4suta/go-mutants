// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// stream renders a `go test -json` stream out of records, so that a test says
// what the run reported rather than what its JSON looks like.
//
// The fields are the ones cmd/go writes and this tool reads; everything else in
// a real record — the timestamps, the `run` and `output` actions carrying the
// tested program's own writing — is noise for every assertion below, and a
// fixture full of it would hide the one field the test is about.
func stream(records ...event) string {
	var b strings.Builder
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			panic(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// pass, skip and fail name one result each, so that a stream reads as the run
// it stands for.
func pass(pkg, test string, elapsed float64) event {
	return event{Action: "pass", Package: pkg, Test: test, Elapsed: elapsed}
}

func skip(pkg, test string) event {
	return event{Action: "skip", Package: pkg, Test: test}
}

func fail(pkg, test string, elapsed float64) event {
	return event{Action: "fail", Package: pkg, Test: test, Elapsed: elapsed}
}

// TestTableSumsElapsedPerPackage is the table's arithmetic, and the two
// counting rules that make the numbers mean something.
//
// A package's seconds are the sum of its top-level tests, not the wall clock of
// the binary and not the sum of every subtest as well. Both of the rejected
// readings are plausible and both are wrong: the package's own line goes to
// zero the moment a result comes out of the build cache, and counting a subtest
// beside its parent charges the same second twice — which would put whichever
// package happened to use `t.Run` the most at the top of a table nobody could
// then trust.
//
// The order is part of the contract as much as the figures are. Nobody reads a
// cost table alphabetically; the whole reason to print one is to see what to
// look at first.
func TestTableSumsElapsedPerPackage(t *testing.T) {
	t.Parallel()

	input := stream(
		pass("example.com/fast", "TestOne", 0.25),
		pass("example.com/fast", "TestTwo", 0.75),
		event{Action: "pass", Package: "example.com/fast", Elapsed: 1.1},
		pass("example.com/slow", "TestBig", 4),
		pass("example.com/slow", "TestBig/case", 3.5),
		skip("example.com/slow", "TestSkipped"),
		event{Action: "pass", Package: "example.com/slow", Elapsed: 4.2},
	)

	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader(input), &stdout, &stderr, map[string]string{}); code != exitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, exitOK, stderr.String())
	}

	got := stdout.String()
	for _, want := range []string{
		"| `example.com/slow` | 2 | 1 | 4.00s |",
		"| `example.com/fast` | 2 | 0 | 1.00s |",
		"| **2 packages** | **4** | **1** | **5.00s** |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the table has no row %q:\n%s", want, got)
		}
	}
	slow := strings.Index(got, "example.com/slow")
	fast := strings.Index(got, "example.com/fast")
	if slow < 0 || fast < 0 || slow > fast {
		t.Errorf("the table is not sorted by elapsed time, slowest first:\n%s", got)
	}
}

// TestAPackageWithNoTestFilesIsNotARow keeps the table about what ran.
//
// cmd/go reports a package holding no test files as a package-level `skip`, and
// a reader that treated it as one more row got a third of this repository's
// table filled with `0 | 0 | 0.00s` lines for `schema`, `cmd/go-mutants` and
// every other package that has nothing to run — which is noise in a table whose
// entire job is to say what to look at first. It is also not a skipped *test*,
// so it must not reach the skip list either, where it would be indistinguishable
// from a test that stopped running.
func TestAPackageWithNoTestFilesIsNotARow(t *testing.T) {
	t.Parallel()

	input := stream(
		event{Action: "skip", Package: "example.com/empty"},
		pass("example.com/pkg", "TestOne", 0.5),
		event{Action: "pass", Package: "example.com/pkg", Elapsed: 0.6},
	)

	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader(input), &stdout, &stderr, map[string]string{}); code != exitOK {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitOK, stderr.String())
	}
	if strings.Contains(stdout.String(), "example.com/empty") {
		t.Errorf("a package with no test files became a row:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "| **1 packages** | **1** | **0** | **0.50s** |") {
		t.Errorf("the total counts a package that ran nothing:\n%s", stdout.String())
	}
	// And --no-skips must not fail on one: a package with nothing to run is not
	// a test that stopped running.
	var quiet bytes.Buffer
	if code := run([]string{"--no-skips"}, strings.NewReader(input), &quiet, &quiet, map[string]string{}); code != exitOK {
		t.Errorf("--no-skips failed on a package with no test files:\n%s", quiet.String())
	}
}

// out renders one `output` record, which is how cmd/go carries every line the
// test binary wrote.
func out(pkg, test, line string) event {
	return event{Action: "output", Package: pkg, Test: test, Output: line}
}

// TestAFailingTestsOutputReachesTheLog is the whole reason a red CI run is
// diagnosable.
//
// cmd/go runs the binary with -test.v under -json, so every line a failing test
// wrote is in the stream — the `=== RUN`, the `t.Errorf` with its file and
// line, the `--- FAIL` — and a reader that consumed the output records and
// printed only a count turned that into "something failed". A log that names a
// test but shows nothing it said is a log that sends somebody back to the
// runner they cannot reach.
func TestAFailingTestsOutputReachesTheLog(t *testing.T) {
	t.Parallel()

	input := stream(
		out("example.com/pkg", "TestBroken", "=== RUN   TestBroken\n"),
		out("example.com/pkg", "TestBroken", "    thing_test.go:42: got 1, want 2\n"),
		out("example.com/pkg", "TestBroken", "--- FAIL: TestBroken (0.10s)\n"),
		fail("example.com/pkg", "TestBroken", 0.1),
		event{Action: "fail", Package: "example.com/pkg", Elapsed: 0.2},
	)

	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader(input), &stdout, &stderr, map[string]string{}); code != exitFailure {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitFailure, stderr.String())
	}
	for _, want := range []string{
		"--- FAIL: example.com/pkg.TestBroken",
		"thing_test.go:42: got 1, want 2",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the log does not carry %q:\n%s", want, stderr.String())
		}
	}
}

// TestAPackageLevelFailuresOutputReachesTheLog covers the shape that carries no
// test name at all: a panic that took the binary down, a TestMain that exited
// non-zero, a suite that ran out of time. Whatever cmd/go could not attribute to
// a test is attributed to the package, and it is the only evidence there is.
func TestAPackageLevelFailuresOutputReachesTheLog(t *testing.T) {
	t.Parallel()

	input := stream(
		out("example.com/pkg", "", "panic: test timed out after 40m0s\n"),
		out("example.com/pkg", "", "goroutine 1 [running]:\n"),
		event{Action: "fail", Package: "example.com/pkg", Elapsed: 2400},
	)

	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader(input), &stdout, &stderr, map[string]string{}); code != exitFailure {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitFailure, stderr.String())
	}
	for _, want := range []string{
		"--- FAIL: example.com/pkg [package failed]",
		"panic: test timed out after 40m0s",
		"goroutine 1 [running]:",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the log does not carry %q:\n%s", want, stderr.String())
		}
	}
}

// TestABuildFailuresDiagnosticsReachTheLog is the third shape, and the one
// with no package result behind it at all: cmd/go reports what the compiler
// said as `build-output` against an import path, and those lines are the entire
// explanation of why nothing ran.
func TestABuildFailuresDiagnosticsReachTheLog(t *testing.T) {
	t.Parallel()

	input := stream(
		event{Action: "build-output", ImportPath: "example.com/pkg", Output: "./a.go:7:2: undefined: nope\n"},
		event{Action: "build-fail", ImportPath: "example.com/pkg"},
	)

	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader(input), &stdout, &stderr, map[string]string{}); code != exitFailure {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitFailure, stderr.String())
	}
	if !strings.Contains(stderr.String(), "./a.go:7:2: undefined: nope") {
		t.Errorf("the log does not carry the compiler's diagnostic:\n%s", stderr.String())
	}
}

// TestAPassingTestsOutputIsDiscarded is the other half of the rule, and the one
// that keeps this usable.
//
// Under -json every test's output is in the stream whether it passed or not, so
// replaying all of it would turn a green CI log into a `go test -v` transcript
// of a thousand tests — which is the log nobody reads, and the reason this tool
// prints a table in the first place. --verbose is there for the run where
// somebody wants it.
func TestAPassingTestsOutputIsDiscarded(t *testing.T) {
	t.Parallel()

	input := stream(
		out("example.com/pkg", "TestQuiet", "=== RUN   TestQuiet\n"),
		out("example.com/pkg", "TestQuiet", "    thing_test.go:9: a chatty but successful test\n"),
		pass("example.com/pkg", "TestQuiet", 0.1),
		event{Action: "pass", Package: "example.com/pkg", Elapsed: 0.2},
	)

	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader(input), &stdout, &stderr, map[string]string{}); code != exitOK {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitOK, stderr.String())
	}
	if strings.Contains(stderr.String(), "a chatty but successful test") {
		t.Errorf("a passing test's output was replayed:\n%s", stderr.String())
	}

	var verboseOut, verboseErr bytes.Buffer
	if code := run([]string{"--verbose"}, strings.NewReader(input), &verboseOut, &verboseErr, map[string]string{}); code != exitOK {
		t.Fatalf("--verbose: exit = %d, want %d:\n%s", code, exitOK, verboseErr.String())
	}
	if !strings.Contains(verboseErr.String(), "a chatty but successful test") {
		t.Errorf("--verbose withheld a passing test's output:\n%s", verboseErr.String())
	}
}

// TestOutputIsBoundedAsItArrivesNotOnlyAtReplay is the memory bound, asserted
// where it has to hold.
//
// Trimming at replay bounds the log and nothing else: every line a test writes
// is retained until its result arrives, so a test that logs in a loop — which is
// how a test that is going wrong usually behaves — grows this process without
// limit and can kill the wrapper before it renders anything at all. The cap
// therefore applies as each line is buffered, and what a failure carries is
// already bounded by the time it is filed.
func TestOutputIsBoundedAsItArrivesNotOnlyAtReplay(t *testing.T) {
	t.Parallel()

	chatty := strings.Repeat("x", 4096) + "\n"
	records := []event{}
	for range 2 * (maxRetainedBytes / len(chatty)) {
		records = append(records, out("example.com/pkg", "TestLoud", chatty))
	}
	records = append(records, fail("example.com/pkg", "TestLoud", 1))

	measured := read(strings.NewReader(stream(records...)), false)
	if len(measured.failures) != 1 {
		t.Fatalf("the run reported %d failures, want the one: %+v", len(measured.failures), measured.failures)
	}
	failed := measured.failures[0]
	retained := 0
	for _, line := range failed.output {
		retained += len(line)
	}
	if retained > maxRetainedBytes {
		t.Errorf("the failure retained %d bytes, want at most %d: the cap is only being applied at replay",
			retained, maxRetainedBytes)
	}
	if failed.elided == 0 {
		t.Errorf("nothing was recorded as elided, so a reader is not told the output is incomplete")
	}
}

// TestVerboseReplaysEachBufferExactlyOnce is the routing rule.
//
// There are two places a buffer can be filed and three kinds of result, and
// getting that wrong is invisible in the ordinary run: under --verbose a failing
// test's output went to both the failure list and the verbose one, so it was
// printed twice; a skipped test's went to neither and was never printed at all,
// while its buffer stayed in the map for the rest of the run. Every terminal
// event now files its buffer exactly once and drops it either way.
func TestVerboseReplaysEachBufferExactlyOnce(t *testing.T) {
	t.Parallel()

	input := stream(
		out("example.com/pkg", "TestBroken", "the failing line\n"),
		fail("example.com/pkg", "TestBroken", 0.1),
		out("example.com/pkg", "TestQuiet", "the passing line\n"),
		pass("example.com/pkg", "TestQuiet", 0.1),
		out("example.com/pkg", "TestSkipped", "the skipped line\n"),
		skip("example.com/pkg", "TestSkipped"),
		out("example.com/pkg", "", "the package line\n"),
		event{Action: "fail", Package: "example.com/pkg", Elapsed: 0.4},
	)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--verbose"}, strings.NewReader(input), &stdout, &stderr, map[string]string{}); code != exitFailure {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitFailure, stderr.String())
	}
	for _, line := range []string{
		"the failing line",
		"the passing line",
		"the skipped line",
		"the package line",
	} {
		if got := strings.Count(stderr.String(), line); got != 1 {
			t.Errorf("%q appears %d times under --verbose, want exactly once:\n%s", line, got, stderr.String())
		}
	}
}

// TestNoSkipsFlagFailsOnASkipAction is the flag the whole skip column exists
// for: a run that reported a skip is a run that measured less than it looks
// like it did, and --no-skips is how a caller says it wanted all of it.
//
// The same stream without the flag has to succeed, because a skip is not a
// failure — it is a fact the table reports — and a tool that gated on one
// unasked could not be used as the ordinary formatter it mostly is.
func TestNoSkipsFlagFailsOnASkipAction(t *testing.T) {
	t.Parallel()

	input := stream(
		pass("example.com/pkg", "TestRan", 0.1),
		skip("example.com/pkg", "TestDidNot"),
		skip("example.com/pkg", "TestTable/unsupported"),
		event{Action: "pass", Package: "example.com/pkg", Elapsed: 0.2},
	)

	var quiet bytes.Buffer
	if code := run(nil, strings.NewReader(input), &quiet, &quiet, map[string]string{}); code != exitOK {
		t.Fatalf("without --no-skips: exit = %d, want %d:\n%s", code, exitOK, quiet.String())
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--no-skips"}, strings.NewReader(input), &stdout, &stderr, map[string]string{})
	if code != exitFailure {
		t.Fatalf("with --no-skips: exit = %d, want %d:\n%s", code, exitFailure, stderr.String())
	}
	for _, want := range []string{"example.com/pkg.TestDidNot", "example.com/pkg.TestTable/unsupported"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the refusal does not name %s:\n%s", want, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "TestRan") {
		t.Errorf("the refusal names a test that ran:\n%s", stderr.String())
	}
}

// TestTableGoesToTheStepSummaryWhenSet is the routing rule.
//
// A table printed on stdout in CI is a line in a log nobody opens; the point of
// the summary file is that GitHub renders it on the run's own page. The append
// is the other half: a job has several steps and any of them may write there, so
// a table that truncated would delete whatever ran before it.
func TestTableGoesToTheStepSummaryWhenSet(t *testing.T) {
	t.Parallel()

	summary := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(summary, []byte("written by an earlier step\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := stream(
		pass("example.com/pkg", "TestOne", 1.5),
		event{Action: "pass", Package: "example.com/pkg", Elapsed: 1.6},
	)

	var stdout, stderr bytes.Buffer
	env := map[string]string{summaryEnv: summary}
	if code := run(nil, strings.NewReader(input), &stdout, &stderr, env); code != exitOK {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitOK, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("the table was also written to stdout: %q", stdout.String())
	}
	written, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(written), "written by an earlier step\n") {
		t.Errorf("the summary file was truncated rather than appended to:\n%s", written)
	}
	if !strings.Contains(string(written), "| `example.com/pkg` | 1 | 0 | 1.50s |") {
		t.Errorf("the summary file has no table row:\n%s", written)
	}
}

// TestAFailingRunExitsNonZero is what keeps this tool from being the hole in
// every task that runs through it.
//
// `go test -json ./... | testcost` is a pipeline, and a shell reports the last
// command's status — so a formatter that always succeeded would turn every red
// suite green, silently, in exactly the tasks CI runs. Both shapes of failure
// are reported: a test that failed, and a package that failed without any test
// failing (a TestMain that exited non-zero, a panic outside a test).
func TestAFailingRunExitsNonZero(t *testing.T) {
	t.Parallel()

	input := stream(
		fail("example.com/pkg", "TestBroken", 0.1),
		event{Action: "fail", Package: "example.com/pkg", Elapsed: 0.2},
		event{Action: "fail", Package: "example.com/other", Elapsed: 0.3},
	)

	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader(input), &stdout, &stderr, map[string]string{}); code != exitFailure {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitFailure, stderr.String())
	}
	for _, want := range []string{
		"example.com/pkg.TestBroken",
		"example.com/pkg [package failed]",
		"example.com/other [package failed]",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the report does not name %q:\n%s", want, stderr.String())
		}
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the table does not name %q:\n%s", want, stdout.String())
		}
	}
}

// TestABuildFailureIsAFailure covers the run that produced no test results at
// all.
//
// cmd/go reports a package that did not compile as `build-fail` against an
// ImportPath, before any package result exists — so a reader that only knew
// about `Package` would see an empty stream, print an empty table and exit
// zero, which is the most confident way possible of saying nothing happened.
func TestABuildFailureIsAFailure(t *testing.T) {
	t.Parallel()

	input := stream(event{Action: "build-fail", ImportPath: "example.com/pkg [example.com/pkg.test]"})

	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader(input), &stdout, &stderr, map[string]string{}); code != exitFailure {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitFailure, stderr.String())
	}
	if !strings.Contains(stderr.String(), "example.com/pkg [example.com/pkg.test] [build failed]") {
		t.Errorf("the report does not name the package that did not build:\n%s", stderr.String())
	}
}

// TestNonJSONLinesAreIgnored keeps a legible toolchain error legible.
//
// A `go` command that fails before it starts testing — a module it cannot
// resolve, a toolchain it cannot download — writes plain text, and a reader
// that treated the first such line as a parse error would replace the message
// explaining what went wrong with one about JSON.
func TestNonJSONLinesAreIgnored(t *testing.T) {
	t.Parallel()

	input := "go: downloading example.com/dep v1.2.3\n" +
		stream(pass("example.com/pkg", "TestOne", 0.5)) +
		"\n"

	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader(input), &stdout, &stderr, map[string]string{}); code != exitOK {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "| `example.com/pkg` | 1 | 0 | 0.50s |") {
		t.Errorf("the table lost the record that followed the plain-text line:\n%s", stdout.String())
	}
}

// TestAMalformedRecordIsReportedAndTheRestStillRenders is the other half of the
// rule above, and both halves of it matter.
//
// A line that announces itself as JSON and is not one is a defect in whatever
// produced the stream, so it is named and it fails the run — silently dropping
// it would make the table quietly incomplete. But it must not *cost* the table:
// the reason anybody is reading this output is usually that something went
// wrong, and answering "one line was malformed" instead of showing the forty
// packages that parsed fine is the least useful moment to start withholding
// results. So the scan carries on, the table renders from what parsed, and the
// malformed lines are reported underneath it.
func TestAMalformedRecordIsReportedAndTheRestStillRenders(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	input := stream(pass("example.com/before", "TestEarly", 0.25)) +
		"{\"Action\":\"pass\",\n" +
		stream(pass("example.com/after", "TestLate", 0.75))
	if code := run(nil, strings.NewReader(input), &stdout, &stderr, map[string]string{}); code != exitFailure {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitFailure, stderr.String())
	}
	if !strings.Contains(stderr.String(), `{\"Action\":\"pass\",`) {
		t.Errorf("the error does not quote the line it could not read:\n%s", stderr.String())
	}
	for _, want := range []string{
		"| `example.com/after` | 1 | 0 | 0.75s |",
		"| `example.com/before` | 1 | 0 | 0.25s |",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("one malformed line cost the table the row %q:\n%s", want, stdout.String())
		}
	}
}

// TestAnEmptyRunSaysSoRatherThanPrintingAnEmptyTable is the case a reader
// misreads fastest: a header with no rows under it looks like a table that has
// not finished loading, and the run it stands for tested nothing.
func TestAnEmptyRunSaysSoRatherThanPrintingAnEmptyTable(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader(""), &stdout, &stderr, map[string]string{}); code != exitOK {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No packages were tested.") {
		t.Errorf("an empty run printed:\n%s", stdout.String())
	}
}

// TestAnArgumentIsAUsageError catches the shape of the mistake this tool is
// most likely to be given: `testcost ./...`, by somebody who expected it to run
// the suite rather than read one.
//
// The `--` is what separates a command from a mistake. Without it an argument
// is a usage error, so the tool can never quietly execute something it was
// merely handed.
func TestAnArgumentIsAUsageError(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"./..."}, strings.NewReader(""), &stdout, &stderr, map[string]string{}); code != exitUsage {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--") {
		t.Errorf("the refusal does not say how to give it a command:\n%s", stderr.String())
	}
}

// helperModeEnv switches the child process below on, and carries the exit
// status it should end with.
const helperModeEnv = "TESTCOST_TEST_HELPER_EXIT"

// TestCostHelperProcess is not a test.
//
// It is the child the `--` tests run: it writes a `go test -json` stream on
// stdout, a line of plain text on stderr, and ends with the status the test
// asked for. Without the guard it skips, because in an ordinary run of this
// package it is only ever reached by the framework enumerating tests.
func TestCostHelperProcess(t *testing.T) {
	status := os.Getenv(helperModeEnv)
	if status == "" {
		t.Skipf("this is the child process the `--` tests re-execute, and it does nothing unless %s is set", helperModeEnv)
	}
	code, err := strconv.Atoi(status)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: %s=%q is not a status\n", helperModeEnv, status)
		os.Exit(99)
	}
	_, _ = fmt.Fprint(os.Stdout, stream(
		pass("example.com/child", "TestRan", 1.5),
		event{Action: "pass", Package: "example.com/child", Elapsed: 1.6},
	))
	fmt.Fprintln(os.Stderr, "go: a plain-text diagnostic nothing in the JSON mentions")
	os.Exit(code)
}

// helperArgv re-executes this test binary as [TestCostHelperProcess].
func helperArgv(t *testing.T) []string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("finding this test binary: %v", err)
	}
	return []string{binary, "-test.run=^TestCostHelperProcess$"}
}

// TestTheChildStatusSurvivesTheReport is the hole a pipeline leaves, closed.
//
// `go test -json ./... | testcost` reports the *last* command's status in every
// shell there is, and the failures this tool reads are the ones cmd/go wrote
// into the stream. A `go` command that fell over before it started testing —
// a module it cannot resolve, a toolchain it cannot download — writes plain
// text on stderr and nothing at all on stdout, so the table is empty, the
// verdict is "nothing failed", and a green task reports a run that never
// happened. Running the command instead of being piped its output is what makes
// that impossible: the child's status is read rather than discarded.
func TestTheChildStatusSurvivesTheReport(t *testing.T) {
	argv := append([]string{"--"}, helperArgv(t)...)

	t.Run("a failing child fails the report", func(t *testing.T) {
		t.Setenv(helperModeEnv, "3")
		var stdout, stderr bytes.Buffer
		if code := run(argv, strings.NewReader(""), &stdout, &stderr, map[string]string{}); code != exitFailure {
			t.Fatalf("exit = %d, want %d:\n%s", code, exitFailure, stderr.String())
		}
		if !strings.Contains(stderr.String(), "exited with status 3") {
			t.Errorf("the report does not say what the child did:\n%s", stderr.String())
		}
		// And the table is still rendered from what the child managed to say.
		if !strings.Contains(stdout.String(), "| `example.com/child` | 1 | 0 | 1.50s |") {
			t.Errorf("a failing child cost the table its rows:\n%s", stdout.String())
		}
	})

	t.Run("a passing child passes", func(t *testing.T) {
		t.Setenv(helperModeEnv, "0")
		var stdout, stderr bytes.Buffer
		if code := run(argv, strings.NewReader(""), &stdout, &stderr, map[string]string{}); code != exitOK {
			t.Fatalf("exit = %d, want %d:\n%s", code, exitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "| `example.com/child` | 1 | 0 | 1.50s |") {
			t.Errorf("the table is missing the child's run:\n%s", stdout.String())
		}
	})
}

// TestTheReportVerdictSurvivesAPassingChild is the same seam in the other
// direction: a suite that exited zero while the stream carried a failure.
//
// It is not hypothetical — a `go test` whose TestMain swallows a status, or a
// build failure cmd/go reports without failing the command, both look like
// this — and a wrapper that only forwarded the child's status would report the
// run as green while printing the failure in its own table.
func TestTheReportVerdictSurvivesAPassingChild(t *testing.T) {
	t.Parallel()

	measured := results{failures: []failure{{name: "example.com/pkg.TestBroken"}}}
	if got := verdict(measured, 0, false); got != exitFailure {
		t.Errorf("verdict with a failure in the stream and a zero child = %d, want %d", got, exitFailure)
	}
	if got := verdict(results{}, 0, false); got != exitOK {
		t.Errorf("verdict with nothing wrong = %d, want %d", got, exitOK)
	}
}
