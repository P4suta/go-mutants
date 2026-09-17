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

func pass(pkg, test string, elapsed float64) event {
	return event{Action: "pass", Package: pkg, Test: test, Elapsed: elapsed}
}

func skip(pkg, test string) event {
	return event{Action: "skip", Package: pkg, Test: test}
}

func fail(pkg, test string, elapsed float64) event {
	return event{Action: "fail", Package: pkg, Test: test, Elapsed: elapsed}
}

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
	var quiet bytes.Buffer
	if code := run([]string{"--no-skips"}, strings.NewReader(input), &quiet, &quiet, map[string]string{}); code != exitOK {
		t.Errorf("--no-skips failed on a package with no test files:\n%s", quiet.String())
	}
}

func out(pkg, test, line string) event {
	return event{Action: "output", Package: pkg, Test: test, Output: line}
}

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

const helperModeEnv = "TESTCOST_TEST_HELPER_EXIT"

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

func helperArgv(t *testing.T) []string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("finding this test binary: %v", err)
	}
	return []string{binary, "-test.run=^TestCostHelperProcess$"}
}

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
