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

// The switch, the name and the payload of the child
// TestHelperUnderTheGocoverdirFlagKeepsItsChildrenQuiet drives.
//
// They live here rather than beside that test because the process they describe
// is here: the integration-tagged parent compiles *this* package's test binary,
// so the test it selects with `-test.run` has to be one the unit tier contains.
//
// The payload is written to standard error with no trailing newline, because
// stderr is the stream the coverage runtime's exit hook writes its warning to
// and exact bytes are the only assertion that can tell "the child spoke" apart
// from "the child spoke and the runtime added a line".
const (
	gocoverdirProbeEnv  = "TESTKIT_GOCOVERDIR_FLAG_PROBE"
	gocoverdirProbeTest = "TestHelperUnderTheGocoverdirFlagProcess"
	gocoverdirProbeText = "exactly these bytes on standard error"
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
	case "printerr":
		// printerr TEXT — the same on standard error, which is where the
		// coverage runtime's exit hook writes and therefore the stream a test
		// about that hook has to read.
		_, _ = fmt.Fprint(os.Stderr, rest[0])
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

// TestHelperUnderTheGocoverdirFlagProcess is not a test either: it is the child
// TestHelperUnderTheGocoverdirFlagKeepsItsChildrenQuiet re-executes, in a
// process built with `go test -c -cover` and told where its coverage data goes
// with `-test.gocoverdir` rather than with GOCOVERDIR.
//
// It reports the root its own TestMain published, which is the only way the
// parent can see a directory that is gone by the time this process has exited,
// and then starts a helper whose entire output is [gocoverdirProbeText]. That
// helper is the subject. It is this binary again, so it is instrumented too,
// and it leaves through os.Exit without reaching the testing package's coverage
// teardown — so its exit hook fires, and what the hook does depends on whether
// this suite gave it somewhere private to write.
func TestHelperUnderTheGocoverdirFlagProcess(t *testing.T) {
	if !HelperEnabled(gocoverdirProbeEnv) {
		return
	}
	_, _ = fmt.Fprintln(os.Stdout, coverRootMarker+HelperCoverRoot())

	env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1")
	result := Exec(t, t.TempDir(), env, TestBinary(), "printerr", gocoverdirProbeText)

	RequireExit(t, result, 0, "the helper program")
	if got := string(result.Output); got != gocoverdirProbeText {
		t.Errorf("the helper's whole output is %q, want exactly %q: an instrumented helper with "+
			"nowhere private to write warns on the stream its caller is asserting the bytes of",
			got, gocoverdirProbeText)
	}
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

// TestHelperCoverageRedirectionTurnsOnTheRoot pins which of the two variables
// decides, and what each of the three shapes without both of them means.
//
// A helper with neither a root nor a GOCOVERDIR is an ordinary `go test`: it is
// not instrumented, nothing writes coverage, and there is nothing to redirect —
// so it runs, and creating a directory for it would be creating the leak. A
// helper with a GOCOVERDIR and no root is a coverage run whose root went missing
// on the way in — a TestMain that ran m.Run itself, an environment policy that
// stripped the root — and carrying on means writing covmeta into the very
// directory `go test` is collecting, so it refuses instead. A helper with a
// root and no GOCOVERDIR is the third shape and the reason the root is what
// decides: see the case itself.
func TestHelperCoverageRedirectionTurnsOnTheRoot(t *testing.T) {
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

	// A root and no GOCOVERDIR is the shape a scrubbing environment policy
	// produces, and it is why the root rather than the variable is what the
	// redirection turns on.
	//
	// internal/execute strips GOCOVERDIR from every child environment it
	// composes, so that a mutant's test binary cannot append its counters into
	// the profile go-mutants' own coverage job is collecting — and that
	// package's unit tests start this very binary as their scripted `go` and as
	// the test binary a scripted compile produced. The variable is gone by the
	// time the helper looks; the instrumentation is not, and the coverage
	// runtime's exit hook fires all the same. Reading only GOCOVERDIR there
	// leaves it printing "warning: GOCOVERDIR not set, no coverage data
	// emitted" onto the stderr those tests assert the exact bytes of.
	//
	// The root is what still says "this binary is instrumented": [runSuite]
	// publishes it only in a suite that is itself a coverage run, and a helper
	// is that suite's binary re-executed.
	t.Run("a root without the variable is still a coverage run", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		env := withEntries(Compose(t, t.TempDir()), selfHelperEnv+"=1",
			HelperCoverRootEnv+"="+root, CoverDirEnv+"=")
		result := Exec(t, t.TempDir(), env, os.Args[0], "env", CoverDirEnv)

		RequireExit(t, result, 0, "the helper program")
		dir := string(result.Stdout)
		if !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			t.Errorf("a helper whose %s was stripped on the way in set it to %q, want a directory under "+
				"the private root %s", CoverDirEnv, dir, root)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("the helper's coverage directory was not created: %v", err)
		}
	})
}

// TestTheCoverModeDecidesTheRootAndTheRootDecidesTheHelper is the whole
// decision as a table, in the one tier that can ask it about a cover mode this
// binary was not built with.
//
// The two halves are asked separately because they are answered separately, and
// getting that wrong is what this table exists to keep from happening again:
//
//   - Whether a suite makes a private root at all is [testing.CoverMode], which
//     is the compiled-in fact "this binary is instrumented". It is not
//     GOCOVERDIR, which `go test -cover` happens to export and which a binary
//     run with `-test.gocoverdir` — every mutant's test binary — does not have.
//   - What a helper does about its own coverage output is the root and the
//     variable, exactly as #62 left it. The mode does not enter, and the rows
//     that vary it while holding the rest still say so: a helper is the binary
//     of the suite that published the root, re-executed, so asking again would
//     only re-derive what publishing the root already claimed — and it would
//     make the redirection unaskable in the tier that has no coverage.
func TestTheCoverModeDecidesTheRootAndTheRootDecidesTheHelper(t *testing.T) {
	t.Parallel()

	const (
		root    = "/tmp/go-mutants-helper-cover-1"
		profile = "/tmp/the-parents-own-profile"
	)
	for _, test := range []struct {
		name     string
		mode     string
		root     string
		coverDir string
		publish  bool
		action   coverAction
	}{{
		// Nothing is instrumented, no exit hook fires, and a directory nothing
		// writes to is a directory every killed process leaves behind.
		name:   "a plain `go test`",
		action: coverNothing,
	}, {
		// The developer's `go test -cover`, which exports the variable as well.
		name:     "`go test -cover`",
		mode:     "set",
		root:     root,
		coverDir: profile,
		publish:  true,
		action:   coverCarve,
	}, {
		// A mutant's test binary, and the shape this table was written for: the
		// directory arrives as a flag, so there is no variable to read.
		name:    "a `-cover` binary run with -test.gocoverdir",
		mode:    "atomic",
		root:    root,
		publish: true,
		action:  coverCarve,
	}, {
		// A coverage run whose root went missing on the way in — a TestMain that
		// ran m.Run itself, an environment policy that stripped it. Carrying on
		// means writing covmeta into the directory `go test` is collecting.
		name:     "a coverage run with nowhere private to write",
		mode:     "count",
		coverDir: profile,
		publish:  true,
		action:   coverRefuse,
	}, {
		// An uninstrumented process that inherited a root: the root is the
		// parent's claim and this honours it, because the mode is not what it
		// answers. Nothing is written either way, and the directory is inside a
		// root its owner removes whole.
		name:   "a root inherited by a process with no coverage of its own",
		root:   root,
		action: coverCarve,
	}, {
		// The refusal does not ask about the mode either, and deliberately: a
		// helper handed the parent's profile directory is a misconfiguration
		// whichever binary it is, and a check that meant one thing in the unit
		// tier and another under -cover is a check nobody can rely on.
		name:     "a GOCOVERDIR handed to a process with no root",
		coverDir: profile,
		action:   coverRefuse,
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := suitePublishesCoverRoot(test.mode); got != test.publish {
				t.Errorf("suitePublishesCoverRoot(%q) = %v, want %v", test.mode, got, test.publish)
			}
			if got := helperCoverAction(test.root, test.coverDir); got != test.action {
				t.Errorf("helperCoverAction(%q, %q) = %v, want %v",
					test.root, test.coverDir, got, test.action)
			}
		})
	}
}

// TestHelperMakesACoverageDirectoryOnlyForAnInstrumentedRun is the other half
// of that rule: the private directory exists for an instrumented run and for
// nothing else.
//
// Nothing ever collects what a helper writes into it. [runSuite] removes the
// root with the counters still inside, deliberately — that is how helper
// counters are kept out of the parent's own profile — so the directory's whole
// job is to be somewhere other than the directory the parent's own coverage is
// collected in. A binary that is not instrumented has no such directory to stay
// away from: no exit hook fires, and the mkdir buys nothing at all.
//
// What it costs is a directory per test binary process that only a deferred
// function removes, and a mutation run is thousands of test binary processes
// with a budget that kills the slow ones. Eleven thousand eight hundred and
// forty-two of them were counted in one machine's /tmp after a run, which is
// why the last case here leaves the way a killed process does.
//
// Every expectation is derived from [testing.CoverMode] rather than written
// down, and that is the correction this test carries. The child is this binary
// re-executed, so it is instrumented exactly when this suite is — and it used
// to be GOCOVERDIR that decided, which is a variable `go test -cover` exports,
// a plain `go test` does not, and a mutant's test binary run with
// `-test.gocoverdir` never sees although it is as instrumented as either. A
// test that asserted one branch would be asserting which command the developer
// happened to type.
func TestHelperMakesACoverageDirectoryOnlyForAnInstrumentedRun(t *testing.T) {
	t.Parallel()

	argv := HelperArgv("TestHelperCoverRootTargetProcess")
	// Both variables are cleared rather than assumed absent, because a plain
	// `go test` has neither and this suite is sometimes the one under -cover:
	// the root is what this suite would otherwise pass down to a child of its
	// own, and GOCOVERDIR is what this test is about not being decided by.
	plainRun := []string{CoverDirEnv + "=", HelperCoverRootEnv + "="}
	instrumented := testing.CoverMode() != ""

	// requireRoot asserts what the child published, whichever tier this is.
	requireRoot := func(t *testing.T, result Result, scratch string) {
		t.Helper()
		root := publishedCoverRoot(t, result)
		switch {
		case !instrumented:
			if root != "" {
				t.Errorf("a suite with no coverage to keep apart published %s = %q; a directory "+
					"nothing writes to is a directory every killed process leaves behind",
					HelperCoverRootEnv, root)
			}
		case root == "":
			t.Errorf("a coverage-instrumented suite published no %s, so its helpers have nowhere "+
				"private to write: every one of them writes covmeta into the parent's directory, or "+
				"says on stderr that it wrote nowhere", HelperCoverRootEnv)
		case !SamePath(filepath.Dir(root), scratch):
			t.Errorf("the coverage root is %q, want one carved out of the temporary directory the "+
				"child was given, %s", root, scratch)
		}
	}

	t.Run("a run that ends normally leaves nothing behind", func(t *testing.T) {
		t.Parallel()

		scratch := t.TempDir()
		env := withEntries(Compose(t, scratch), append([]string{argvTargetEnv + "=1"}, plainRun...)...)
		result := Exec(t, t.TempDir(), env, argv...)

		RequireExit(t, result, 0, "the re-executed test")
		requireRoot(t, result, scratch)
		requireCoverageDirectoriesIn(t, scratch, 0)
	})

	t.Run("a GOCOVERDIR does not make a run a coverage run", func(t *testing.T) {
		t.Parallel()

		scratch := t.TempDir()
		// The root is cleared as well, so that what the child reports is the one
		// it published rather than one inherited from a parent under -cover.
		env := withEntries(Compose(t, scratch),
			argvTargetEnv+"=1", HelperCoverRootEnv+"=", CoverDirEnv+"="+t.TempDir())
		result := Exec(t, t.TempDir(), env, argv...)

		RequireExit(t, result, 0, "the re-executed test")
		requireRoot(t, result, scratch)
		requireCoverageDirectoriesIn(t, scratch, 0)
	})

	// The residual, stated rather than hoped for. A killed process runs no
	// deferred function, so an instrumented one leaves its root exactly where
	// os.TempDir() put it — which is why that directory matters: under a
	// mutation run internal/execute points TMPDIR at the worker's scratch and
	// the run takes the whole thing away afterwards, and under a developer's own
	// `go test -cover` it is the machine's, where a Ctrl-C really does leave one.
	t.Run("a run that is killed leaves only what it was carrying", func(t *testing.T) {
		t.Parallel()

		scratch := t.TempDir()
		env := withEntries(Compose(t, scratch),
			append([]string{argvTargetEnv + "=1", argvTargetExitEnv + "=9"}, plainRun...)...)
		result := Exec(t, t.TempDir(), env, argv...)

		if result.ExitCode != 9 {
			t.Fatalf("the child exited %d, want the 9 it was told to leave with:\n%s",
				result.ExitCode, result.Output)
		}
		want := 0
		if instrumented {
			want = 1
		}
		requireCoverageDirectoriesIn(t, scratch, want)
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

// requireCoverageDirectoriesIn fails the step unless exactly want private
// coverage directories are left in the temporary directory a child was given.
//
// A count rather than an absence, because one of the two answers is now a
// number: a killed instrumented process leaves the root it was carrying, and a
// test that could only say "none" would have to skip the case rather than state
// it.
func requireCoverageDirectoriesIn(t *testing.T, dir string, want int) {
	t.Helper()

	left, err := filepath.Glob(filepath.Join(dir, helperCoverPrefix+"*"))
	if err != nil {
		t.Fatalf("scanning %s for coverage directories: %v", dir, err)
	}
	if len(left) != want {
		t.Errorf("the child left %d coverage directory(ies) behind in the temporary directory it was "+
			"given, want %d: %q", len(left), want, left)
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
