// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The two variables this tool and internal/testkit both read.
//
// They are spelled out here rather than imported, and that is not an oversight:
// testkit is a test-only package, nothing that ships may import it — the import
// gate parses every production file in the tree to say so — and this is a
// production `main`. The agreement between the two copies is a test rather than
// a compiler check: internal/testkit's TestPathAgreesWithTestkit runs this
// program and compares what it prints with what testkit.BuildCache resolved,
// with and without the variable set, so a change to either rule fails there.
const (
	// buildCacheEnv names the directory the suites' child `go` commands use as
	// GOCACHE. CI points it at the runner's own temporary area.
	buildCacheEnv = "GO_MUTANTS_TEST_GOCACHE"
	// keepDirEnv names the root a kept scratch directory is filed under.
	keepDirEnv = "GO_MUTANTS_TEST_KEEP_DIR"
)

// The directory names below the platform's cache root. One parent holds both,
// so that a developer who wants everything this harness ever wrote can delete
// `<cache root>/go-mutants-test` and be done.
const (
	harnessDir   = "go-mutants-test"
	buildCacheIn = "go-build"
	keptIn       = "kept"
)

// The ownership markers, and the whole reason this tool is safe to point at a
// directory a person typed.
//
// Absolute is not the same as ours. `GO_MUTANTS_TEST_GOCACHE=$HOME` is an
// absolute path, and before these files existed it was enough to have `clean`
// run `go clean -cache` and os.RemoveAll against a home directory. Nothing in a
// path says who made it, so ownership is written down instead: the harness
// stamps the build cache when it resolves it, `exec` stamps it before the run it
// wraps, and nothing here removes a directory that exists without its marker.
// The worst outcome of the scheme is a cache that grows — which is the failure
// this tool was written to notice, and the opposite of the one it could cause.
//
// buildCacheMarker is duplicated in internal/testkit as BuildCacheMarker, for
// the same reason the path rule is: a test-only package cannot be imported from
// production code. internal/testkit's TestMarkerNamesAgreeWithTestcache runs
// `path --marker` and compares the two.
const (
	buildCacheMarker = ".go-mutants-testcache"
	keptMarker       = ".go-mutants-kept"
)

// The tool's own exit codes. `exec` does not use them: it exits with its
// child's status, which is the whole point of it.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, environment(), deps{}))
}

// environment reads the process's environment as the map [run] resolves paths
// from and composes a child's environment out of.
func environment() map[string]string {
	env := make(map[string]string, len(os.Environ()))
	for _, entry := range os.Environ() {
		if name, value, ok := strings.Cut(entry, "="); ok {
			env[name] = value
		}
	}
	return env
}

// deps are the things a unit test replaces, and there are only three because
// everything else this tool does — walking a tree, removing it, starting a
// child — is exactly what the test wants to observe for real.
type deps struct {
	// cleaner runs the toolchain's own cache eviction against dir. A unit test
	// records the call instead of paying for a `go` command; the integration
	// test uses the real one.
	cleaner func(dir string) error
	// userCache is os.UserCacheDir, so a test can resolve the default path
	// without moving the home directory the rest of the process is reading.
	userCache func() (string, error)
	// userHome is os.UserHomeDir, for the guard that refuses to treat a home
	// directory as a cache. A test can then prove the refusal against a home of
	// its own rather than against the developer's real one.
	userHome func() (string, error)
	// sleep is the pause before the single removal retry, so the test of that
	// path costs microseconds rather than two seconds.
	sleep func(d time.Duration)
}

// withDefaults fills in the real implementations, so that the zero deps is the
// production one and a test replaces only the field it is about.
func (d deps) withDefaults() deps {
	if d.cleaner == nil {
		d.cleaner = goCleanCache
	}
	if d.userCache == nil {
		d.userCache = os.UserCacheDir
	}
	if d.userHome == nil {
		d.userHome = os.UserHomeDir
	}
	if d.sleep == nil {
		d.sleep = time.Sleep
	}
	return d
}

// run is the whole program, over streams and an environment a test supplies.
//
// Every subcommand is reachable in process: the streams are writers rather than
// the process's own files, the environment is a map rather than a read of the
// process, and the two effects worth faking are in [deps]. That is what lets the
// budget, the status arithmetic and the exit-code mapping be unit tests instead
// of a shell script nobody runs.
func run(args []string, stdout, stderr io.Writer, env map[string]string, d deps) int {
	d = d.withDefaults()
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch name := args[0]; name {
	case "path":
		return runPath(args[1:], stdout, stderr, env, d)
	case "status":
		return runStatus(args[1:], stdout, stderr, env, d)
	case "clean":
		return runClean(args[1:], stdout, stderr, env, d)
	case "trim":
		return runTrim(args[1:], stdout, stderr, env, d)
	case "exec":
		return runExec(args[1:], stderr, env, d)
	case "help", "-h", "-help", "--help":
		usage(stdout)
		return exitOK
	default:
		printf(stderr, "testcache: unknown subcommand %q\n", name)
		usage(stderr)
		return exitUsage
	}
}

// usage is the whole command tree in one screen.
func usage(w io.Writer) {
	printf(w, "%s", `testcache keeps the test suites' `+"`go`"+` commands out of the developer's own build cache.

usage:
  testcache path [--kept] [--marker]   print the resolved directory, the kept
                                       scratch root, or a marker file's name
  testcache status                     print the cache and kept-scratch sizes
  testcache clean                      empty the cache and the kept scratch root
  testcache trim --budget <size>       empty the cache only if it is over budget
  testcache exec [--budget <size>] -- <cmd...>
                                       run <cmd...> with GOCACHE pointed at the
                                       cache, report the growth, then trim

The directory is `+buildCacheEnv+` when it names an absolute path,
and <os.UserCacheDir()>/`+harnessDir+`/`+buildCacheIn+` otherwise; the kept
scratch root is `+keepDirEnv+` or <os.UserCacheDir()>/`+harnessDir+`/`+keptIn+`.
Sizes are binary: B, KiB, MiB, GiB, TiB.

Nothing here removes a directory that does not hold its marker file (`+buildCacheMarker+`
for the cache, `+keptMarker+` for the kept root). The harness writes it when a
test resolves the cache, and `+"`exec`"+` writes it before the run it wraps -- but
never into a directory that already holds files that are not the harness's.
`)
}

// printf writes one line of the tool's own output.
//
// The error is dropped, and that is the whole reason this exists rather than a
// bare fmt.Fprintf at every call site: the two writers are the process's own
// stdout and stderr, a write to either fails only when the caller has closed the
// stream or filled the disk, and there is nowhere left to report that to. The
// exit code still carries the outcome, which is what a caller actually reads.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// runPath prints one of the four facts the rest of the harness has to agree
// with: either directory, and either marker's name.
//
// It exists so that a shell, a CI step or a developer can name what this tool
// operates on without knowing the rule — `du -sh "$(testcache path)"` — and,
// more importantly, so that internal/testkit can compare its own copy of the
// rule against this one from a test. That is the only mechanism keeping the two
// copies equal, since the test-only package may not be imported here.
func runPath(args []string, stdout, stderr io.Writer, env map[string]string, d deps) int {
	flags := flag.NewFlagSet("path", flag.ContinueOnError)
	flags.SetOutput(stderr)
	kept := flags.Bool("kept", false, "print the kept scratch root rather than the build cache")
	marker := flags.Bool("marker", false, "print the name of the ownership marker file rather than a directory")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if code := requireNoArguments("path", flags.Args(), stderr); code != exitOK {
		return code
	}

	// The marker's name is a constant, so it is answered without resolving
	// anything: a caller asking what the file is called deserves an answer even
	// on a machine where the directory itself cannot be resolved.
	if *marker {
		printf(stdout, "%s\n", markerFor(*kept))
		return exitOK
	}

	where, err := resolveTarget(env, d, *kept)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}
	printf(stdout, "%s\n", where.dir)
	return exitOK
}

// markerFor is the marker belonging to one of the two directories.
func markerFor(kept bool) string {
	if kept {
		return keptMarker
	}
	return buildCacheMarker
}

// resolveTarget resolves one of the two directories.
func resolveTarget(env map[string]string, d deps, kept bool) (target, error) {
	if kept {
		return keptTarget(env, d)
	}
	return buildCacheTarget(env, d)
}

// runStatus prints what the harness is holding: the build cache, and the kept
// scratch root beside it.
//
// It is the last line of every long run and the first thing anybody looks at
// when a disk fills, so it says four things about each directory — where it is,
// whether it exists, how big it is in both spellings, and how many files that
// is. The two spellings are not redundant: the human one is what a person reads,
// the exact one is what two runs can be subtracted from each other, and the file
// count is the difference between a cache that is one `rm -rf` and a cache that
// is four hundred thousand inodes.
//
// A measurement that partly failed is reported rather than fatal. This runs as
// a CI step with `if: always()`, after the step that actually matters, and a
// diagnostic that turns a red job into a differently red job has told nobody
// anything. A directory that cannot be *resolved* is the other case, and does
// fail: there is nothing to report at all.
func runStatus(args []string, stdout, stderr io.Writer, env map[string]string, d deps) int {
	if code := requireNoArguments("status", args, stderr); code != exitOK {
		return code
	}
	cache, err := buildCacheTarget(env, d)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}
	kept, err := keptTarget(env, d)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}
	reportUsage(stdout, stderr, cache)
	reportUsage(stdout, stderr, kept)
	return exitOK
}

// runClean empties both directories the harness owns: `mise run test-clean`.
//
// Both of them, because they are the only two places the suites write outside a
// temporary directory, and a developer reclaiming disk space should not have to
// learn that there were two. The kept scratch root goes here rather than into
// `trim`, because `clean` is a person asking for exactly this and a trim is a
// budget being enforced on something else entirely.
func runClean(args []string, stdout, stderr io.Writer, env map[string]string, d deps) int {
	if code := requireNoArguments("clean", args, stderr); code != exitOK {
		return code
	}
	cache, err := buildCacheTarget(env, d)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}
	kept, err := keptTarget(env, d)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}

	// Both removals are attempted before either verdict is returned: a person
	// running this wants to know about both directories, not about the first one
	// that went wrong.
	removed := wipe(stdout, stderr, cache, d)
	keptUsed, err := measure(kept.dir)
	if err != nil {
		printf(stderr, "testcache: %s could not be measured completely: %v\n", kept, err)
	}
	if !removeTree(stdout, stderr, kept, keptUsed, d) {
		removed = false
	}
	// Exactly one thing fails this command, and it is worth being precise about
	// which, because the two ways a removal does not happen deserve opposite
	// answers. A directory this tool will not touch — no marker — is a refusal:
	// `clean` is a person asking for a removal, so being unable to do it is the
	// answer to their question, and it is reported and returned as a failure. A
	// directory it tried and could not finish removing — a file still open — is
	// reported and forgiven inside [removeTree], because a locked file is not a
	// reason to fail a suite that has just gone green. `trim` and `exec` forgive
	// both, because they are housekeeping around somebody else's run.
	if !removed {
		return exitFailure
	}
	return exitOK
}

// runTrim enforces a budget, and does nothing at all below it.
//
// The budget is what makes one persistent shared cache safe to keep. Without it
// the directory grows for as long as the machine lives — the suites key entries
// on absolute paths that exist for a single run, which is how a developer's own
// cache reached 14 GB — and with an unconditional wipe it stops being a cache,
// because the standard library is then recompiled once per suite. So the wipe is
// conditional and it happens *after* a run rather than before one: the run that
// paid to fill the cache is the run that gets to use it.
func runTrim(args []string, stdout, stderr io.Writer, env map[string]string, d deps) int {
	flags := flag.NewFlagSet("trim", flag.ContinueOnError)
	flags.SetOutput(stderr)
	budget := flags.String("budget", "", "empty the cache when it holds more than this (B, KiB, MiB, GiB, TiB)")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if code := requireNoArguments("trim", flags.Args(), stderr); code != exitOK {
		return code
	}
	limit, err := parseSize(*budget)
	if err != nil {
		printf(stderr, "testcache: --budget: %v\n", err)
		return exitUsage
	}
	cache, err := buildCacheTarget(env, d)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}
	trimToBudget(stdout, stderr, cache, limit, d)
	return exitOK
}

// runExec is the wrapper the long tasks run through: it exports the cache into
// one child, reports what that child cost, and gets out of the way.
//
// Everything the suites spend disk on happens in a child — `go build`, `go test
// -c`, `go list`, and under dogfood a whole mutation run's worth of them — so
// the directory is exported rather than intercepted, and it is exported twice.
// GOCACHE is what every `go` command in the tree below reads.
// GO_MUTANTS_TEST_GOCACHE is what internal/testkit reads when a test composes a
// hermetic environment of its own: without it, the very tests that redirect
// their environment would send their children back to the default cache, which
// is the developer's.
//
// Everything this subcommand says goes to stderr, and nothing at all to stdout.
// A caller pipes the child — `exec -- go-mutants run --json > report.json` — and
// a size line in that file would be a defect in whatever read it next.
func runExec(args []string, stderr io.Writer, env map[string]string, d deps) int {
	flags := flag.NewFlagSet("exec", flag.ContinueOnError)
	flags.SetOutput(stderr)
	budget := flags.String("budget", "", "empty the cache afterwards if it holds more than this (B, KiB, MiB, GiB, TiB)")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	argv := flags.Args()
	if len(argv) == 0 {
		printf(stderr, "testcache: `exec` needs a command to run: testcache exec [--budget <size>] -- <cmd...>\n")
		return exitUsage
	}

	// An absent --budget is a caller who did not ask for one; an empty --budget
	// is a caller who asked with something they thought had a value in it. The
	// two used to be the same thing here, and that is how a task definition
	// reading `--budget "$BUDGET"` from an unset variable would have run for
	// months with no ceiling and nothing in the log to say so. `trim` has always
	// refused it; so does this.
	limit := int64(-1)
	if flagWasGiven(flags, "budget") {
		parsed, err := parseSize(*budget)
		if err != nil {
			printf(stderr, "testcache: --budget: %v\n", err)
			return exitUsage
		}
		limit = parsed
	}

	cache, err := buildCacheTarget(env, d)
	if err != nil {
		printf(stderr, "testcache: %v\n", err)
		return exitFailure
	}

	// Created and stamped before the child rather than left for the go command
	// to create: the marker is what licenses every later removal, and `exec` is
	// the one path that fills a cache without any of this repository's own tests
	// being involved — the children of a dogfood run are `go` commands, and a
	// `go` command has never heard of the marker. A cache that cannot be created
	// stops the run here, because the child was about to fail on it anyway and a
	// message naming the directory is worth more than the compiler error that
	// would otherwise arrive instead.
	stamped, stampErr := stampDirectory(cache)
	switch {
	case stampErr != nil:
		printf(stderr, "testcache: %v\n", stampErr)
		return exitFailure
	case !stamped:
		printf(stderr, "testcache: %s already holds files that are not the harness's, so it was not "+
			"stamped: the run below will use it and nothing here will ever empty it. Point %s at a "+
			"directory of its own if that is not what you meant.\n", cache, cache.variable)
	}

	before, err := measure(cache.dir)
	if err != nil {
		printf(stderr, "testcache: %s could not be measured completely before the run: %v\n", cache, err)
	}
	code := spawn(argv, cache.dir, env, stderr)
	after, err := measure(cache.dir)
	if err != nil {
		printf(stderr, "testcache: %s could not be measured completely: %v\n", cache, err)
	}
	printf(stderr, "testcache: %s %d bytes (%+d)\n", cache.dir, after.bytes, after.bytes-before.bytes)

	// After the child, never before it: the run that paid to fill the cache is
	// the run that gets to use it, and a trim on the way in would hand every
	// suite a cold cache and recompile the standard library for nothing.
	if limit >= 0 {
		trimToBudget(stderr, stderr, cache, limit, d)
	}
	return code
}

// flagWasGiven reports whether a flag was written on the command line, as
// opposed to holding its zero value because nobody mentioned it.
//
// flag.FlagSet.Visit walks only what was set, which is the only way to tell
// `--budget ""` from no `--budget` at all — and those two mean opposite things.
func flagWasGiven(flags *flag.FlagSet, name string) bool {
	given := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

// spawn runs the child with the cache exported into it, and returns the status
// a shell would have reported.
//
// The three streams are the process's own files rather than the writers [run]
// was given, and that is deliberate: a pipe in place of the terminal would take
// the progress display away from `go test`, take the TUI away from a dogfood
// run, and take the keyboard away from anything that asks. This wrapper is
// supposed to be invisible.
func spawn(argv []string, cache string, env map[string]string, stderr io.Writer) int {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = childEnvironment(env, cache)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	err := cmd.Run()
	var exited *exec.ExitError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &exited):
		return childStatus(exited.ProcessState)
	default:
		// 127 is what a shell reports for a command it could not run, and this
		// is the same failure: the argv named something that is not there.
		printf(stderr, "testcache: %s could not be run: %v\n", argv[0], err)
		return 127
	}
}

// childStatus maps a finished child onto an exit code.
//
// A child killed by a signal has no exit status at all, and the operating system
// reports -1 for one; the shells' convention of 128+N is what every CI log, every
// `$?` and every reader expects instead. syscall.WaitStatus is defined on Windows
// too — where Signaled() is always false — so there is no platform switch here.
func childStatus(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	if code := state.ExitCode(); code >= 0 {
		return code
	}
	return exitFailure
}

// childEnvironment is the environment the child gets: the caller's own, with
// the two cache variables replaced rather than added.
//
// Replaced, because a duplicate name is resolved differently by os/exec, by the
// C library and by a shell, and "the last one wins" is not something the tasks
// that run through this wrapper should have to know. The entries are sorted so
// that two runs of the same command compose the same environment, which is one
// less thing to wonder about when only one of them worked.
func childEnvironment(env map[string]string, cache string) []string {
	child := make([]string, 0, len(env)+2)
	for name, value := range env {
		if sameEnvName(name, "GOCACHE") || sameEnvName(name, buildCacheEnv) {
			continue
		}
		child = append(child, name+"="+value)
	}
	slices.Sort(child)
	return append(child, "GOCACHE="+cache, buildCacheEnv+"="+cache)
}

// sameEnvName reports whether two names are the same environment variable.
//
// Case is folded only on Windows, where the environment really is
// case-insensitive: folding it everywhere would conflate HOME with `home`, which
// are two different variables on POSIX and both of which internal/testkit sets.
func sameEnvName(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// trimToBudget empties the cache only when it is larger than the budget.
//
// A partial measurement is said out loud rather than used silently. If part of
// the tree could not be read, the total is a floor rather than a size: the
// decision to keep the cache was made on less than the cache, and a reader
// wondering why a directory over its budget survived should not have to guess.
// (The decision itself still stands on what was measured — refusing to decide
// would mean an unbounded cache, and the failure the budget prevents is worse
// than a trim that happens one run late.)
func trimToBudget(report, stderr io.Writer, t target, limit int64, d deps) {
	used, err := measure(t.dir)
	partial := ""
	if err != nil {
		printf(stderr, "testcache: %s could not be measured completely: %v\n", t, err)
		partial = ", counting only what could be read"
	}
	if used.bytes <= limit {
		printf(report, "testcache: %s holds %s (%d bytes)%s, under the budget of %s; kept\n",
			t.dir, humanSize(used.bytes), used.bytes, partial, humanSize(limit))
		return
	}
	// The line stops at the verdict, and the removal reports itself: a trim that
	// announced "emptying it" and was then refused for want of a marker would
	// have said the opposite of what happened.
	printf(report, "testcache: %s holds %s (%d bytes)%s, over the budget of %s\n",
		t.dir, humanSize(used.bytes), used.bytes, partial, humanSize(limit))
	wipe(report, stderr, t, d)
}

// wipe empties the build cache the way its owner does, and then removes what is
// left of it.
//
// Both halves are needed. `go clean -cache` is the supported way to empty a
// build cache and the only one that takes the cache's own lock, so a `go build`
// finishing in another terminal sees an emptied cache rather than a tree being
// deleted underneath it — but it leaves the directory itself, its log, and
// anything a killed command dropped in it that the go command no longer
// recognises. The removal is what makes "empty" mean empty.
func wipe(stdout, stderr io.Writer, t target, d deps) bool {
	// Before the cleaner, not only before the removal: `go clean -cache` empties
	// whatever GOCACHE names, so a foreign directory that reached this function
	// would already have been emptied by the time os.RemoveAll was asked to
	// refuse it.
	if !licensed(stderr, t) {
		return false
	}
	// Measured before the go command empties it, so that the line at the end
	// reports what was removed rather than the handful of bytes `go clean` left
	// for the removal to pick up.
	used, err := measure(t.dir)
	if err != nil {
		printf(stderr, "testcache: %s could not be measured completely: %v\n", t, err)
	}
	if err := d.cleaner(t.dir); err != nil {
		// Reported, not fatal: the removal below is the guarantee, and a
		// toolchain that could not be found or would not run is not a reason to
		// leave the directory standing.
		printf(stderr, "testcache: %v\n", err)
	}
	return removeTree(stdout, stderr, t, used, d)
}

// removalRetryDelay is how long a locked file is given to be released.
//
// Two seconds is far longer than the moment Windows needs to finish unmapping a
// test binary that has just exited, and far shorter than anything a person
// waiting on a suite would notice. It is a single retry rather than a loop
// because the failures that outlast it — a virus scanner with the file open, a
// `go` command that is still running — are not failures that a longer wait
// fixes, and reporting one is better than blocking a run behind it.
const removalRetryDelay = 2 * time.Second

// removeTree removes a directory, retries once, and then reports rather than
// fails.
//
// The retry is the Windows rule: a file in the build cache can be held open by
// something the run started — an antivirus scanner, a lingering `go` command, a
// test binary the operating system has not finished unmapping — and the second
// attempt a moment later usually succeeds. What matters more is the ending: this
// runs as the last step of a suite, and a collector that turns a green run red
// because a directory it wanted to delete is still there has done more damage
// than the directory ever would. So it says what it could not do and exits
// successfully.
//
// The measurement is the caller's, because the caller may have emptied the
// directory first: what the line at the end should report is what was removed,
// not what was left over to remove.
func removeTree(stdout, stderr io.Writer, t target, used diskUsage, d deps) bool {
	// Checked here as well as in [wipe], so that no path to os.RemoveAll exists
	// that has not asked first.
	if !licensed(stderr, t) {
		return false
	}
	err := os.RemoveAll(t.dir)
	if err != nil {
		d.sleep(removalRetryDelay)
		err = os.RemoveAll(t.dir)
	}
	if err != nil {
		printf(stderr, "testcache: the %s %s could not be removed, and is left as it is "+
			"(a file in it is probably still open): %v\n", t.label, t, err)
		// Not a refusal: this one is reported and forgiven, because a locked file
		// is not a reason to fail a suite that has just gone green.
		return true
	}
	if !used.exists {
		printf(stdout, "testcache: the %s %s was already empty\n", t.label, t)
		return true
	}
	printf(stdout, "testcache: removed the %s %s (%s in %s)\n",
		t.label, t, humanSize(used.bytes), plural(used.files, "file"))
	return true
}

// labelWidth aligns the two directories' figures under each other, so that the
// pair can be compared at a glance rather than read.
const labelWidth = len("kept scratch:")

// reportUsage writes one directory's two lines.
//
// The ownership note on the end of the second line is there because the
// alternative is a `clean` that refuses with no warning anybody could have seen
// coming: a directory holding gigabytes and no marker is exactly the situation
// where somebody needs to be told that the collector will not touch it, and
// this is the command they run to look.
func reportUsage(stdout, stderr io.Writer, t target) {
	used, err := measure(t.dir)
	if err != nil {
		printf(stderr, "testcache: %s could not be measured completely: %v\n", t, err)
	}
	printf(stdout, "%-*s %s\n", labelWidth, t.label+":", t)
	indent := strings.Repeat(" ", labelWidth+1)
	if !used.exists {
		printf(stdout, "%sdoes not exist (0 bytes in 0 files)\n", indent)
		return
	}
	note := ""
	if own, err := inspectOwnership(t); err != nil || own == ownershipForeign {
		note = " — not stamped by the harness, so `clean` will refuse it"
	}
	printf(stdout, "%s%s (%d bytes) in %s%s\n",
		indent, humanSize(used.bytes), used.bytes, plural(used.files, "file"), note)
}

// plural renders a count with its noun, because "1 files" in the line a run ends
// with is the kind of thing that makes a reader distrust the number beside it.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// diskUsage is what one directory holds.
type diskUsage struct {
	exists bool
	files  int
	bytes  int64
}

// measure walks a directory and adds up what is in it, counting everything it
// can read and reporting everything it could not.
//
// The walk never stops early, and that is the point rather than politeness. A
// returned error stops filepath.WalkDir where it stands, so a single unreadable
// subdirectory near the top of a 6 GiB cache produces a 200 MiB total — and that
// number is then compared against the budget as though it were the size of the
// cache, passes, and goes on passing for as long as the directory stays
// unreadable. The cache is never trimmed and nothing says why. Continuing gives
// a total that is at least a floor, and the joined error is what lets the report
// say it is a floor rather than a size.
//
// A missing directory is not an error at all: it is the normal state after
// `mise run test-clean` and before the first `go` command of the day. Nor is a
// file that vanishes mid-walk — the build cache is live, and a `go build`
// finishing in another terminal evicts entries while this reads them.
func measure(dir string) (diskUsage, error) {
	var used diskUsage
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return used, nil
	case err != nil:
		return used, err
	case !info.IsDir():
		// Not a case anybody means to create, but a file where a directory was
		// expected is worth reporting as one file rather than as nothing.
		return diskUsage{exists: true, files: 1, bytes: info.Size()}, nil
	}

	used.exists = true
	// Only the first is kept. A cache holds hundreds of thousands of files, and
	// a permission problem is almost always one cause repeated: a message naming
	// the first entry and the number of others is readable, and a joined list of
	// nine thousand is not.
	var firstFailure error
	others := 0
	note := func(err error) error {
		if firstFailure == nil {
			firstFailure = err
		} else {
			others++
		}
		return nil
	}

	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return note(err)
		case entry.IsDir():
			return nil
		}
		info, err := entry.Info()
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return note(fmt.Errorf("%s: %w", path, err))
		}
		used.files++
		used.bytes += info.Size()
		return nil
	})

	switch {
	case firstFailure == nil:
		return used, nil
	case others > 0:
		return used, fmt.Errorf("%w (and %d more like it)", firstFailure, others)
	default:
		return used, firstFailure
	}
}

// requireNoArguments refuses trailing words rather than ignoring them, because
// `testcache path --budget 4GiB` is a person asking for something this tool does
// not do and a silent success would be the wrong answer.
func requireNoArguments(name string, args []string, stderr io.Writer) int {
	if len(args) == 0 {
		return exitOK
	}
	printf(stderr, "testcache: `%s` takes no arguments, and was given %q\n", name, args)
	return exitUsage
}

// target is one of the two directories the harness owns, resolved.
//
// It carries five things because every message and every refusal needs them
// together: what to call it, where it actually is, how the caller spelled it,
// which variable to change, and which marker licenses removing it. The spelling
// is not decoration — a cache reached through a symlink is measured and emptied
// at the far end of the link, and a refusal naming only that end would be about
// a path the reader never typed.
type target struct {
	label    string
	dir      string
	named    string
	variable string
	marker   string
}

// String names the directory, and says how it was spelled when the two differ.
func (t target) String() string {
	if t.dir == t.named {
		return t.dir
	}
	return t.dir + " (named as " + t.named + ")"
}

// buildCacheTarget resolves the shared build cache, and refuses a relative
// override rather than falling back to the default.
//
// The go command refuses a relative GOCACHE outright. A wrapper that resolved
// one against its own working directory would put the cache somewhere nobody
// named, and one that ignored the variable would send the run back into the
// developer's own cache — which is the directory whoever set the variable was
// moving away from. Neither is a fallback worth having, so it is an error that
// names the variable.
func buildCacheTarget(env map[string]string, d deps) (target, error) {
	dir, named, err := harnessDirectory(env, d, buildCacheEnv, buildCacheIn,
		"and the go command refuses a relative GOCACHE")
	return target{
		label: "build cache", dir: dir, named: named,
		variable: buildCacheEnv, marker: buildCacheMarker,
	}, err
}

// keptTarget resolves the root a kept scratch directory is filed under.
//
// It is resolved here, in the tool that empties things, because the keep policy
// is opt-in per run and its directories outlive the run that wrote them: a
// developer who turned keeping on last week has a tree under this root and no
// reason to remember it. `status` reports it and `clean` removes it, so the
// collector is one command rather than a path to look up.
func keptTarget(env map[string]string, d deps) (target, error) {
	dir, named, err := harnessDirectory(env, d, keepDirEnv, keptIn,
		"and a child that starts elsewhere would resolve it against its own working directory")
	return target{
		label: "kept scratch", dir: dir, named: named,
		variable: keepDirEnv, marker: keptMarker,
	}, err
}

// harnessDirectory is the shared rule: an absolute override, or one directory
// below the platform's cache root — resolved through its symlinks, and checked
// against the directories that must never be treated as either.
//
// Below the *platform's* cache root and not below HOME, because the suites move
// HOME into a temporary directory of their own — that is how a test keeps its
// fixtures out of the developer's real `~/Library/Caches/go-mutants` — and a
// cache below a moved HOME is a cache that starts empty in every test.
func harnessDirectory(env map[string]string, d deps, variable, leaf, because string) (dir, named string, err error) {
	if value := env[variable]; value != "" {
		if !filepath.IsAbs(value) {
			return "", value, fmt.Errorf("%s=%s is not an absolute path, %s", variable, value, because)
		}
		named = filepath.Clean(value)
	} else {
		root, err := d.userCache()
		// Two failures, two messages: a platform that has no cache directory at
		// all, and one whose lookup failed and can say why. A single line with
		// `%v` on a nil error prints "<nil>", which reads like the tool has lost
		// track of what went wrong.
		switch {
		case err != nil:
			return "", "", fmt.Errorf("this platform has no user cache directory, so %s has to name one: %w", variable, err)
		case root == "":
			return "", "", fmt.Errorf("this platform reports no user cache directory, so %s has to name one", variable)
		}
		named = filepath.Join(root, harnessDir, leaf)
	}
	dir = resolveSymlinks(named)
	if err := refuseDangerousDirectory(dir, named, variable, d); err != nil {
		return "", named, err
	}
	return dir, named, nil
}

// resolveSymlinks follows a path to the directory it stands for, and leaves one
// it cannot resolve exactly as it was written.
//
// A cache that is a symlink onto a bigger disk is an arrangement developers
// build on purpose, and it breaks every part of this tool that is not told about
// it: os.Stat says the path exists, filepath.WalkDir then walks the *link* and
// finds a single 24-byte "file", so every budget passes forever — and the
// removal takes the link and leaves the gigabytes behind it, which is a cache
// that is at once never trimmed and permanently cold. A path that does not exist
// yet, or that cannot be read, is returned unchanged: the first is the normal
// state before the first run, and the second is not worth failing over here.
func resolveSymlinks(dir string) string {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return resolved
}

// refuseDangerousDirectory refuses the directories that must never be treated as
// the harness's, whatever they happen to contain.
//
// This is the belt to the ownership marker's braces. The marker answers "did we
// make this?"; these answer "could this ever have been ours?", before anything is
// measured, created or removed — so a marker somebody copied about, or a
// directory that acquired one by accident, still cannot make any of them
// removable. The user's own build cache is on the list because it is the exact
// directory this tool exists to keep the suites out of, and a variable pointing
// back at it would reinstate that problem while looking configured.
func refuseDangerousDirectory(dir, named, variable string, d deps) error {
	spelling := dir
	if dir != named {
		spelling = dir + " (named as " + named + ")"
	}
	if dir == filepath.Dir(dir) {
		return fmt.Errorf("%s resolves to %s, which is a filesystem root: nothing this tool does to a "+
			"directory is a thing to do to one of those", variable, spelling)
	}
	if home, err := d.userHome(); err == nil && home != "" && samePath(dir, home) {
		return fmt.Errorf("%s resolves to %s, which is your home directory: this tool empties what it is "+
			"pointed at, so it refuses to be pointed there", variable, spelling)
	}
	if root, err := d.userCache(); err == nil && root != "" && samePath(dir, filepath.Join(root, buildCacheIn)) {
		return fmt.Errorf("%s resolves to %s, which is the go command's own build cache: empty that with "+
			"`go clean -cache` yourself if you mean to. This tool exists to keep the suites out of it",
			variable, spelling)
	}
	return nil
}

// samePath reports whether two paths name the same directory, following what
// links either of them has.
//
// Lexical after that, which is exact on the platforms this runs on and merely
// conservative on a case-insensitive one: two spellings differing only in case
// are then treated as different directories, and the only cost is a guard
// declining to fire on a path somebody went out of their way to mistype.
func samePath(a, b string) bool {
	return resolveSymlinks(filepath.Clean(a)) == resolveSymlinks(filepath.Clean(b))
}

// stampDirectory creates a directory and writes the marker that licenses its
// later removal — but only for a directory this harness could have created.
//
// The "only" is the whole rule, and getting it wrong undoes the guard
// completely: a stamp written into whatever the variable happened to name would
// make `GO_MUTANTS_TEST_GOCACHE=$HOME testcache exec --budget 0 -- true` a
// command that deletes a home directory, with the tool's own marker as its
// permission slip. So a directory that already holds somebody else's files is
// never stamped. It is reported instead (see [runExec]), the run proceeds — a
// cache is perfectly usable without a marker — and nothing will ever remove it,
// which is the safe half of the trade.
//
// Absent and empty both qualify: the first is the normal state before the first
// run, and the second is a `mkdir -p` in a CI step or a developer's shell, which
// is how the directory usually exists before anything has written to it.
//
// It is idempotent because it runs on every wrapped command and, in the harness,
// on every call to Env: O_EXCL turns a race between two of them into one winner
// and one no-op rather than two writers truncating one file. The body is
// addressed to whoever finds the file, because it is the only explanation they
// are going to get.
func stampDirectory(t target) (bool, error) {
	own, inspectErr := inspectOwnership(t)
	switch {
	case inspectErr != nil:
		return false, fmt.Errorf("inspecting the %s %s: %w", t.label, t, inspectErr)
	case own == ownershipOurs:
		return true, nil
	case own == ownershipForeign:
		empty, readErr := isEmptyDirectory(t.dir)
		if readErr != nil {
			return false, fmt.Errorf("reading the %s %s: %w", t.label, t, readErr)
		}
		if !empty {
			return false, nil
		}
	}

	if mkErr := os.MkdirAll(t.dir, 0o700); mkErr != nil {
		return false, fmt.Errorf("creating the %s %s: %w", t.label, t, mkErr)
	}
	file, err := os.OpenFile(filepath.Join(t.dir, t.marker), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("stamping the %s %s: %w", t.label, t, err)
	}
	_, writeErr := file.Write(markerBody(t))
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return false, fmt.Errorf("stamping the %s %s: %w", t.label, t, err)
	}
	return true, nil
}

// isEmptyDirectory reports whether a directory holds nothing at all.
//
// One entry is enough to answer, which matters: this runs against a build cache
// that may hold hundreds of thousands of files, and the question is not how many
// there are.
func isEmptyDirectory(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// markerBody is what a person who finds one of these files reads.
func markerBody(t target) []byte {
	return []byte("This directory is the go-mutants test harness's " + t.label + ".\n" +
		"It exists so that the test suites' `go` commands do not fill your own build cache.\n" +
		"`mise run test-cache-status` reports it; `mise run test-clean` empties it.\n" +
		"Nothing in it is precious: deleting it costs one recompile.\n" +
		"This file is also what tells the collector the directory is safe to remove,\n" +
		"which is why nothing removes a directory that does not have one.\n")
}

// ownership is what a removal is allowed to do to a directory.
type ownership int

const (
	// ownershipAbsent: there is nothing there, which is the normal state before
	// the first run of the day and after a clean.
	ownershipAbsent ownership = iota
	// ownershipOurs: the marker is there.
	ownershipOurs
	// ownershipForeign: something is there, and it is not ours.
	ownershipForeign
)

// inspectOwnership answers whether a directory may be removed.
//
// Anything unreadable counts as foreign. A directory this tool cannot even look
// at is not one it should be deleting, and the alternative — reading an error as
// permission — is the same reasoning that made `GO_MUTANTS_TEST_GOCACHE=$HOME`
// destructive in the first place.
func inspectOwnership(t target) (ownership, error) {
	info, err := os.Stat(t.dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ownershipAbsent, nil
	case err != nil:
		return ownershipForeign, err
	case !info.IsDir():
		return ownershipForeign, nil
	}
	switch _, err := os.Lstat(filepath.Join(t.dir, t.marker)); {
	case err == nil:
		return ownershipOurs, nil
	case errors.Is(err, fs.ErrNotExist):
		return ownershipForeign, nil
	default:
		return ownershipForeign, err
	}
}

// licensed reports whether a removal may proceed, and says why not when it may
// not.
//
// The message is the whole value of the guard. Somebody has pointed a variable
// at a directory this tool will not touch, and the two possible reasons need
// opposite answers: if it really is meant to be the cache, one run creates the
// marker; if it is not, the variable is wrong and the message says which one.
func licensed(stderr io.Writer, t target) bool {
	own, err := inspectOwnership(t)
	if err != nil {
		printf(stderr, "testcache: the %s %s cannot be inspected, so nothing here will remove it: %v\n",
			t.label, t, err)
		return false
	}
	if own == ownershipForeign {
		printf(stderr, "testcache: %s is not the harness's %s: it holds no %s, so nothing here will remove "+
			"it. If it is meant to be the %s, either let a suite or `testcache exec` create it — both "+
			"write that file — or, if it predates this check, delete it once by hand and the next run "+
			"will make it again. If it is not, point %s somewhere else.\n",
			t, t.label, t.marker, t.label, t.variable)
		return false
	}
	return true
}

// goCleanCache empties the cache the way its owner does.
//
// `go clean -cache` rather than a plain removal, because the go command holds
// its own lock over that directory: a concurrent `go build` — a suite still
// finishing, another terminal — finds an emptied cache rather than a tree that
// is being deleted underneath it. What it leaves behind (the trim log, the
// README the go command writes, and anything a killed command dropped) is
// removed afterwards by the caller.
//
// The environment is the process's own with three rows replaced. GOCACHE is the
// point of the exercise; GOTOOLCHAIN=local keeps a go.mod asking for another
// toolchain from downloading one before it will delete a directory; GOWORK=off
// keeps a go.work above the working directory out of it. The command runs in the
// temporary directory rather than in the repository for the same reason — the
// cache to empty is named by a variable, not by where the tool was started.
func goCleanCache(dir string) error {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("locating the go command that owns %s: %w", dir, err)
	}
	cmd := exec.Command(gobin, "clean", "-cache")
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "GOCACHE="+dir, "GOTOOLCHAIN=local", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("`go clean -cache` with GOCACHE=%s: %w\n%s", dir, err, out)
	}
	return nil
}

// binaryUnits are the size suffixes a budget may carry, longest first so that
// the suffix match cannot mistake the `B` at the end of `GB` for a unit of its
// own.
var binaryUnits = []struct {
	suffix string
	scale  float64
}{
	{"KIB", 1 << 10},
	{"MIB", 1 << 20},
	{"GIB", 1 << 30},
	{"TIB", 1 << 40},
	{"B", 1},
}

// parseSize reads a budget.
//
// The units are binary and only binary. A developer who types `4GB` means four
// gibibytes about as often as they mean four gigabytes, and the two differ by
// 7% — read as decimal, a `4GB` budget wipes a cache that is still 300 MiB
// below what its author intended, and the only visible symptom is a suite that
// recompiles the standard library for no reason anybody can see. So `GB` is
// refused with a message naming the accepted spellings rather than quietly
// reinterpreted.
//
// A bare number is bytes, because that is what every other number this tool
// prints is, and a fractional value is accepted because `1.5GiB` is a budget a
// person types.
func parseSize(text string) (int64, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(text))
	if trimmed == "" {
		return 0, fmt.Errorf("a budget may not be empty: write it as a number of bytes or with a unit (B, KiB, MiB, GiB, TiB)")
	}

	number, scale := trimmed, float64(1)
	for _, unit := range binaryUnits {
		if rest, found := strings.CutSuffix(trimmed, unit.suffix); found {
			number, scale = strings.TrimSpace(rest), unit.scale
			break
		}
	}

	// Every rejection below is a rejection rather than a clamp, and the last two
	// are the reason this function has a test of its own. Converting a float64
	// that does not fit into an int64 is undefined in Go, and on every machine
	// this runs on it produces math.MinInt64 — a *negative* budget, which every
	// measured total is over, so a tool whose entire job is keeping one cache
	// warm would empty it on every single run. NaN is the same failure in
	// different clothes: every comparison against it is false, `used.bytes <=
	// budget` included, so the wipe happens then too. Neither is a number
	// anybody could have meant, so both stop the command rather than quietly
	// becoming something else.
	value, err := strconv.ParseFloat(number, 64)
	switch {
	case err != nil:
		return 0, fmt.Errorf("%q is not a size: write it as a number of bytes or with a unit (B, KiB, MiB, GiB, TiB); "+
			"the decimal spellings (kB, MB, GB) are refused so that a budget is never quietly smaller than it reads", text)
	case math.IsNaN(value):
		return 0, fmt.Errorf("%q is not a number at all: a budget is a number of bytes, optionally with a unit (B, KiB, MiB, GiB, TiB)", text)
	case math.IsInf(value, 0):
		return 0, fmt.Errorf("%q has no size: a budget is a number of bytes, optionally with a unit (B, KiB, MiB, GiB, TiB)", text)
	case value < 0:
		return 0, fmt.Errorf("%q is negative: a budget is a number of bytes, optionally with a unit (B, KiB, MiB, GiB, TiB)", text)
	// Divided rather than multiplied, so the comparison itself cannot overflow,
	// and `>=` rather than `>`, because float64(math.MaxInt64) rounds *up* to
	// 2^63 — the one value that passes a `>` test and then converts to
	// math.MinInt64.
	case value >= float64(math.MaxInt64)/scale:
		return 0, fmt.Errorf("%q is larger than any directory can be (the limit is just under %d bytes, and units are B, KiB, MiB, GiB, TiB)",
			text, int64(math.MaxInt64))
	}
	return int64(value * scale), nil
}

// humanSize renders a number of bytes the way a person reads one.
//
// Binary units, to match what [parseSize] accepts: a status line that reported
// "4.3 GB" for a cache a 4GiB budget had just left alone would read like a bug
// in the budget.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for rest := n / unit; rest >= unit; rest /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
