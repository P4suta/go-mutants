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

// The two switches [TestHelperCoverRootTargetProcess] answers to.
//
// coverRootMarker prefixes the one line it writes, so that the parent reads a
// value rather than a stream — the testing package writes a "PASS" of its own
// after it. argvTargetExitEnv makes the child leave the way a killed one does:
// os.Exit runs no deferred function, which is exactly what a SIGKILL, a
// `go test -timeout` and a mutation run's budget have in common, and modelling
// it with a status rather than a signal keeps the test instant and identical on
// every platform.
const (
	coverRootMarker   = "cover-root="
	argvTargetExitEnv = "TESTKIT_ARGV_TARGET_EXIT"
)

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

// TestHelperCoverRootTargetProcess is not a test either: it is the child
// [TestHelperLeavesNoCoverageDirectoryBehind] re-executes.
//
// It reports the coverage root its own TestMain published — the parent cannot
// see it any other way, because the directory is gone by the time the child has
// exited — and then leaves the way [argvTargetExitEnv] told it to.
func TestHelperCoverRootTargetProcess(t *testing.T) {
	if !HelperEnabled(argvTargetEnv) {
		return
	}
	_, _ = fmt.Fprintln(os.Stdout, coverRootMarker+HelperCoverRoot())
	status := os.Getenv(argvTargetExitEnv)
	if status == "" {
		return
	}
	code, err := strconv.Atoi(status)
	if err != nil {
		t.Fatalf("%s = %q, which is not a status", argvTargetExitEnv, status)
	}
	os.Exit(code)
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

	// A root of the test's own rather than the one this suite published, which
	// it publishes only when it is itself running under -cover, and a GOCOVERDIR
	// of the test's own standing in for the one `go test -cover` exports, which
	// is what makes the helper a coverage run at all. The rule holds in either
	// run, so the test should not need either of them from the suite.
	root := t.TempDir()
	shared := t.TempDir()
	env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1",
		HelperCoverRootEnv+"="+root, CoverDirEnv+"="+shared)
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
		if dir == shared || strings.HasPrefix(dir, shared+string(filepath.Separator)) {
			t.Errorf("a helper's %s is %q, inside the directory the parent's own profile is collected in", CoverDirEnv, dir)
		}
	}
	if seen[0] == seen[1] {
		t.Errorf("two helper processes shared %s = %q, which is the collision this exists to prevent",
			CoverDirEnv, seen[0])
	}
}

// TestHelperWithNoCoverageRootReadsItsOwnGOCOVERDIR is how the two ways a root
// can be missing are told apart, and why they are not the same thing.
//
// A helper with neither a root nor a GOCOVERDIR is an ordinary `go test`: it is
// not instrumented, nothing writes coverage, and there is nothing to redirect —
// so it runs, and creating a directory for it would be creating the leak. A
// helper with a GOCOVERDIR and no root is a coverage run whose root went missing
// on the way in — a TestMain that ran m.Run itself, an environment policy that
// stripped the variable — and carrying on means writing covmeta into the very
// directory `go test` is collecting, so it refuses instead.
func TestHelperWithNoCoverageRootReadsItsOwnGOCOVERDIR(t *testing.T) {
	t.Parallel()

	t.Run("no coverage anywhere runs", func(t *testing.T) {
		t.Parallel()

		env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1",
			HelperCoverRootEnv+"=", CoverDirEnv+"=")
		result := Exec(t, t.TempDir(), env, os.Args[0], "env", CoverDirEnv)

		RequireExit(t, result, 0, "the helper program")
		if got := string(result.Stdout); got != "" {
			t.Errorf("a helper in a run with no coverage set %s = %q, so it made a directory nothing "+
				"will ever read", CoverDirEnv, got)
		}
	})

	t.Run("a coverage run with no root refuses", func(t *testing.T) {
		t.Parallel()

		env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1",
			HelperCoverRootEnv+"=", CoverDirEnv+"="+t.TempDir())
		result := Exec(t, t.TempDir(), env, os.Args[0], "env", CoverDirEnv)

		if result.ExitCode != HelperMisuse {
			t.Errorf("a helper handed the parent's %s and no %s exited %d, want the misuse status %d:\n%s",
				CoverDirEnv, HelperCoverRootEnv, result.ExitCode, HelperMisuse, result.Output)
		}
		RequireOutput(t, result, "the refusal", HelperCoverRootEnv+" is unset")
	})

	// A root without a GOCOVERDIR is the inverse leftover: an ancestor's root
	// still in the environment of a process that is not itself a coverage run.
	// It writes nothing, so it must make nothing — a directory made here would
	// have no owner at all, since only the suite that made the root removes it.
	t.Run("a root without coverage makes nothing", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1",
			HelperCoverRootEnv+"="+root, CoverDirEnv+"=")
		result := Exec(t, t.TempDir(), env, os.Args[0], "env", CoverDirEnv)

		RequireExit(t, result, 0, "the helper program")
		if got := string(result.Stdout); got != "" {
			t.Errorf("a helper with a root but no coverage set %s = %q, so it made a directory nothing "+
				"will ever read or remove", CoverDirEnv, got)
		}
		if entries := Entries(t, root); len(entries) != 0 {
			t.Errorf("a helper with a root but no coverage left %v under the root", entries)
		}
	})
}

// TestHelperLeavesNoCoverageDirectoryBehind is the other half of that rule: the
// private directory exists for a coverage run and for nothing else.
//
// Nothing ever collects what a helper writes into it. [runSuite] removes the
// root with the counters still inside, deliberately — that is how helper
// counters are kept out of the parent's own profile — so the directory's whole
// job is to be somewhere other than the GOCOVERDIR `go test -cover` exports.
// Outside a coverage run there is no such directory to stay away from: nothing
// is instrumented, no exit hook fires, and the mkdir buys nothing at all.
//
// What it costs is a directory per test binary process that only a deferred
// function removes, and a mutation run is thousands of test binary processes
// with a budget that kills the slow ones. Eleven thousand eight hundred and
// forty-two of them were counted in one machine's /tmp after a run, which is
// why the third case here leaves the way a killed process does.
func TestHelperLeavesNoCoverageDirectoryBehind(t *testing.T) {
	t.Parallel()

	argv := HelperArgv("TestHelperCoverRootTargetProcess")
	// Both variables are cleared rather than assumed absent, because a plain
	// `go test` has neither and this suite is sometimes the one under -cover:
	// GOCOVERDIR is what makes a run a coverage run, and the root is what this
	// suite would otherwise pass down to a child of its own.
	plainRun := []string{CoverDirEnv + "=", HelperCoverRootEnv + "="}

	t.Run("a run with no coverage makes none", func(t *testing.T) {
		t.Parallel()

		scratch := t.TempDir()
		env := withEntries(Compose(t, scratch), append([]string{argvTargetEnv + "=1"}, plainRun...)...)
		result := Exec(t, t.TempDir(), env, argv...)

		RequireExit(t, result, 0, "the re-executed test")
		if root := publishedCoverRoot(t, result); root != "" {
			t.Errorf("a suite with no coverage to keep apart published %s = %q; a directory nothing "+
				"writes to is a directory every killed process leaves behind", HelperCoverRootEnv, root)
		}
		requireNoCoverageDirectoriesIn(t, scratch)
	})

	t.Run("a coverage run makes one and removes it", func(t *testing.T) {
		t.Parallel()

		scratch := t.TempDir()
		// The root is cleared as well, so that what the child reports is the one
		// it published rather than one inherited from a parent under -cover.
		env := withEntries(Compose(t, scratch),
			argvTargetEnv+"=1", HelperCoverRootEnv+"=", CoverDirEnv+"="+t.TempDir())
		result := Exec(t, t.TempDir(), env, argv...)

		RequireExit(t, result, 0, "the re-executed test")
		root := publishedCoverRoot(t, result)
		if root == "" {
			t.Fatalf("a suite whose %s is set published no %s, so its helpers have nowhere private "+
				"to write and every one of them writes covmeta into the parent's directory",
				CoverDirEnv, HelperCoverRootEnv)
		}
		if !SamePath(filepath.Dir(root), scratch) {
			t.Errorf("the coverage root is %q, want one carved out of the temporary directory the "+
				"child was given, %s", root, scratch)
		}
		requireNoCoverageDirectoriesIn(t, scratch)
	})

	t.Run("a run that is killed leaves none", func(t *testing.T) {
		t.Parallel()

		scratch := t.TempDir()
		env := withEntries(Compose(t, scratch),
			append([]string{argvTargetEnv + "=1", argvTargetExitEnv + "=9"}, plainRun...)...)
		result := Exec(t, t.TempDir(), env, argv...)

		if result.ExitCode != 9 {
			t.Fatalf("the child exited %d, want the 9 it was told to leave with:\n%s",
				result.ExitCode, result.Output)
		}
		requireNoCoverageDirectoriesIn(t, scratch)
	})
}

// publishedCoverRoot is the root the child reported, out of a stream that also
// carries the testing package's own output.
func publishedCoverRoot(t *testing.T, r Result) string {
	t.Helper()

	for line := range strings.SplitSeq(string(r.Stdout), "\n") {
		if root, ok := strings.CutPrefix(strings.TrimSpace(line), coverRootMarker); ok {
			return root
		}
	}
	t.Fatalf("the child never reported its coverage root:\n%s", r.Output)
	return ""
}

// requireNoCoverageDirectoriesIn fails the step for every private coverage
// directory left in the temporary directory a child was given.
func requireNoCoverageDirectoriesIn(t *testing.T, dir string) {
	t.Helper()

	left, err := filepath.Glob(filepath.Join(dir, helperCoverPrefix+"*"))
	if err != nil {
		t.Fatalf("scanning %s for coverage directories: %v", dir, err)
	}
	if len(left) > 0 {
		t.Errorf("the child left %d coverage directory(ies) behind in the temporary directory it was "+
			"given: %q", len(left), left)
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
