// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// selfHelperEnv switches this test binary into the helper program below, and
// argvTargetEnv gates the *test function* [HelperArgv] re-executes. They are two
// different shapes and this file is the only place both are exercised, so they
// are two different variables: a binary that answered both would prove neither.
//
// Neither begins with GO_MUTANTS_, because the environment policy strips that
// whole prefix from every child it composes.
const (
	selfHelperEnv = "TESTKIT_SELF_HELPER"
	argvTargetEnv = "TESTKIT_ARGV_TARGET"
)

// helperMisuseStatus is what the program below exits with when it does not
// understand its own arguments. It is distinct from every status a test here
// asks for, so a misuse can never be read as a pass.
const helperMisuseStatus = 97

// TestMain is the [Helper] shape this package both offers and uses, with the
// guard that no test in it files evidence in the developer's own kept root.
//
// The guard needs a package-level hook and there is nowhere else to put one. A
// test can only see what it did itself, and the failure being prevented is a
// test *elsewhere in the package* leaving a directory in
// `<os.UserCacheDir()>/go-mutants-test/kept` — which is where a real failed run
// files its evidence, and which CI does not upload because the job names a root
// of its own. TestKeptRootAgreesWithTestkit did exactly that: it cleared the
// override to ask what the default was and then took a scratch under it.
//
// A root that was already there is left alone and the guard skips: it is the
// developer's, it may hold evidence they are reading, and this is not the
// process that gets to decide it is stale.
func TestMain(m *testing.M) {
	os.Exit(guardTheDefaultKeptRoot(func() int {
		return Helper(m, selfHelperEnv, selfHelperProgram)
	}))
}

// guardTheDefaultKeptRoot runs the suite and fails it if the default kept root
// appeared while it ran and had no business appearing.
//
// It has business appearing in exactly one case: somebody ran the suite with the
// policy on and named no root of their own, which is the documented way to keep
// things locally. Every other case is the defect — a run with the policy off
// must leave the default root untouched, and a run that named a root must file
// everything under it, or CI uploads an empty directory while the evidence sits
// somewhere on the runner that dies with it.
func guardTheDefaultKeptRoot(run func() int) int {
	if pinned.userCache == "" {
		return run()
	}
	root := filepath.Join(pinned.userCache, harnessDirName, keptDirName)
	if _, err := os.Lstat(root); err == nil {
		// Already the developer's. Nothing here may judge it.
		return run()
	}
	allowed := KeepPolicy() != KeepNever && os.Getenv(KeepDirEnv) == ""
	code := run()
	if _, err := os.Lstat(root); err == nil && !allowed {
		fmt.Fprintf(os.Stderr, "\nthis suite created %s, which is the developer's own kept root and "+
			"the one CI does not upload: a test that wants the keep policy on has to point %s at a "+
			"directory of its own\n", root, KeepDirEnv)
		if code == 0 {
			return 1
		}
	}
	return code
}

// selfHelperProgram is the whole helper program: a verb and its arguments,
// writing exactly what it was told to and nothing else.
func selfHelperProgram(args []string) int {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "helper: bad invocation %q\n", args)
		return helperMisuseStatus
	}
	switch verb, rest := args[0], args[1:]; verb {
	case "print":
		// print TEXT — written with no trailing newline, so a test can assert
		// on the exact bytes.
		_, _ = fmt.Fprint(os.Stdout, rest[0])
		return 0
	case "env":
		// env NAME — prints the value the child sees, empty when unset.
		_, _ = fmt.Fprint(os.Stdout, os.Getenv(rest[0]))
		return 0
	case "exit":
		code, err := strconv.Atoi(rest[0])
		if err != nil {
			return helperMisuseStatus
		}
		return code
	default:
		fmt.Fprintf(os.Stderr, "helper: unknown verb %q\n", verb)
		return helperMisuseStatus
	}
}

// TestHelperArgvTargetProcess is not a test: it is what
// [TestHelperArgvReexecutesOnlyTheNamedTest] re-executes. It returns silently
// when it was not asked for, so an ordinary run neither runs it nor reports it
// as skipped.
func TestHelperArgvTargetProcess(t *testing.T) {
	if !HelperEnabled(argvTargetEnv) {
		return
	}
	t.Log("the named test ran")
	_, _ = fmt.Fprintln(os.Stdout, "target-process-marker")
}

// TestHelperArgvNeighbourProcess is the test the run pattern must not select.
// It exists only so that "only the named test" is an assertion rather than a
// hope.
func TestHelperArgvNeighbourProcess(t *testing.T) {
	if !HelperEnabled(argvTargetEnv) {
		return
	}
	_, _ = fmt.Fprintln(os.Stdout, "neighbour-process-marker")
}

// TestHelperArgvReexecutesOnlyTheNamedTest pins the argv, and then proves the
// pattern does what the string suggests.
//
// The anchors are the whole of it. `-test.run=TestHelperArgvTargetProcess`
// without them is a regular expression that also selects
// TestHelperArgvTargetProcessAndMore, and a helper that ran two tests writes two
// tests' output into the stream a caller is parsing.
func TestHelperArgvReexecutesOnlyTheNamedTest(t *testing.T) {
	t.Parallel()

	argv := HelperArgv("TestHelperArgvTargetProcess")
	want := []string{TestBinary(), "-test.run=^TestHelperArgvTargetProcess$"}
	if !slices.Equal(argv, want) {
		t.Fatalf("HelperArgv = %q, want %q", argv, want)
	}
	if !filepath.IsAbs(argv[0]) {
		t.Errorf("HelperArgv starts with %q, which a child that runs in another directory cannot resolve", argv[0])
	}

	env := withEntries(Compose(t, t.TempDir()), argvTargetEnv+"=1")
	result := Exec(t, t.TempDir(), env, argv...)

	RequireExit(t, result, 0, "the re-executed test")
	RequireOutput(t, result, "the re-executed test", "target-process-marker")
	RequireNoOutput(t, result, "the re-executed test", "neighbour-process-marker")
}

// TestHelperRunsTheProgramWithByteExactStdout is why the program runs from
// TestMain rather than from a test function.
//
// internal/runner's subject is what a child printed, byte for byte: the tests
// there assert that a truncated stream keeps its tail, that a helper's stdout
// and stderr interleave the way the child wrote them, and that nothing was
// added. A helper written as a test function cannot promise that — the testing
// package writes "PASS" and a timing line after it, and under -test.v a "=== RUN"
// before it — and it cannot exit with a chosen status either.
func TestHelperRunsTheProgramWithByteExactStdout(t *testing.T) {
	t.Parallel()

	const payload = "exactly these bytes and no newline"
	env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1")
	result := Exec(t, t.TempDir(), env, os.Args[0], "print", payload)

	RequireExit(t, result, 0, "the helper program")
	if got := string(result.Stdout); got != payload {
		t.Errorf("the helper's stdout is %q, want exactly %q", got, payload)
	}
}

// TestHelperForwardsTheProgramsExitStatus is the other half of the same reason:
// a mutation runner's whole subject is a non-zero status, so the helper has to
// be able to produce an arbitrary one.
func TestHelperForwardsTheProgramsExitStatus(t *testing.T) {
	t.Parallel()

	env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1")
	result := Exec(t, t.TempDir(), env, os.Args[0], "exit", "3")

	if result.Err != nil {
		t.Fatalf("the helper could not be run: %v\n%s", result.Err, result.Output)
	}
	if result.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want the status the program returned:\n%s", result.ExitCode, result.Output)
	}
}

// TestHelperIsolatesCoverageOutputPerProcess is the rule that took a day to
// find the first time.
//
// A helper is this very test binary re-executed, so under `go test -cover` it is
// coverage-instrumented — and because it exits from TestMain without ever
// reaching the testing package's "the profile is already written" call, the
// coverage runtime's exit hook fires. Left alone that hook writes
// covmeta.<hash> into the single GOCOVERDIR `go test` exports, under a name
// derived from the binary and therefore identical for every helper; the
// concurrent atomic renames collide, and on Windows the loser prints "error:
// coverage meta-data emit failed: ... Access is denied" onto the very stderr a
// test is asserting the exact bytes of. Unsetting the variable is not the fix —
// measured, the hook then prints "warning: GOCOVERDIR not set, no coverage data
// emitted" to the same place. A private directory per process is the only quiet
// answer.
func TestHelperIsolatesCoverageOutputPerProcess(t *testing.T) {
	t.Parallel()

	root := HelperCoverRoot()
	if root == "" {
		t.Fatal("the parent published no helper coverage root, so no helper has anywhere private to write")
	}

	env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1")
	var seen []string
	for range 2 {
		result := Exec(t, t.TempDir(), env, os.Args[0], "env", CoverDirEnv)
		RequireExit(t, result, 0, "the helper program")
		seen = append(seen, string(result.Stdout))
	}

	for _, dir := range seen {
		if !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			t.Errorf("a helper's %s is %q, want a directory under the private root %s", CoverDirEnv, dir, root)
		}
		if _, err := strconv.Atoi(filepath.Base(dir)); err != nil {
			t.Errorf("a helper's %s is %q, want one named by the process id", CoverDirEnv, dir)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("the helper's coverage directory was not created: %v", err)
		}
	}
	if seen[0] == seen[1] {
		t.Errorf("two helper processes shared %s = %q, which is the collision this exists to prevent",
			CoverDirEnv, seen[0])
	}
}

// TestTestBinaryIsAlwaysAnAbsolutePath is the promise the argv makes to a child
// that starts somewhere else.
//
// A child is run in a directory the parent chose — a snapshot, a fixture copy, a
// scratch tree — and a relative argv[0] is resolved by the *child's* working
// directory, not ours. os.Executable answers absolutely, so the ordinary path is
// safe; the fallback is the one that is not. os.Args[0] is whatever the parent
// passed, and `go test -exec` wrappers, `dlv test` and a hand-built binary run
// as `./pkg.test` all pass a relative one — so the fallback resolves it before
// handing it out, and only a filesystem that cannot answer at all gets the raw
// value.
func TestTestBinaryIsAlwaysAnAbsolutePath(t *testing.T) {
	t.Parallel()

	if got := testBinaryFrom("/opt/bin/pkg.test", nil, "ignored"); got != "/opt/bin/pkg.test" {
		t.Errorf("testBinaryFrom with a usable os.Executable = %q, want it used unchanged", got)
	}

	failed := errors.New("os.Executable is not supported here")
	relative := filepath.Join(".", "pkg.test")
	got := testBinaryFrom("", failed, relative)
	if !filepath.IsAbs(got) {
		t.Errorf("testBinaryFrom fell back to %q, which a child in another directory cannot resolve", got)
	}
	if filepath.Base(got) != "pkg.test" {
		t.Errorf("testBinaryFrom fell back to %q, which no longer names the test binary", got)
	}

	absolute := filepath.Join(t.TempDir(), "pkg.test")
	if fellBack := testBinaryFrom("", failed, absolute); fellBack != absolute {
		t.Errorf("testBinaryFrom rewrote an already absolute argv[0]: %q, want %q", fellBack, absolute)
	}
}

// TestHelperArgvEscapesTheTestName keeps `-test.run` a pattern that names one
// test.
//
// The argument is a regular expression, and a Go test name is not one: a name
// with a `.` in it matches any character, and one with a `+` or a `(` in it is
// either a different pattern or not a pattern at all. Names generated from a
// version, a path or a fixture have those characters, so the escape is what
// makes the anchors mean what they look like they mean.
func TestHelperArgvEscapesTheTestName(t *testing.T) {
	t.Parallel()

	argv := HelperArgv("TestVersion1.2+build/sub(case)")
	want := "-test.run=^" + regexp.QuoteMeta("TestVersion1.2+build/sub(case)") + "$"
	if argv[1] != want {
		t.Errorf("HelperArgv run pattern = %q, want %q", argv[1], want)
	}
	pattern, err := regexp.Compile(strings.TrimPrefix(argv[1], "-test.run="))
	if err != nil {
		t.Fatalf("the run pattern is not a valid regular expression: %v", err)
	}
	if !pattern.MatchString("TestVersion1.2+build/sub(case)") {
		t.Error("the run pattern does not match the test it names")
	}
	if pattern.MatchString("TestVersion1x2+build/sub(case)") {
		t.Error("the run pattern matches a name the `.` was never meant to stand for")
	}
}

// TestHelperEnabledReadsANonEmptyValue states the convention: the variable is a
// switch, so `=0` still means "you are the helper". Every helper in this
// repository is started by code that sets the variable on purpose, and a helper
// that decided the value looked falsy and ran the suite instead would look like
// a test that passed. An empty value reads as false, because that is what an
// inherited-but-cleared variable looks like.
func TestHelperEnabledReadsANonEmptyValue(t *testing.T) {
	// Not parallel: t.Setenv is the subject.
	t.Setenv(argvTargetEnv, "0")
	if !HelperEnabled(argvTargetEnv) {
		t.Error("HelperEnabled is false for a variable that is set to 0")
	}
	t.Setenv(argvTargetEnv, "")
	if HelperEnabled(argvTargetEnv) {
		t.Error("HelperEnabled is true for a variable set to the empty string")
	}
}
