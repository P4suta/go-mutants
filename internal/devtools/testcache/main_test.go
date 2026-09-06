// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestPathRefusesARelativeOverride keeps the one mistake that would silently
// undo the whole tool from being a fallback.
//
// The go command refuses a relative GOCACHE outright, so a wrapper that
// resolved `GO_MUTANTS_TEST_GOCACHE=cache` against its own working directory —
// or, worse, ignored it and used the default — would send the run either
// somewhere nobody named or straight back into the developer's own cache, which
// is the directory the caller was explicitly moving away from. It is an error,
// and the error names the variable.
func TestPathRefusesARelativeOverride(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: filepath.Join("relative", "cache")}

	if code := run([]string{"path"}, &stdout, &stderr, env, deps{}); code == 0 {
		t.Errorf("`path` with a relative %s exited 0 and printed %q, want a refusal", buildCacheEnv, stdout.String())
	}
	if !strings.Contains(stderr.String(), buildCacheEnv) {
		t.Errorf("the refusal does not name the variable:\n%s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("`path` printed a directory it had just refused to resolve: %q", stdout.String())
	}
}

// TestPathPrintsTheResolvedDirectory covers both halves of the rule testkit
// states: the named directory wins, and the default is one shared directory
// under the platform's cache root rather than anything below a moved HOME.
func TestPathPrintsTheResolvedDirectory(t *testing.T) {
	t.Parallel()

	cacheRoot := t.TempDir()
	d := deps{userCache: func() (string, error) { return cacheRoot, nil }}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"path"}, &stdout, &stderr, map[string]string{}, d); code != 0 {
		t.Fatalf("`path` exited %d: %s", code, stderr.String())
	}
	want := filepath.Join(cacheRoot, "go-mutants-test", "go-build")
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Errorf("`path` printed %q, want %q", got, want)
	}

	named := filepath.Join(t.TempDir(), "named")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"path"}, &stdout, &stderr, map[string]string{buildCacheEnv: named}, d); code != 0 {
		t.Fatalf("`path` with %s set exited %d: %s", buildCacheEnv, code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != named {
		t.Errorf("`path` printed %q, want the named %q", got, named)
	}
}

// TestStatusReportsSizeAndCountOfATree is the line that ends every long run,
// and the only reason anybody notices the cache before the disk does.
//
// The exact byte count is printed beside the human-readable one on purpose: the
// human number is what a person reads, and the exact one is what two runs of a
// suite can be subtracted from each other. The file count is there because a
// cache of 400 000 tiny entries and a cache of 400 large ones are the same
// number of gigabytes and a very different `rm -rf`.
//
// The kept-scratch root is reported in the same breath. Keeping is opt-in per
// run and its directories outlive the run that wrote them, so a developer who
// turned it on last week has a tree somewhere they have no reason to remember.
func TestStatusReportsSizeAndCountOfATree(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	writeTree(t, cache, map[string]int{
		"trim.txt":        11,
		"ab/abcdef-d":     4096,
		"ab/abcdef-a":     512,
		"cd/nested/entry": 1,
	})
	kept := filepath.Join(t.TempDir(), "kept")
	writeTree(t, kept, map[string]int{"engine/TestRun-0a1b2c/KEPT.txt": 300})

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: cache, keepDirEnv: kept}
	if code := run([]string{"status"}, &stdout, &stderr, env, deps{}); code != 0 {
		t.Fatalf("`status` exited %d: %s", code, stderr.String())
	}

	out := stdout.String()
	for _, needle := range []string{
		cache,
		"4 files",
		"4620 bytes",
		"4.5 KiB",
		kept,
		"1 file",
		"300 bytes",
	} {
		if !strings.Contains(out, needle) {
			t.Errorf("`status` did not report %q:\n%s", needle, out)
		}
	}
}

// TestStatusSaysSoWhenThereIsNothingThere keeps the first run of the day
// readable: a cache that does not exist yet is the normal state after
// `test-clean`, not an error, and the report says which it is rather than
// printing a zero that could mean either.
func TestStatusSaysSoWhenThereIsNothingThere(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "not-created-yet")
	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: missing, keepDirEnv: filepath.Join(t.TempDir(), "kept")}

	if code := run([]string{"status"}, &stdout, &stderr, env, deps{}); code != 0 {
		t.Fatalf("`status` over a missing directory exited %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, needle := range []string{missing, "does not exist", "0 bytes"} {
		if !strings.Contains(out, needle) {
			t.Errorf("`status` did not report %q:\n%s", needle, out)
		}
	}
}

// TestTrimWipesOnlyWhenOverBudget is the rule that keeps a persistent cache
// persistent.
//
// The budget is the whole reason one shared directory is safe to keep: without
// it the cache grows for as long as the machine lives, and with a wipe on every
// run it is not a cache at all — the standard library would be recompiled once
// per suite, which is the failure a per-test cache had. So the wipe is
// conditional, it happens after the run rather than before it (the run that
// paid to fill the cache is the run that should benefit from it), and a
// directory under budget is left completely alone.
//
// The kept scratch root is deliberately not part of a trim. It holds the
// evidence of runs that failed, its size has nothing to do with the build
// cache's, and deleting a failing run's diagnostics because a *cache* grew is
// the one thing this tool must not do. `clean` removes it, because `clean` is a
// person asking for exactly that.
func TestTrimWipesOnlyWhenOverBudget(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		budget     string
		wantWiped  bool
		wantReport string
	}{
		{name: "under", budget: "8KiB", wantWiped: false, wantReport: "under"},
		{name: "over", budget: "4KiB", wantWiped: true, wantReport: "over"},
		{name: "exactly the budget", budget: "4620", wantWiped: false, wantReport: "under"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cache := filepath.Join(t.TempDir(), "go-build")
			writeTree(t, cache, map[string]int{"ab/entry": 4096, "cd/entry": 512, "trim.txt": 12})
			stamp(t, cache, buildCacheMarker)
			kept := filepath.Join(t.TempDir(), "kept")
			writeTree(t, kept, map[string]int{"engine/TestRun-0a1b2c/KEPT.txt": 300})

			var cleaned []string
			d := deps{cleaner: func(dir string) error {
				cleaned = append(cleaned, dir)
				return nil
			}}

			var stdout, stderr bytes.Buffer
			env := map[string]string{buildCacheEnv: cache, keepDirEnv: kept}
			if code := run([]string{"trim", "--budget", tc.budget}, &stdout, &stderr, env, d); code != 0 {
				t.Fatalf("`trim --budget %s` exited %d: %s", tc.budget, code, stderr.String())
			}

			_, err := os.Stat(cache)
			gone := errors.Is(err, fs.ErrNotExist)
			if gone != tc.wantWiped {
				t.Errorf("after `trim --budget %s` the cache is gone: %v, want %v", tc.budget, gone, tc.wantWiped)
			}
			if wantCleaned := tc.wantWiped; wantCleaned != (len(cleaned) == 1) {
				t.Errorf("`go clean -cache` was run against %q, want it run exactly %v", cleaned, wantCleaned)
			}
			if tc.wantWiped && len(cleaned) == 1 && cleaned[0] != cache {
				t.Errorf("`go clean -cache` was pointed at %s, want the cache %s", cleaned[0], cache)
			}
			if _, err := os.Stat(filepath.Join(kept, "engine", "TestRun-0a1b2c", "KEPT.txt")); err != nil {
				t.Errorf("a trim removed the kept scratch root, which holds the evidence of failed runs: %v", err)
			}
			if out := stdout.String() + stderr.String(); !strings.Contains(out, tc.wantReport) {
				t.Errorf("`trim --budget %s` did not say it was %s budget:\n%s", tc.budget, tc.wantReport, out)
			}
		})
	}
}

// TestTrimRefusesAMissingOrUnreadableBudget keeps a typo from turning the wipe
// into a no-op nobody notices: a `trim` with no budget cannot mean "never wipe",
// because that is what a `trim` that was never wired up also looks like.
func TestTrimRefusesAMissingOrUnreadableBudget(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"trim"}, {"trim", "--budget", "4GB"}, {"trim", "--budget", ""}} {
		var stdout, stderr bytes.Buffer
		env := map[string]string{buildCacheEnv: t.TempDir(), keepDirEnv: t.TempDir()}
		if code := run(args, &stdout, &stderr, env, deps{}); code == 0 {
			t.Errorf("`%s` exited 0, want a refusal:\n%s", strings.Join(args, " "), stdout.String())
		}
		if !strings.Contains(stderr.String(), "budget") {
			t.Errorf("the refusal of `%s` does not mention the budget:\n%s", strings.Join(args, " "), stderr.String())
		}
	}
}

// TestCleanEmptiesTheCacheAndTheKeptRoot is `mise run test-clean`: one command
// that leaves nothing of the harness behind.
//
// Both directories, because they are the two places the suites write outside a
// temporary directory, and a developer reclaiming disk space should not have to
// know there were two. The go command's own eviction runs first — see the
// integration test for why — and the removal that follows is what makes the
// claim "empty" true rather than "empty of entries the go command recognised".
func TestCleanEmptiesTheCacheAndTheKeptRoot(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	writeTree(t, cache, map[string]int{"ab/entry": 4096, "trim.txt": 12})
	stamp(t, cache, buildCacheMarker)
	kept := filepath.Join(t.TempDir(), "kept")
	writeTree(t, kept, map[string]int{"engine/TestRun-0a1b2c/KEPT.txt": 300})
	stamp(t, kept, keptMarker)

	var cleaned []string
	d := deps{cleaner: func(dir string) error {
		cleaned = append(cleaned, dir)
		return nil
	}}

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: cache, keepDirEnv: kept}
	if code := run([]string{"clean"}, &stdout, &stderr, env, d); code != 0 {
		t.Fatalf("`clean` exited %d: %s", code, stderr.String())
	}

	if !slices.Equal(cleaned, []string{cache}) {
		t.Errorf("`go clean -cache` was run against %q, want it run once against %s", cleaned, cache)
	}
	for _, dir := range []string{cache, kept} {
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s still exists after `clean`: %v", dir, err)
		}
	}
	if out := stdout.String(); !strings.Contains(out, cache) || !strings.Contains(out, kept) {
		t.Errorf("`clean` did not say what it removed:\n%s", out)
	}
}

// TestCleanReportsARemovalItCannotFinish is the Windows rule, written so that a
// POSIX machine can prove it.
//
// A file in the build cache can be held open by something the run started — an
// antivirus scanner, a lingering `go` command, a test binary Windows has not
// finished unmapping — and a removal that fails for that reason is retried once
// after a pause and then *reported*. It must not fail the caller: this runs as
// the last step of a suite, and a collector that turns a green run red because a
// directory it wanted to delete is still there has done more damage than the
// directory ever would.
//
// This is the half of the pair that exits 0. The other half is
// TestWipeRefusesADirectoryItDoesNotOwn, where `clean` exits non-zero — the two
// ways a directory does not get emptied are different questions and get opposite
// answers, and reading either test without the other gives the wrong idea of
// what a non-zero exit from `mise run test-clean` means.
func TestCleanReportsARemovalItCannotFinish(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("a read-only parent does not stop a removal on Windows, so the failure has to be provoked differently there")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make a removal fail")
	}

	parent := t.TempDir()
	cache := filepath.Join(parent, "go-build")
	writeTree(t, cache, map[string]int{"ab/entry": 16})
	stamp(t, cache, buildCacheMarker)
	// A directory can only be unlinked from a writable parent, so this makes the
	// removal fail without making the tree unreadable.
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("making %s read-only: %v", parent, err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	var slept []time.Duration
	d := deps{
		cleaner: func(string) error { return nil },
		sleep:   func(d time.Duration) { slept = append(slept, d) },
	}

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: cache, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
	if code := run([]string{"clean"}, &stdout, &stderr, env, d); code != 0 {
		t.Errorf("`clean` exited %d over a directory it could not remove, want 0 and a report:\n%s", code, stderr.String())
	}
	if len(slept) != 1 {
		t.Errorf("the removal slept %v times, want exactly one retry", len(slept))
	}
	if len(slept) > 0 && slept[0] < time.Second {
		t.Errorf("the retry paused for %v, want long enough for a file handle to be released", slept[0])
	}
	if out := stderr.String(); !strings.Contains(out, cache) {
		t.Errorf("the report does not name the directory that survived:\n%s", out)
	}
}

// TestExecExportsGocacheAndPrintsTheDelta is the wrapper doing its one job.
//
// Everything the suites spend disk on happens in a child: `go build`, `go test
// -c`, `go list`, and — under dogfood — a whole mutation run's worth of them. So
// the wrapper exports the directory into that child rather than trying to
// intercept anything, and it exports it twice: GOCACHE for every `go` command
// the child starts, and GO_MUTANTS_TEST_GOCACHE so that a test which composes a
// hermetic environment of its own through testkit resolves the same directory
// instead of falling back to the default.
//
// The report goes to stderr, and nothing at all goes to stdout, because a caller
// pipes the child's stdout: `exec -- go-mutants run --json > report.json` must
// produce JSON and not JSON with a size line in it.
func TestExecExportsGocacheAndPrintsTheDelta(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	writeTree(t, cache, map[string]int{"seeded-by-an-earlier-run": 100})
	// An earlier run left the marker as well as the entries, which is what makes
	// this a warm cache rather than somebody else's directory.
	stamp(t, cache, buildCacheMarker)
	report := filepath.Join(t.TempDir(), "what-the-child-saw")

	env := helperEnvironment(t, map[string]string{
		helperModeEnv:   "write",
		helperReportEnv: report,
		helperBytesEnv:  "4096",
		buildCacheEnv:   cache,
		keepDirEnv:      filepath.Join(t.TempDir(), "kept"),
	})

	var stdout, stderr bytes.Buffer
	argv := append([]string{"exec", "--"}, helperArgv(t)...)
	if code := run(argv, &stdout, &stderr, env, deps{}); code != 0 {
		t.Fatalf("`exec` exited %d: %s", code, stderr.String())
	}

	saw := readReport(t, report)
	for _, name := range []string{"GOCACHE", buildCacheEnv} {
		if saw[name] != cache {
			t.Errorf("the child saw %s=%q, want the cache %s", name, saw[name], cache)
		}
	}

	// 100 bytes were already there and the child wrote 4096 more.
	want := fmt.Sprintf("testcache: %s %d bytes (%+d)", cache, 4196, 4096)
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("`exec` did not report the growth as %q:\n%s", want, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("`exec` wrote to stdout, which belongs to the child it wraps: %q", stdout.String())
	}
}

// TestExecForwardsTheChildExitStatus keeps the wrapper invisible to CI.
//
// `mise run test-integration` and `mise run dogfood` are gates: the whole point
// of them is that a failing suite or an undeclared survivor fails the job. A
// wrapper that swallowed the status — or that reported its own success after
// cleaning up — would turn both into steps that always pass, and nobody would
// notice until a release. A child killed by a signal has no exit status at all,
// so it gets the shell's convention, 128+N, rather than the -1 the operating
// system reports.
func TestExecForwardsTheChildExitStatus(t *testing.T) {
	t.Parallel()

	for _, want := range []int{0, 1, 3, 97} {
		t.Run(fmt.Sprintf("exit %d", want), func(t *testing.T) {
			t.Parallel()

			env := helperEnvironment(t, map[string]string{
				helperModeEnv: "exit",
				helperExitEnv: strconv.Itoa(want),
				buildCacheEnv: filepath.Join(t.TempDir(), "go-build"),
				keepDirEnv:    filepath.Join(t.TempDir(), "kept"),
			})
			var stdout, stderr bytes.Buffer
			argv := append([]string{"exec", "--"}, helperArgv(t)...)
			if got := run(argv, &stdout, &stderr, env, deps{}); got != want {
				t.Errorf("`exec` exited %d, want the child's %d:\n%s", got, want, stderr.String())
			}
		})
	}

	t.Run("killed by a signal", func(t *testing.T) {
		t.Parallel()

		env := helperEnvironment(t, map[string]string{
			helperModeEnv: "signal",
			buildCacheEnv: filepath.Join(t.TempDir(), "go-build"),
			keepDirEnv:    filepath.Join(t.TempDir(), "kept"),
		})
		var stdout, stderr bytes.Buffer
		argv := append([]string{"exec", "--"}, helperArgv(t)...)
		got := run(argv, &stdout, &stderr, env, deps{})

		if runtime.GOOS == "windows" {
			// Windows has no signals to map: a terminated process carries the
			// exit code the terminator chose, and all this can promise is that
			// it is not mistaken for success.
			if got == 0 {
				t.Errorf("`exec` exited 0 for a child that was terminated")
			}
			return
		}
		if want := 128 + int(syscall.SIGKILL); got != want {
			t.Errorf("`exec` exited %d for a child killed by SIGKILL, want %d", got, want)
		}
	})
}

// TestExecAppliesTheBudgetAfterTheChildAndKeepsItsStatus pins the order of the
// two things `exec` does when the run is over.
//
// The trim is after the child and not before it, because the run that paid to
// fill the cache is the run that should benefit from it — trimming first would
// hand every suite a cold cache and recompile the standard library for nothing.
// And the trim must not touch the status: a suite that failed under a cache that
// happened to be over budget still failed.
func TestExecAppliesTheBudgetAfterTheChildAndKeepsItsStatus(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	env := helperEnvironment(t, map[string]string{
		helperModeEnv:  "exit",
		helperExitEnv:  "7",
		helperBytesEnv: "4096",
		buildCacheEnv:  cache,
		keepDirEnv:     filepath.Join(t.TempDir(), "kept"),
	})

	var cleaned []string
	d := deps{cleaner: func(dir string) error {
		cleaned = append(cleaned, dir)
		return nil
	}}

	var stdout, stderr bytes.Buffer
	argv := append([]string{"exec", "--budget", "1KiB", "--"}, helperArgv(t)...)
	if got := run(argv, &stdout, &stderr, env, d); got != 7 {
		t.Errorf("`exec --budget 1KiB` exited %d, want the child's 7:\n%s", got, stderr.String())
	}
	if !slices.Equal(cleaned, []string{cache}) {
		t.Errorf("`go clean -cache` was run against %q, want it run once against %s", cleaned, cache)
	}
	if _, err := os.Stat(cache); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the cache survived a trim it was over budget for: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("`exec` wrote to stdout, which belongs to the child it wraps: %q", stdout.String())
	}
}

// TestExecRefusesToRunNothing keeps `exec` from silently becoming a `status`:
// `testcache exec --budget 4GiB` with the command left off is a broken task
// definition, and a green exit would hide it for as long as nobody read the log.
func TestExecRefusesToRunNothing(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: t.TempDir(), keepDirEnv: t.TempDir()}
	if code := run([]string{"exec", "--budget", "4GiB", "--"}, &stdout, &stderr, env, deps{}); code == 0 {
		t.Errorf("`exec` with no command exited 0, want a refusal")
	}
	if !strings.Contains(stderr.String(), "command") {
		t.Errorf("the refusal does not say what was missing:\n%s", stderr.String())
	}
}

// The variables that turn this test binary into the child process the `exec`
// tests run.
//
// The binary re-executes itself rather than building a program at test time:
// the fixture then lives beside the assertions that depend on it, it needs no
// toolchain, and it leaves nothing behind. The guard is what keeps an ordinary
// `go test` run from becoming a helper — its presence, not its value, decides.
const (
	helperModeEnv   = "TESTCACHE_TEST_HELPER"
	helperReportEnv = "TESTCACHE_TEST_HELPER_REPORT"
	helperBytesEnv  = "TESTCACHE_TEST_HELPER_BYTES"
	helperExitEnv   = "TESTCACHE_TEST_HELPER_EXIT"
	// helperMisuse is the status a helper that cannot do its job exits with. It
	// is not a status any test asks for, so it can never be mistaken for one.
	helperMisuse = 99
)

// TestExecHelper is not a test.
//
// It is the child process the `exec` tests run: it records the cache-related
// variables it was given, grows the cache by an exact number of bytes, and then
// ends the way the test asked — with a status, or by being killed. Without the
// guard it skips, because in an ordinary run of this package it is only ever
// reached by the framework enumerating tests.
func TestExecHelper(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		t.Skipf("this is the child process the exec tests re-execute, and it does nothing unless %s is set", helperModeEnv)
	}

	if path := os.Getenv(helperReportEnv); path != "" {
		body := "GOCACHE=" + os.Getenv("GOCACHE") + "\n" +
			buildCacheEnv + "=" + os.Getenv(buildCacheEnv) + "\n" +
			// Everything after the program name, so a test can prove the wrapper
			// handed the child its own flags rather than reading them.
			"ARGV=" + strings.Join(os.Args[1:], " ") + "\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "helper: reporting the environment: %v\n", err)
			os.Exit(helperMisuse)
		}
	}

	if size := os.Getenv(helperBytesEnv); size != "" {
		n, err := strconv.Atoi(size)
		if err != nil {
			fmt.Fprintf(os.Stderr, "helper: %s=%q is not a number of bytes\n", helperBytesEnv, size)
			os.Exit(helperMisuse)
		}
		dir := os.Getenv("GOCACHE")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			fmt.Fprintf(os.Stderr, "helper: creating %s: %v\n", dir, err)
			os.Exit(helperMisuse)
		}
		if err := os.WriteFile(filepath.Join(dir, "written-by-the-child"), bytes.Repeat([]byte("x"), n), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "helper: filling the cache: %v\n", err)
			os.Exit(helperMisuse)
		}
	}

	if mode == "signal" {
		self, err := os.FindProcess(os.Getpid())
		if err == nil {
			err = self.Signal(os.Kill)
		}
		fmt.Fprintf(os.Stderr, "helper: this process survived being killed: %v\n", err)
		os.Exit(helperMisuse)
	}

	code, err := strconv.Atoi(os.Getenv(helperExitEnv))
	if err != nil {
		code = 0
	}
	os.Exit(code)
}

// helperArgv re-executes this test binary as [TestExecHelper] and nothing else.
//
// The `-test.run` anchor is what makes it a program rather than a test run:
// without the `^…$` a pattern would also match a future TestExecHelperSomething,
// and the child would run two tests and exit from the wrong one.
func helperArgv(t *testing.T) []string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("locating this test binary to re-execute it: %v", err)
	}
	return []string{binary, "-test.run=^TestExecHelper$"}
}

// helperEnvironment is this process's environment with the helper's switches
// applied, as the map [run] composes a child's environment from.
//
// GOCOVERDIR is pointed at a directory of the test's own. A helper is this
// coverage-instrumented binary re-executed, and it exits without the testing
// package's "the profile is already written" call, so the coverage runtime's
// exit hook fires and writes a covmeta file named after the binary. Left in the
// single directory `go test -cover` exports, the concurrent renames collide and
// every helper but one prints an error onto the stderr this test inherits.
func helperEnvironment(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	env := map[string]string{}
	for _, entry := range os.Environ() {
		if name, value, ok := strings.Cut(entry, "="); ok {
			env[name] = value
		}
	}
	env["GOCOVERDIR"] = t.TempDir()
	for name, value := range extra {
		env[name] = value
	}
	return env
}

// readReport reads back the `name=value` lines [TestExecHelper] wrote.
func readReport(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the child did not report what it was given: %v", err)
	}
	saw := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			saw[name] = value
		}
	}
	return saw
}

// stamp writes the ownership marker a removal needs to find, the way the harness
// and `exec` write it in production.
//
// The file is empty here on purpose: a fixture's byte arithmetic should be about
// the files the test wrote, not about the length of a paragraph in production
// code. That the real marker has a body of its own is asserted where it is
// written, in TestExecStampsTheCacheItWritesInto.
func stamp(t *testing.T, root, marker string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("creating %s: %v", root, err)
	}
	if err := os.WriteFile(filepath.Join(root, marker), nil, 0o600); err != nil {
		t.Fatalf("stamping %s: %v", root, err)
	}
}

// writeTree creates a tree of files of the given sizes, and returns nothing: a
// test that wants the total states it, because a total computed the same way the
// code under test computes it would assert nothing.
func writeTree(t *testing.T, root string, files map[string]int) {
	t.Helper()
	for name, size := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

// TestParseSizeAcceptsBinaryUnits pins the spelling a budget is written in.
//
// The units are binary and only binary. A developer who types `4GB` means four
// gibibytes about as often as they mean four gigabytes, and the two differ by
// 7%: read as decimal, a `4GB` budget wipes a cache that is still 300 MiB below
// what its author intended, which shows up as a suite that recompiles the
// standard library for no visible reason. So the decimal spellings are refused
// with a message naming the accepted ones rather than quietly reinterpreted.
func TestParseSizeAcceptsBinaryUnits(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		text string
		want int64
	}{
		{"0", 0},
		{"4096", 4096},
		{"512B", 512},
		{"512 b", 512},
		{"4KiB", 4 << 10},
		{"64MiB", 64 << 20},
		{"4GiB", 4 << 30},
		{" 4gib ", 4 << 30},
		{"1.5GiB", 3 << 29},
		{"2TiB", 2 << 40},
	} {
		got, err := parseSize(tc.text)
		if err != nil {
			t.Errorf("parseSize(%q): %v", tc.text, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseSize(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}

	// The refusals. The last six are the ones that matter most, and they are the
	// reason this is not a clamp: a budget that overflows int64 wraps to
	// math.MinInt64, every measured total is then "over budget", and a tool
	// whose entire job is keeping one cache warm quietly empties it on every
	// run instead. NaN is the same defect wearing a different hat — every
	// comparison against it is false, so `used.bytes <= budget` is false and the
	// wipe happens. A budget nobody could have meant is a typo, and a typo
	// should be visible rather than absorbed.
	for _, text := range []string{
		"", "   ", "4GB", "4kb", "-1", "-1GiB", "GiB", "four", "4 GiB extra",
		"nan", "NaN", "inf", "-inf", "infGiB",
		"9223372036854775807", // the largest int64, which no float64 can name
		"9223372036854775808", // exactly 2^63, one past it
		"8388608TiB",          // the same number written with a unit
		"9007199254740992TiB",
		"1e30", "1e300GiB",
	} {
		got, err := parseSize(text)
		if err == nil {
			t.Errorf("parseSize(%q) = %d, want a refusal", text, got)
			continue
		}
		if !strings.Contains(err.Error(), "GiB") {
			t.Errorf("the refusal of %q does not name an accepted unit: %v", text, err)
		}
	}

	// The bound is a refusal of what cannot be held rather than a lower ceiling
	// nobody wrote down: the largest budget a float64 can name below 2^63 is
	// still accepted exactly. (The last 1024 bytes below the int64 ceiling are
	// not representable as a float64 at all, so a budget written inside them is
	// refused rather than rounded to something its author did not type. Nobody
	// budgets eight exbibytes; what matters is that the refusal is a refusal and
	// never a negative number.)
	if got, err := parseSize("9223372036854774784"); err != nil || got != 9223372036854774784 {
		t.Errorf("parseSize(the largest representable budget) = %d (%v), want 9223372036854774784", got, err)
	}
}

// TestMeasureCountsWhatItCanReadAndSaysWhatItCouldNot is about the budget being
// decided on a number that means what it says.
//
// A walk that stops at the first unreadable entry returns whatever it had added
// up so far — and that partial total is then compared against the budget as if
// it were the size of the cache. One unreadable subdirectory near the top of a
// 6 GiB cache therefore reports 200 MiB, passes a 4 GiB budget, and keeps
// passing it for as long as the directory stays unreadable: the cache never
// gets trimmed and nothing anywhere says why. So the walk continues past what it
// cannot read, the total is everything it could, and the error comes back beside
// it so the report can say the number is a floor rather than a size.
func TestMeasureCountsWhatItCanReadAndSaysWhatItCouldNot(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("a directory with no permissions is not how Windows makes one unreadable")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads directories whose permissions say otherwise")
	}

	cache := filepath.Join(t.TempDir(), "go-build")
	// Named so that the unreadable one is walked first: a total that included the
	// readable entry could otherwise mean the walk stopped afterwards.
	writeTree(t, cache, map[string]int{"aa-locked/entry": 512, "bb-open/entry": 4096})
	stamp(t, cache, buildCacheMarker)
	locked := filepath.Join(cache, "aa-locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("making %s unreadable: %v", locked, err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	used, err := measure(cache)
	if err == nil {
		t.Errorf("measure returned no error for a tree it could not read completely")
	} else if !strings.Contains(err.Error(), "aa-locked") {
		t.Errorf("the error does not name what could not be read: %v", err)
	}
	if used.bytes < 4096 {
		t.Errorf("measure counted %d bytes, want at least the 4096 it could read: the walk stopped "+
			"at the entry it could not open", used.bytes)
	}

	// And the decision made on that total says so, because a cache that is over
	// its budget and survives is otherwise inexplicable.
	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: cache, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
	d := deps{cleaner: func(string) error { return nil }}
	if code := run([]string{"trim", "--budget", "8KiB"}, &stdout, &stderr, env, d); code != 0 {
		t.Fatalf("`trim` exited %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "counting only what could be read") {
		t.Errorf("the trim report does not say the total was partial:\n%s", stdout.String())
	}
}

// TestWipeRefusesADirectoryItDoesNotOwn is the guard that stands between a
// mistyped environment variable and somebody's home directory.
//
// `GO_MUTANTS_TEST_GOCACHE=$HOME` used to be enough: the value was absolute, so
// it was accepted, and `clean` then ran `go clean -cache` and os.RemoveAll
// against it. Nothing in the tool knew the difference between a directory it had
// created and a directory that merely got named. The marker file is that
// difference — the harness writes it when it resolves the cache, this tool
// refuses to remove anything that does not carry it, and the worst outcome of
// the whole scheme becomes a directory that grows rather than one that vanishes.
//
// The three subcommands end differently on purpose. `clean` is a person asking
// for a removal, so being unable to do it is a failure. `trim` and `exec` are
// housekeeping around somebody else's run: they report and get out of the way,
// because a collector that fails a green suite over a directory it did not
// recognise is worse than the directory.
//
// A refusal is the *only* thing that makes `clean` exit non-zero. A removal it
// began and could not finish is reported and forgiven — see
// TestCleanReportsARemovalItCannotFinish, which is the other half of this pair.
func TestWipeRefusesADirectoryItDoesNotOwn(t *testing.T) {
	t.Parallel()

	// The reviewer's demonstration, as a fixture: a directory full of somebody's
	// work, named by a variable that was meant to name a cache.
	setup := func(t *testing.T) (string, deps, *[]string) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "pretend")
		writeTree(t, dir, map[string]int{"thesis.txt": 4096, "chapters/three.txt": 512})
		recorded := &[]string{}
		return dir, deps{cleaner: func(name string) error {
			*recorded = append(*recorded, name)
			return nil
		}}, recorded
	}
	survives := func(t *testing.T, dir string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(dir, "thesis.txt")); err != nil {
			t.Errorf("the file this tool had no business touching is gone: %v", err)
		}
	}

	t.Run("clean fails", func(t *testing.T) {
		t.Parallel()
		dir, d, cleaned := setup(t)

		var stdout, stderr bytes.Buffer
		env := map[string]string{buildCacheEnv: dir, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
		if code := run([]string{"clean"}, &stdout, &stderr, env, d); code == 0 {
			t.Errorf("`clean` exited 0 over a directory it does not own, want a failure")
		}
		survives(t, dir)
		if len(*cleaned) != 0 {
			t.Errorf("`go clean -cache` was run against %q, which would have emptied it before the guard could speak", *cleaned)
		}
		for _, needle := range []string{dir, buildCacheMarker} {
			if !strings.Contains(stderr.String(), needle) {
				t.Errorf("the refusal does not name %q:\n%s", needle, stderr.String())
			}
		}
	})

	t.Run("trim reports and succeeds", func(t *testing.T) {
		t.Parallel()
		dir, d, cleaned := setup(t)

		var stdout, stderr bytes.Buffer
		env := map[string]string{buildCacheEnv: dir, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
		if code := run([]string{"trim", "--budget", "0"}, &stdout, &stderr, env, d); code != 0 {
			t.Errorf("`trim` exited %d over a directory it does not own, want 0 and a report", code)
		}
		survives(t, dir)
		if len(*cleaned) != 0 {
			t.Errorf("`go clean -cache` was run against %q", *cleaned)
		}
		if !strings.Contains(stdout.String()+stderr.String(), dir) {
			t.Errorf("the report does not name the directory:\n%s%s", stdout.String(), stderr.String())
		}
	})

	t.Run("exec keeps the child's status", func(t *testing.T) {
		t.Parallel()
		dir, d, _ := setup(t)

		env := helperEnvironment(t, map[string]string{
			helperModeEnv: "exit",
			helperExitEnv: "3",
			buildCacheEnv: dir,
			keepDirEnv:    filepath.Join(t.TempDir(), "kept"),
		})
		var stdout, stderr bytes.Buffer
		argv := append([]string{"exec", "--budget", "0", "--"}, helperArgv(t)...)
		if code := run(argv, &stdout, &stderr, env, d); code != 3 {
			t.Errorf("`exec` exited %d over a directory it does not own, want the child's 3", code)
		}
		survives(t, dir)

		// And it did not write itself a permission slip on the way in. This is
		// the sharp edge of the whole scheme: `exec` stamps the cache it is about
		// to fill, and a stamp written into whatever the variable happened to name
		// would make `--budget 0` delete the directory two lines later — with this
		// tool's own marker as the licence.
		if _, err := os.Stat(filepath.Join(dir, buildCacheMarker)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("`exec` stamped a directory full of somebody else's files: %v", err)
		}
		if !strings.Contains(stderr.String(), "not") {
			t.Errorf("`exec` did not say it had left the directory unstamped:\n%s", stderr.String())
		}
	})

	t.Run("status says why", func(t *testing.T) {
		t.Parallel()
		dir, _, _ := setup(t)

		var stdout, stderr bytes.Buffer
		env := map[string]string{buildCacheEnv: dir, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
		if code := run([]string{"status"}, &stdout, &stderr, env, deps{}); code != 0 {
			t.Fatalf("`status` exited %d: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "not stamped") {
			t.Errorf("`status` does not say the directory carries no marker, so a refused `clean` has "+
				"nowhere to send the reader:\n%s", stdout.String())
		}
	})
}

// TestHarnessDirectoryRefusesRootHomeAndTheUsersGoCache is the belt to the
// marker's braces.
//
// A marker cannot help with a directory that carries one and should still never
// be named: these are refused by their identity rather than by their contents,
// before anything is measured, stamped or removed. The user's own
// `<cache>/go-build` is in the list because it is the exact directory this whole
// tool exists to keep the suites out of, and a variable pointing back at it
// would quietly reinstate the 14 GB problem while looking configured.
func TestHarnessDirectoryRefusesRootHomeAndTheUsersGoCache(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cacheRoot := t.TempDir()
	d := deps{
		userCache: func() (string, error) { return cacheRoot, nil },
		userHome:  func() (string, error) { return home, nil },
	}

	for _, tc := range []struct {
		name     string
		dir      string
		variable string
		needle   string
	}{
		{name: "the filesystem root", dir: filesystemRoot(t), variable: buildCacheEnv, needle: "root"},
		{name: "the home directory", dir: home, variable: buildCacheEnv, needle: "home"},
		{name: "the user's own build cache", dir: filepath.Join(cacheRoot, "go-build"), variable: buildCacheEnv, needle: "go clean -cache"},
		{name: "the kept root at home", dir: home, variable: keepDirEnv, needle: "home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			env := map[string]string{buildCacheEnv: cacheRoot, keepDirEnv: cacheRoot, tc.variable: tc.dir}
			// `status` resolves both directories, so it reaches either guard.
			if code := run([]string{"status"}, &stdout, &stderr, env, d); code == 0 {
				t.Errorf("%s=%s was accepted:\n%s", tc.variable, tc.dir, stdout.String())
			}
			out := stderr.String()
			if !strings.Contains(out, tc.dir) || !strings.Contains(out, tc.needle) {
				t.Errorf("the refusal does not name both %q and %q:\n%s", tc.dir, tc.needle, out)
			}
		})
	}
}

// filesystemRoot walks up from a real directory until it cannot go further, so
// that the test names a root on every platform rather than assuming "/".
func filesystemRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// TestExecStampsTheCacheItWritesInto is the other half of the ownership rule:
// something has to write the marker, and it has to be whatever created the
// directory.
//
// The harness writes it when a test resolves the cache; this writes it when a
// run is wrapped, because `exec` is the one path that fills a cache without any
// of this repository's own tests being involved — a dogfood run's children are
// `go` commands, and a `go` command has never heard of the marker. Both are
// idempotent, because between them they run thousands of times per suite.
func TestExecStampsTheCacheItWritesInto(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	env := helperEnvironment(t, map[string]string{
		helperModeEnv:  "exit",
		helperBytesEnv: "512",
		buildCacheEnv:  cache,
		keepDirEnv:     filepath.Join(t.TempDir(), "kept"),
	})

	for _, pass := range []string{"first", "second"} {
		var stdout, stderr bytes.Buffer
		argv := append([]string{"exec", "--"}, helperArgv(t)...)
		if code := run(argv, &stdout, &stderr, env, deps{}); code != 0 {
			t.Fatalf("the %s `exec` exited %d: %s", pass, code, stderr.String())
		}
		marker := filepath.Join(cache, buildCacheMarker)
		data, err := os.ReadFile(marker)
		if err != nil {
			t.Fatalf("after the %s run, %s: %v", pass, marker, err)
		}
		if len(data) == 0 {
			t.Errorf("the marker is empty, and it is the only explanation a person who finds this directory gets")
		}
	}

	// And the stamp licenses the removal it exists for.
	var stdout, stderr bytes.Buffer
	env2 := map[string]string{buildCacheEnv: cache, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
	d := deps{cleaner: func(string) error { return nil }}
	if code := run([]string{"clean"}, &stdout, &stderr, env2, d); code != 0 {
		t.Errorf("`clean` exited %d over the directory `exec` had stamped: %s", code, stderr.String())
	}
	if _, err := os.Stat(cache); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the stamped cache survived `clean`: %v", err)
	}
}

// TestASymlinkedCacheRootIsMeasuredAndWipedByItsContents covers the arrangement
// a developer on a small root disk actually builds: the cache is a link to
// somewhere with room on it.
//
// Unresolved, os.Stat says the path exists, WalkDir walks the link itself and
// reports 24 bytes in one "file", so every budget passes — and the removal takes
// the link and leaves the gigabytes behind it untouched, which makes the cache
// both never trimmed and permanently cold.
func TestASymlinkedCacheRootIsMeasuredAndWipedByItsContents(t *testing.T) {
	t.Parallel()

	behind := filepath.Join(t.TempDir(), "somewhere-with-room")
	writeTree(t, behind, map[string]int{"ab/entry": 4096})
	stamp(t, behind, buildCacheMarker)
	link := filepath.Join(t.TempDir(), "go-build")
	if err := os.Symlink(behind, link); err != nil {
		t.Skipf("this machine does not let a test create a symlink: %v", err)
	}

	var stdout, stderr bytes.Buffer
	env := map[string]string{buildCacheEnv: link, keepDirEnv: filepath.Join(t.TempDir(), "kept")}
	if code := run([]string{"status"}, &stdout, &stderr, env, deps{}); code != 0 {
		t.Fatalf("`status` exited %d: %s", code, stderr.String())
	}
	// Unresolved, this would read "24 B (24 bytes) in 1 file" — the link itself.
	if !strings.Contains(stdout.String(), "4.0 KiB") || !strings.Contains(stdout.String(), "2 files") {
		t.Errorf("`status` measured the link rather than what is behind it:\n%s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	d := deps{cleaner: func(string) error { return nil }}
	if code := run([]string{"clean"}, &stdout, &stderr, env, d); code != 0 {
		t.Fatalf("`clean` exited %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(behind, "ab", "entry")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("`clean` removed the link and left the cache behind it: %v", err)
	}
}

// TestExecRefusesAnEmptyBudget closes the gap between the two subcommands that
// take one.
//
// `trim --budget ""` is refused and `exec --budget ""` used to mean "no budget
// at all", which is the worst of the three possible readings: a task definition
// with an unset shell variable in it — `--budget "$BUDGET"` — would have run for
// months with no ceiling and nothing in the log to say so. An absent flag is
// still no budget, because that is a caller who never asked for one.
func TestExecRefusesAnEmptyBudget(t *testing.T) {
	t.Parallel()

	env := helperEnvironment(t, map[string]string{
		helperModeEnv: "exit",
		buildCacheEnv: filepath.Join(t.TempDir(), "go-build"),
		keepDirEnv:    filepath.Join(t.TempDir(), "kept"),
	})
	var stdout, stderr bytes.Buffer
	argv := append([]string{"exec", "--budget", "", "--"}, helperArgv(t)...)

	if code := run(argv, &stdout, &stderr, env, deps{}); code == 0 {
		t.Errorf("`exec --budget \"\"` exited 0, want a refusal")
	}
	if !strings.Contains(stderr.String(), "budget") {
		t.Errorf("the refusal does not mention the budget:\n%s", stderr.String())
	}
}

// TestExecPassesTheChildItsOwnFlags is why the argv is split at `--`.
//
// The command this wraps has flags of its own, and one of them is spelled the
// same as this one: `go-mutants run --budget …` is a plausible future, and
// `go test -budget` is not, but neither is the point. The point is that
// everything after `--` is the child's, verbatim, and a wrapper that parsed it
// would silently steal an argument and change what the run measured.
func TestExecPassesTheChildItsOwnFlags(t *testing.T) {
	t.Parallel()

	cache := filepath.Join(t.TempDir(), "go-build")
	report := filepath.Join(t.TempDir(), "what-the-child-saw")
	env := helperEnvironment(t, map[string]string{
		helperModeEnv:   "exit",
		helperReportEnv: report,
		buildCacheEnv:   cache,
		keepDirEnv:      filepath.Join(t.TempDir(), "kept"),
	})

	// The `--` in the middle is the child's own, not this tool's: the child here
	// is a Go test binary, whose flag parser refuses a flag it does not know
	// until one tells it to stop reading them. What is being asserted is that
	// everything after the *wrapper's* `--` arrived verbatim.
	var stdout, stderr bytes.Buffer
	argv := append(append([]string{"exec", "--budget", "4GiB", "--"}, helperArgv(t)...), "--", "--budget", "8MiB")
	if code := run(argv, &stdout, &stderr, env, deps{}); code != 0 {
		t.Fatalf("`exec` exited %d: %s", code, stderr.String())
	}

	if got, want := readReport(t, report)["ARGV"], "-test.run=^TestExecHelper$ -- --budget 8MiB"; got != want {
		t.Errorf("the child was given %q, want %q", got, want)
	}
	if !strings.Contains(stderr.String(), "under the budget of 4.0 GiB") {
		t.Errorf("the wrapper did not apply its own 4GiB budget:\n%s", stderr.String())
	}
}

// TestExecReportsACommandItCannotStart separates the two failures a caller has
// to tell apart: a child that ran and failed, and a command that was never
// there at all.
//
// 127 is what a shell reports for the second, and a wrapper that returned 1 for
// both would turn a typo in a mise task into something that looks exactly like
// a failing test suite.
func TestExecReportsACommandItCannotStart(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "no-such-command")
	env := map[string]string{
		buildCacheEnv: filepath.Join(t.TempDir(), "go-build"),
		keepDirEnv:    filepath.Join(t.TempDir(), "kept"),
	}
	var stdout, stderr bytes.Buffer

	if code := run([]string{"exec", "--", missing}, &stdout, &stderr, env, deps{}); code != 127 {
		t.Errorf("`exec` exited %d for a command that does not exist, want 127", code)
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Errorf("the report does not name the command it could not run:\n%s", stderr.String())
	}
}
