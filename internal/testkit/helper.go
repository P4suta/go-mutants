// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// The two variables the helper protocol owns.
//
// Neither begins with GO_MUTANTS_, and that is load-bearing rather than
// stylistic: [Env] and [Compose] strip that whole prefix from every child
// environment they compose — which is how a developer's exported
// GO_MUTANTS_ACTIVE is kept from running a mutant as the baseline — and a cover
// root stripped on the way into a helper is a helper with nowhere private to
// write.
const (
	// HelperCoverRootEnv names the directory each helper process carves its own
	// coverage output directory out of.
	HelperCoverRootEnv = "TESTKIT_HELPER_COVERDIR_ROOT"
	// CoverDirEnv is the variable Go's coverage runtime reads at exit.
	CoverDirEnv = "GOCOVERDIR"
)

// HelperMisuse is the status a helper exits with when it cannot set itself up.
//
// It is a status no test in this repository asks a helper to produce, so a
// harness failure can never be read as the exit code a test was expecting.
const HelperMisuse = 97

// HelperArgv is the command that re-executes this test binary with one test
// selected, for the helper shape that is a Test function.
//
// Some helpers cannot be a program: they need a *testing.T, because what they
// do is fail. A barrier that has to report "the release file never appeared"
// after thirty seconds, a child that asserts on the environment it was handed —
// those are tests, and re-executing the binary with the run pattern is how a
// test becomes a subprocess without compiling anything at test time.
//
// The anchors are the point of it existing at all. `-test.run=TestFoo` is a
// regular expression that also selects TestFooAndMore, and a helper process that
// ran a second test writes a second test's output into the stream its parent is
// parsing. Every hand-written copy of this line in the repository had them; the
// next one would have been the one that did not.
//
// The name is escaped, because `-test.run` takes a regular expression and a Go
// test name is not one: a subtest path with a `+` or a `.` in it — and every
// name generated from a version, a path or a fixture has one — would otherwise
// be a pattern that matches something else, or nothing at all.
//
// The gate inside the selected test is [HelperEnabled]: without it, an ordinary
// `go test ./...` runs the helper's body as a test.
func HelperArgv(testName string) []string {
	return []string{TestBinary(), "-test.run=^" + regexp.QuoteMeta(testName) + "$"}
}

// TestBinary is the path of the running test binary, for a caller building an
// argv that re-executes it.
//
// os.Executable is preferred over os.Args[0] and the difference is not
// theoretical. os.Args[0] is whatever the parent passed, so it can be relative —
// and a child started in another directory resolves a relative argv[0] against
// *its* working directory, which is exactly what happens when a helper is run
// through a Workspace: the command is executed inside the snapshot. os.Executable
// answers with an absolute path or with an error, and the error case falls back
// rather than failing, because a caller with no way to name the binary is no
// worse off than it was.
func TestBinary() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return os.Args[0]
}

// HelperEnabled reports whether this process was started as the helper the
// variable names.
//
// A non-empty value, so `=0` still means "you are the helper". Every helper here
// is started by code that sets the variable deliberately, and one that decided
// the value looked falsy and ran the suite instead would look exactly like a
// test that passed. An empty value reads as false because that is what an
// inherited-but-cleared variable looks like, and because os/exec's own
// deduplication makes `NAME=` the way a composed environment says "not set".
func HelperEnabled(variable string) bool {
	return os.Getenv(variable) != ""
}

// Helper is the TestMain of a package whose tests need real processes.
//
//	func TestMain(m *testing.M) {
//		os.Exit(testkit.Helper(m, "GO_MUTANTS_RUNNER_TEST_HELPER", runHelper))
//	}
//
// When the variable is set this process *is* the helper: program runs with
// os.Args[1:] and its return value is the process's status. Otherwise the suite
// runs as usual, with the private coverage root created before it and removed
// after it.
//
// The program runs from TestMain rather than from a test function because of
// what internal/runner's tests are about. They assert on a child's exact bytes
// and on arbitrary exit statuses, and a helper written as a test function can
// promise neither: the testing package writes "PASS" and a timing line after
// the test, "=== RUN" before it under -test.v, and it decides the exit status
// itself.
//
// GOCOVERDIR is redirected into a directory of the helper's own, and that is
// not a nicety either. A helper is this very test binary re-executed, so under
// `go test -cover` it is coverage-instrumented — and because it exits from
// TestMain without ever reaching the testing package's "the profile is already
// written" call, the coverage runtime's exit hook fires. Left alone that hook
// writes covmeta.<hash> into the single GOCOVERDIR that `go test` exports,
// under a name derived from the binary and therefore identical for every
// helper. The concurrent atomic renames then collide: on Windows the loser
// prints "error: coverage meta-data emit failed: ... Access is denied" on
// stderr, the runner faithfully captures it, and every assertion about exact
// captured bytes fails. Unsetting the variable is not the fix — measured, the
// hook then prints "warning: GOCOVERDIR not set, no coverage data emitted" to
// the same stderr. A private directory is the only quiet answer, and it has the
// second virtue of keeping helper counters out of the parent's own profile.
func Helper(m *testing.M, variable string, program func(args []string) int) int {
	if HelperEnabled(variable) {
		if err := isolateCoverageOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "helper: %v\n", err)
			return HelperMisuse
		}
		return program(os.Args[1:])
	}
	return runSuite(m)
}

// runSuite runs the suite proper with the helper coverage root published.
//
// It is a function rather than the body of [Helper] because the root has to be
// removed on the way out and a TestMain ends in os.Exit, which runs no deferred
// function — so the removal has to happen before the status is returned.
func runSuite(m *testing.M) int {
	root, err := os.MkdirTemp("", "go-mutants-helper-cover-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating the helper coverage root: %v\n", err)
		return HelperMisuse
	}
	defer func() { _ = os.RemoveAll(root) }()

	// Published into this process's own environment rather than only into the
	// environments the suite composes, so that a child which deliberately
	// inherits — internal/runner runs one with a nil Spec.Env, to prove that
	// nil means "the whole environment" — finds it too.
	if err := os.Setenv(HelperCoverRootEnv, root); err != nil {
		fmt.Fprintf(os.Stderr, "publishing the helper coverage root: %v\n", err)
		return HelperMisuse
	}
	return m.Run()
}

// HelperCoverRoot is the directory helper processes carve their coverage
// directories out of, or the empty string in a process that is not running
// under [Helper].
func HelperCoverRoot() string { return os.Getenv(HelperCoverRootEnv) }

// isolateCoverageOutput points this helper's coverage output at a directory
// nothing else writes to.
//
// It runs in the helper rather than where the parent composes an environment,
// so that it covers every helper process there is: the ones handed a composed
// environment, the one that inherits, and any grandchild a helper spawns.
func isolateCoverageOutput() error {
	root := HelperCoverRoot()
	if root == "" {
		return fmt.Errorf("%s is unset, so this helper has nowhere private to write coverage output",
			HelperCoverRootEnv)
	}
	// The pid is unique among the processes that are alive at the same time,
	// which is exactly the set that could collide.
	dir := filepath.Join(root, strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Setenv(CoverDirEnv, dir)
}
