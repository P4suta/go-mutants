// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// The variables the keep policy reads.
//
// All four wear the GO_MUTANTS_ prefix, because a developer types them on a
// command line and CI writes them into a workflow file — and that is the prefix
// [Env] strips from the process, so every one of them is read through
// [harnessSetting] rather than with os.Getenv. A helper that read them naively
// would lose the whole feature the moment a test redirected its environment,
// which is every test.
const (
	// KeepEnv is the policy: unset keeps nothing, `1`/`true`/`yes`/`on`/
	// `failed`/`on-failure` keeps what a failing test left, `always` keeps
	// everything. [keepPolicyOf] holds the whole table.
	KeepEnv = "GO_MUTANTS_TEST_KEEP"
	// KeepDirEnv names the root a kept scratch directory is filed under.
	// internal/devtools/testcache reports and empties that root, so the two
	// copies of the rule have to agree about where it is —
	// TestKeptRootAgreesWithTestkit is what keeps them equal.
	KeepDirEnv = "GO_MUTANTS_TEST_KEEP_DIR"
	// VerboseEnv makes [DumpFiles] print on a test that passed.
	VerboseEnv = "GO_MUTANTS_TEST_VERBOSE"
	// ForceFailEnv names one test that is to fail on purpose, so that what a
	// failure leaves behind can be looked at without breaking anything.
	ForceFailEnv = "GO_MUTANTS_TEST_FORCE_FAIL"
)

// The names below the platform's cache root, and the file that says a kept root
// is the harness's.
//
// One parent holds the build cache and the kept scratch root, so that a
// developer who wants everything this harness ever wrote can delete
// `<cache root>/go-mutants-test` and be done. Both names are duplicated in
// internal/devtools/testcache for the reason [BuildCacheMarker] gives — a
// production `main` may not import a test-only package — and both are compared
// against that copy by TestKeptRootAgreesWithTestkit.
const (
	harnessDirName    = "go-mutants-test"
	buildCacheDirName = "go-build"
	keptDirName       = "kept"

	// KeptMarker is the file that says the kept scratch root is the harness's,
	// and therefore that `mise run test-clean` may empty it. Nothing in a path
	// says who made it: `GO_MUTANTS_TEST_KEEP_DIR=$HOME/Desktop/failures` is an
	// absolute path like any other, and that is a directory somebody keeps
	// things in on purpose.
	KeptMarker = ".go-mutants-kept"
)

// A Keep is what happens to a test's scratch directory when the test ends.
type Keep int

const (
	// KeepNever is the default, and the reason there is a policy at all:
	// keeping unconditionally filled a developer's disk twice.
	KeepNever Keep = iota
	// KeepOnFailure keeps what a failing test left and removes the rest. It is
	// what CI runs with.
	KeepOnFailure
	// KeepAlways keeps everything, which is what a developer asking "what does
	// this test even produce?" wants.
	KeepAlways
)

// String is the spelling that appears in a log line and in [KeptFileName].
func (k Keep) String() string {
	switch k {
	case KeepOnFailure:
		return "on-failure"
	case KeepAlways:
		return "always"
	default:
		return "never"
	}
}

// pinnedKeep, pinnedKeepDir, pinnedVerbose and pinnedForceFail are the four
// variables as the process started with them, for the fallback [harnessSetting]
// applies once [Env] has removed the GO_MUTANTS_ namespace from the process.
var (
	pinnedKeep      = os.Getenv(KeepEnv)
	pinnedKeepDir   = os.Getenv(KeepDirEnv)
	pinnedVerbose   = os.Getenv(VerboseEnv)
	pinnedForceFail = os.Getenv(ForceFailEnv)
)

// forcedAny records whether [ForceFail] ever fired in this process, so that a
// developer who named a test that does not reach the harness is told rather than
// left watching a green run.
var forcedAny atomic.Bool

// KeepPolicy is what this run does with a test's scratch directory.
//
// The value is resolved on every call rather than cached, and that is the same
// decision [BuildCache] makes for the same reason: [Env] strips this variable
// out of the process, a test may set its own with t.Setenv, and a value read
// once before either of those happened would answer a different question from
// the one being asked. [harnessSetting] is the seam — the live environment,
// what the policy removed during this test, then what the process started with —
// so a job-level `env:` still decides for the tests that say nothing.
//
// Once a test has taken a directory the answer is pinned to it: the policy is
// captured on that test's ledger, so a test whose environment changes halfway
// through cannot have its directories created under one rule and judged under
// another. [Keeping] is the form that reads the pinned answer.
//
// An unknown spelling panics, and that is the whole point of the function
// rejecting rather than defaulting. The variable is set once, for a whole CI
// job, in a file nobody reads again: a policy that read
// `GO_MUTANTS_TEST_KEEP=sometimes` as "never" would leave every failed job with
// nothing to upload and nothing in the log to say why — which is exactly the
// failure keeping exists to prevent. The value the process started with is
// checked once as well, so a typo in the job's own variable is refused even in
// the tests that override it.
func KeepPolicy() Keep {
	validatePinnedKeep()
	return keepPolicyOf(harnessSetting(KeepEnv, pinnedKeep))
}

// validatePinnedKeep refuses a typo in the variable the job set, once, whatever
// the test being run has since done to the environment.
var validatePinnedKeep = sync.OnceFunc(func() {
	if pinnedKeep != "" {
		keepPolicyOf(pinnedKeep)
	}
})

// keepPolicyOf reads the spellings a person types into a workflow file or a
// shell, and refuses everything else.
//
// The "on" and "off" spellings are the ones [truthy] already accepts for the
// harness's other switch, and they are here for that reason rather than for
// generosity: two variables in one namespace that disagreed about how "on" is
// spelled would be a trap with no symptom, because the one that did not
// recognise it would simply be off. The "off" half matters more still — turning
// keeping off for one command is the most likely thing anybody types, and it
// used to be the one spelling that stopped the run.
func keepPolicyOf(value string) Keep {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "n", "off", "disabled":
		return KeepNever
	case "1", "true", "yes", "y", "on", "enabled", "failed", "on-failure":
		return KeepOnFailure
	case "always":
		return KeepAlways
	default:
		panic(fmt.Sprintf("%s=%s is not a keep policy: write nothing, 0, false, no or off to keep "+
			"nothing, one of 1, true, yes, on, enabled, failed or on-failure to keep what a failing "+
			"test left, or always to keep everything", KeepEnv, value))
	}
}

// Verbose reports whether a run was asked to print what it would otherwise only
// print on a failure.
func Verbose() bool { return truthy(harnessSetting(VerboseEnv, pinnedVerbose)) }

// Keeping reports whether this test's directories will be kept.
//
// It is the question a helper with a cleanup of its own has to ask before
// removing anything: internal/testkit/mutantkit's snapshot registers a removal,
// and a snapshot removed by its own cleanup is the instrumented tree the keep
// existed to let somebody read.
//
// The policy is the one this test's directories were created under rather than
// whatever the environment says now, so that a helper's cleanup and the
// directory's own cleanup cannot reach opposite conclusions about the same
// directory. The answer is only final inside a cleanup, because that is the
// first moment [testing.TB.Failed] is.
func Keeping(t testing.TB) bool {
	switch policyFor(t) {
	case KeepAlways:
		return true
	case KeepOnFailure:
		return t.Failed()
	default:
		return false
	}
}

// KeepRoot is the directory a kept scratch directory is filed under.
//
// It is [BuildCache]'s rule for the other directory the harness owns outside a
// temporary one: [KeepDirEnv] when it names an absolute path, and
// `<os.UserCacheDir()>/go-mutants-test/kept` otherwise — the platform's cache
// root rather than anything below HOME, because the suites move HOME into a
// temporary directory of their own and a root below a moved HOME would be a new
// root in every test.
//
// A relative override is an error rather than a fallback, again as in
// [BuildCache]: a helper that resolved one against its own working directory
// would file evidence somewhere nobody named, and one that ignored the value
// would file it where the person setting the variable was moving away from.
//
// The path is not resolved through its symlinks, and a caller comparing it with
// another spelling of the same directory wants [SamePath]: on macOS the cache
// root under /var is reached through /private/var, so a string comparison of
// two correct answers fails.
func KeepRoot() (string, error) {
	named := harnessSetting(KeepDirEnv, pinnedKeepDir)
	if named != "" {
		if !filepath.IsAbs(named) {
			return "", fmt.Errorf("%s=%s is not an absolute path, and a test that started elsewhere "+
				"would file its evidence against its own working directory", KeepDirEnv, named)
		}
		return filepath.Clean(named), nil
	}
	if pinned.userCache == "" {
		return "", errors.New("this platform has no user cache directory, so " + KeepDirEnv + " has to name one")
	}
	return filepath.Join(pinned.userCache, harnessDirName, keptDirName), nil
}

// ForceFail fails the test the [ForceFailEnv] variable names.
//
// It is the way to see what a failure leaves behind without breaking anything:
//
//	GO_MUTANTS_TEST_KEEP=1 GO_MUTANTS_TEST_FORCE_FAIL=TestSomething \
//	    go test -tags integration ./internal/engine -run TestSomething
//
// The name is matched exactly against t.Name(), so naming a parent does not fail
// its subtests and naming a subtest does not fail the parent's other cases. It
// is reported rather than fatal, so the test runs to its end and its dumps and
// its recording are of a test that did what it does — and it is reported once,
// however many constructors the test calls, because three identical failure
// lines above a dump read like three different things going wrong.
//
// # What it cannot reach
//
// The hook fires from [logInputs], so every constructor here that resolves a
// fixture, a toolchain, a module, an environment or a scratch directory carries
// it, and [Scratch] calls it too for the callers that take a directory without
// logging a line. A test that touches none of them — a pure unit test with no
// harness call in it at all — cannot be forced from here, and there is nowhere
// else to put the hook: the testing package offers no per-test entry point a
// library can register, and a TestMain belongs to the package under test rather
// than to the harness. Such a test also has nothing to keep, which is why this
// is a stated limit rather than a gap being worked around — and
// [PackageScratch] says so on the way out when a name was given and nothing
// ever fired.
func ForceFail(t testing.TB) {
	t.Helper()
	name := harnessSetting(ForceFailEnv, pinnedForceFail)
	if name == "" || name != t.Name() {
		return
	}
	l := ledgerFor(t)
	l.mu.Lock()
	already := l.forced
	l.forced = true
	l.mu.Unlock()
	if already {
		return
	}
	forcedAny.Store(true)
	t.Errorf("forced failure by %s=%s", ForceFailEnv, name)
}

// warnIfNothingWasForced is the one report available for a name that never
// matched: it runs where a package's own scratch is released, which is the last
// thing a package with a TestMain does.
func warnIfNothingWasForced() {
	name := harnessSetting(ForceFailEnv, pinnedForceFail)
	if name == "" || forcedAny.Load() {
		return
	}
	fmt.Fprintf(os.Stderr, "testkit: %s=%s matched no test that reached this harness. The hook fires "+
		"from the constructors here, so a test that resolves no fixture, toolchain, module or scratch "+
		"directory cannot be forced — and has nothing to keep either.\n", ForceFailEnv, name)
}

// stampKeptRoot creates the kept root and leaves [KeptMarker] in it, so that
// `mise run test-clean` may empty it later.
//
// It is [stampBuildCache] for the other directory, and it makes the same
// exception for the same reason: a directory that already holds files that are
// not ours is left completely alone. `GO_MUTANTS_TEST_KEEP_DIR=~/Desktop/failures`
// is a perfectly natural thing to type, and a stamp written into it would be a
// permission slip this harness issues on somebody else's behalf.
func stampKeptRoot(t testing.TB, dir string) {
	t.Helper()
	stamped, err := stampHarnessDirectory(dir, KeptMarker, keptMarkerBody)
	switch {
	case err != nil:
		t.Fatalf("creating the kept scratch root %s: %v", dir, err)
	case !stamped:
		t.Logf("testkit: %s already holds files that are not this harness's, so it was not stamped "+
			"as the kept scratch root and `mise run test-clean` will refuse to empty it. Point %s at "+
			"a directory of its own if that is not what you meant.", dir, KeepDirEnv)
	}
}

// keptMarkerBody is what a person who finds the file reads. It is the only
// explanation they get, so it says what the directory is, what fills it, what
// empties it, and that nothing here is precious.
const keptMarkerBody = "This directory is the go-mutants test harness's kept scratch root.\n" +
	"It holds what a failing test left behind, one directory per test, each with a KEPT.txt\n" +
	"naming the test, the fixture, the toolchain and the commands it ran.\n" +
	"`mise run test-cache-status` reports it; `mise run test-clean` empties it.\n" +
	"Nothing in it is precious: it is evidence of runs that have already finished.\n" +
	"This file is also what tells the collector the directory is safe to remove,\n" +
	"which is why nothing removes a directory that does not have one.\n"
