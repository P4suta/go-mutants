// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"context"
	"errors"
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The variables the test harness itself reads. None of them may begin with
// GO_MUTANTS_, except where the value has to survive being stripped from a
// child's environment — the policy removes that whole prefix, because a
// developer with GO_MUTANTS_ACTIVE exported in their shell would otherwise run a
// mutant as the baseline. GO_MUTANTS_TEST_GOCACHE is named the way it is because
// a developer types it, and it is read before the stripping happens.
const (
	// BuildCacheEnv names the directory the suites' child `go` commands use as
	// GOCACHE. CI points it at the runner's own temporary area.
	BuildCacheEnv = "GO_MUTANTS_TEST_GOCACHE"
	// RequireToolsEnv turns a missing `go` or `git` from a skip into a failure.
	RequireToolsEnv = "GO_MUTANTS_TEST_REQUIRE_TOOLS"
	// KeepDirEnv names the root a kept scratch directory is filed under. The keep
	// policy itself is not here yet; the variable is, because
	// internal/devtools/testcache already reports and empties that root, and the
	// two have to agree about where it is.
	KeepDirEnv = "GO_MUTANTS_TEST_KEEP_DIR"
)

// BuildCacheMarker is the file that says the build cache is the harness's.
//
// Nothing in a path says who made it. `GO_MUTANTS_TEST_GOCACHE=$HOME` is an
// absolute path like any other, and a collector that trusted the variable would
// run `go clean -cache` and os.RemoveAll against a home directory. So ownership
// is written down: whatever resolves the cache creates it and leaves this file
// in it, and internal/devtools/testcache removes nothing that does not carry
// one. The harness is the owner; that tool is the collector.
//
// The name is duplicated there, because a test-only package cannot be imported
// from production code — the same reason [BuildCache]'s rule is duplicated. Two
// spellings would fail silently in the worst possible direction: the harness
// would write one file, the collector would look for another, and the only
// symptom would be a cache that was never emptied. TestMarkerNamesAgreeWithTestcache
// runs `testcache path --marker` and compares.
const BuildCacheMarker = ".go-mutants-testcache"

// The identity every repository a test builds commits under.
//
// A commit's hash is derived from its author, its committer and both of their
// dates, so fixing all six is what makes two runs of the same test produce the
// same repository — and what lets a test assert on a commit at all. The address
// is under .invalid, which RFC 2606 reserves, so no message a test writes can
// ever be delivered anywhere.
const (
	GitName  = "go-mutants tests"
	GitEmail = "tests@go-mutants.invalid"
	GitDate  = "2026-02-18T09:15:00+00:00"
)

// Environment names the directories one test owns.
//
// Scratch is the temporary directory the process and its children see; Home is
// the moved HOME; Cache is what os.UserCacheDir resolves to under it — the cache
// *root*, the directory go-mutants puts its own `go-mutants` directory in, which
// is what the code under test joins onto; GoCache is the shared, test-owned
// build cache, which is deliberately *not* under Home.
type Environment struct {
	Scratch string
	Home    string
	Cache   string
	GoCache string

	vars []string
}

// Env gives one test a hermetic environment, in the process and as a value.
//
// It calls t.Setenv, so a test that calls it may not call t.Parallel; a test
// that only needs the value form wants [Compose] instead, which applies these
// same rows. The policy is stated once here:
//
//	TMPDIR, TMP, TEMP                     the test's own scratch directory
//	HOME, home, USERPROFILE               a private home
//	XDG_CACHE_HOME, LocalAppData          the cache below it, telemetry off
//	GOCACHE                               the shared test-owned build cache
//	GOENV, GOPATH, GOMODCACHE             the machine's real ones, pinned
//	GOWORK, GOPROXY, GOSUMDB              off
//	GOTOOLCHAIN                           local
//	GOFLAGS                               -mod=readonly
//	every GO_MUTANTS_*                    removed
//	GIT_CONFIG_GLOBAL, GIT_CONFIG_SYSTEM  paths that do not exist
//	GIT_AUTHOR_*, GIT_COMMITTER_*         the fixed identity above
//	GITHUB_STEP_SUMMARY                   blank
//	PATH and everything else              inherited
//
// The build cache directory is created and stamped with [BuildCacheMarker],
// because that file is what licenses the collector to empty it later: a
// directory nothing claims is a directory `mise run test-clean` will refuse to
// remove. The one exception is a directory that already holds files that are not
// ours, which is left exactly as it was — see [stampBuildCache].
//
// The hermetic value wins on conflict: a developer who exports GOFLAGS gets
// -mod=readonly here, and a test that needs -mod=mod passes it on the command
// line or names it in [Inherit]. Everything the policy does not name is
// inherited, because the child needs a PATH, a shell, a temporary user, a
// terminal — and because a list of variables to *allow* is a list that grows a
// new entry every time a platform is added.
func Env(t testing.TB, opts ...EnvOption) *Environment {
	t.Helper()

	var cfg envConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	// Everything derived from the real HOME is resolved before the real HOME
	// stops being reachable: the go command's own directories, and the build
	// cache, whose variable this policy is about to strip along with the rest of
	// the GO_MUTANTS_ namespace.
	resolveGoDirectories()
	gocache, err := BuildCache()
	if err != nil {
		t.Fatalf("resolving the test build cache: %v", err)
	}

	stampBuildCache(t, gocache)

	p := policy{scratch: t.TempDir(), gocache: gocache}
	if !cfg.keepHome {
		// Inheriting one of the home variables while the policy moves the rest
		// leaves os.UserCacheDir resolving to the machine's real cache directory,
		// which trips the guard in [resolveMovedCache] — and that guard's message
		// is about a platform reading a variable nobody set, which is the wrong
		// thing to read when the cause is two options that cannot both hold.
		if name, ok := cfg.firstInherited(homeNames()); ok {
			t.Fatalf("Inherit(%q) asks to keep a variable this policy moves, and the test would then "+
				"read and write the developer's own home directory. A test whose subject is the "+
				"real home wants KeepHome() rather than Inherit(%q).", name, name)
		}
		p.home = t.TempDir()
	}

	e := &Environment{Scratch: p.scratch, GoCache: gocache}
	for _, pair := range policyPairs(p) {
		setUnlessInherited(t, &cfg, pair[0], pair[1])
	}

	if cfg.keepHome {
		e.Home = realHome(t)
		cache, err := os.UserCacheDir()
		if err != nil {
			t.Fatalf("os.UserCacheDir with the real HOME kept: %v", err)
		}
		e.Cache = cache
	} else {
		e.Home = p.home
		e.Cache = resolveMovedCache(t, p.home)
	}

	stripActivation(t, &cfg)
	e.vars = composeFrom(os.Environ(), p, &cfg)
	logInputs(t, "scratch="+e.Scratch, "home="+e.Home, "gocache="+e.GoCache)
	return e
}

// realHome is the machine's home directory.
//
// os.UserHomeDir rather than $HOME, because on Windows the home directory comes
// from %USERPROFILE% and $HOME is very often unset there — a [KeepHome]
// environment that reported an empty Home would make every assertion against it
// vacuous rather than failing.
func realHome(t testing.TB) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir with the real HOME kept: %v", err)
	}
	return home
}

// Vars returns the environment as a value, for a child started with an explicit
// one.
//
// It is a copy: a caller that appended to the slice it was handed would be
// appending to the next caller's environment. The value is the environment as of
// the call to [Env] — a later t.Setenv is a change to the process, and a test
// that wants a child to see it says so with [Environment.With].
func (e *Environment) Vars() []string { return slices.Clone(e.vars) }

// With returns the environment with the given `NAME=value` entries applied.
//
// An entry naming a variable the policy already set replaces it rather than
// joining it, because a duplicate key is resolved differently by os/exec, by
// the C library and by a shell — and "the last one wins" is not something a
// test should have to know.
func (e *Environment) With(kv ...string) []string {
	return withEntries(e.vars, kv...)
}

// Compose returns the same policy as a value, without touching the process.
//
// This is the form a parallel test uses: a test that starts every child with an
// explicit environment does not need the process changed, and a test that does
// not change the process may call t.Parallel.
//
// Every row [Env] applies is applied here, the private home included — it is
// derived from the scratch directory (`<scratch>/home`) rather than from a
// t.TempDir of the harness's own, which is the only difference between the two
// forms. The home is created and go telemetry is turned off inside it, because a
// child `go` command with the developer's HOME writes into their telemetry
// directory and files go-mutants' history and outcome cache into their real
// cache directory.
//
// The scratch directory must exist and be absolute: every row is derived from
// it, so a bad one surfaces as a `go build` that cannot create a temporary file,
// a git that read a configuration file that turned out to exist, or a HOME the
// child creates in the working directory.
func Compose(t testing.TB, scratch string) []string {
	t.Helper()
	// While HOME is still whatever the process has: see [Env].
	resolveGoDirectories()
	requireScratch(t, scratch)
	gocache, err := BuildCache()
	if err != nil {
		t.Fatalf("resolving the test build cache: %v", err)
	}
	stampBuildCache(t, gocache)
	p := policy{scratch: scratch, gocache: gocache, home: filepath.Join(scratch, "home")}
	createPrivateHome(t, p.home)
	return composeFrom(os.Environ(), p, &envConfig{})
}

// requireScratch refuses a scratch directory the policy cannot be derived from.
func requireScratch(t testing.TB, scratch string) {
	t.Helper()
	switch info, err := os.Stat(scratch); {
	case scratch == "":
		t.Fatalf("the scratch directory may not be empty: every row of the policy is derived from it")
	case !filepath.IsAbs(scratch):
		t.Fatalf("the scratch directory %s is not absolute, and a child that starts elsewhere would "+
			"resolve it against its own working directory", scratch)
	case err != nil:
		t.Fatalf("the scratch directory %s cannot be used: %v", scratch, err)
	case !info.IsDir():
		t.Fatalf("the scratch directory %s is not a directory", scratch)
	}
}

// BuildCache returns the directory the suites' child `go` commands use as
// GOCACHE.
//
// One dedicated, persistent directory is the answer to two failures at once. The
// developer's own cache filled up — 14 GB on the machine this was written on —
// not because go-mutants compiles slowly but because the suites drive thousands
// of `go build`, `go test -c` and `go list` commands against fixtures and
// synthesized modules, each keyed on an absolute path that exists for one run.
// A cache per test is the opposite failure: the standard library recompiled once
// per test. So it is shared, it is outside every temporary directory a test
// owns, and it is somewhere a developer or a CI job can name and delete whole.
//
// A relative [BuildCacheEnv] is an error rather than a fallback. The go command
// refuses a relative GOCACHE, and a helper that quietly ignored the value would
// send the run to the default cache the caller was trying to move it away from.
func BuildCache() (string, error) {
	// [Env] removes every GO_MUTANTS_ variable from the process, this one
	// included, so this reads through the removal: without that, a CI job that
	// named a cache would find its children back in the default one the moment a
	// test redirected its environment — which is every test.
	named := harnessSetting(BuildCacheEnv, pinnedBuildCache)
	if named != "" {
		if !filepath.IsAbs(named) {
			return "", fmt.Errorf("%s=%s is not an absolute path, and the go command refuses a relative GOCACHE", BuildCacheEnv, named)
		}
		return filepath.Clean(named), nil
	}
	if pinned.userCache == "" {
		return "", errors.New("this platform has no user cache directory, so " + BuildCacheEnv + " has to name one")
	}
	return filepath.Join(pinned.userCache, "go-mutants-test", "go-build"), nil
}

// stampBuildCache creates the build cache and leaves [BuildCacheMarker] in it.
//
// Three states, and only two of them get a file. A directory that does not exist
// is created and stamped, which is the first run on any machine. One that is
// empty is stamped, because that is a `mkdir -p` in a CI step or a shell. One
// that already holds files that are not ours is left completely alone — and that
// exception is the whole reason this is not four lines. A stamp written into
// whatever the variable happened to name would be a permission slip the harness
// issues on somebody else's behalf: point GO_MUTANTS_TEST_GOCACHE at a directory
// full of work, run any test in this repository, and `mise run test-clean` would
// then delete it with the marker's blessing. Refusing to stamp costs a cache
// nothing ever empties, which is the half of the trade worth keeping.
//
// A directory that cannot be created stops the test: every child `go` command is
// about to fail on the same directory, and it will not say why nearly as
// clearly. A marker that cannot be written does not, because the run works
// without it and only the collector is worse off.
func stampBuildCache(t testing.TB, dir string) {
	t.Helper()
	marker := filepath.Join(dir, BuildCacheMarker)
	if _, err := os.Lstat(marker); err == nil {
		return
	}

	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
			t.Fatalf("creating the test build cache %s (every child `go` command is about to use it): %v", dir, mkErr)
		}
	case err != nil:
		t.Logf("testkit: the test build cache %s cannot be read, so it was not stamped and nothing will "+
			"collect it: %v", dir, err)
		return
	case len(entries) > 0:
		t.Logf("testkit: %s already holds files that are not this harness's, so it was not stamped as "+
			"the test build cache and `mise run test-clean` will refuse to empty it. If it is a cache "+
			"from before this file existed, delete it once and the next run will make it again; "+
			"otherwise point %s at a directory of its own.", dir, BuildCacheEnv)
		return
	}

	// O_EXCL, so that two tests arriving together produce one winner and one
	// no-op rather than two writers truncating the same file.
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if !errors.Is(err, fs.ErrExist) {
			t.Logf("testkit: the test build cache %s could not be stamped, so nothing will collect it: %v", dir, err)
		}
		return
	}
	_, writeErr := file.WriteString(buildCacheMarkerBody)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		t.Logf("testkit: the test build cache marker %s could not be written: %v", marker, err)
	}
}

// buildCacheMarkerBody is what a person who finds the file reads. It is the only
// explanation they get, so it says what the directory is, what writes it, what
// empties it, and that losing it costs nothing but a recompile.
const buildCacheMarkerBody = "This directory is the go-mutants test harness's build cache.\n" +
	"It exists so that the test suites' `go` commands do not fill your own build cache.\n" +
	"`mise run test-cache-status` reports it; `mise run test-clean` empties it.\n" +
	"Nothing in it is precious: deleting it costs one recompile.\n" +
	"This file is also what tells the collector the directory is safe to remove,\n" +
	"which is why nothing removes a directory that does not have one.\n"

// pinnedBuildCache is [BuildCacheEnv] as the process started with it.
var pinnedBuildCache = os.Getenv(BuildCacheEnv)

// harnessSetting reads one of the harness's own settings, whatever the policy
// has since done to the environment.
//
// The two settings this package reads for itself wear the GO_MUTANTS_ prefix,
// because that prefix names this project and a developer types these on a
// command line — and that is the same prefix [stripActivation] removes from the
// process, so reading them naively means a test that redirected its environment
// silently loses them. There are three answers in order of authority: the live
// environment, whatever the policy removed from it during this test, and the
// value the process started with. The middle one is why a `t.Setenv` before
// [Env] still decides, and the last one is why a CI job's workflow-level `env:`
// still decides after it.
//
// An explicitly empty value is an answer rather than a missing one, at every
// layer. t.Setenv cannot remove a variable, so `t.Setenv(name, "")` is the only
// way a test can say "as if nobody had named one" — and if an empty live or
// stripped value fell through to what the process started with, a test that
// cleared the variable would get the job's directory back from under itself and
// compare its own answer against a different question.
// TestAnExplicitlyEmptyBuildCacheOverrideMeansTheDefault pins that, because the
// failure it prevents is invisible anywhere except on a machine where CI has
// named a cache for the whole job.
func harnessSetting(name, atStart string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	if value, ok := strippedValue(name); ok {
		return value
	}
	return atStart
}

// stripped records what [stripActivation] took out of the process, for the
// length of the test that took it.
//
// The mutex is not for concurrent tests — a test that calls [Env] has called
// t.Setenv and may not be parallel — but for the parallel ones that read this
// through [Compose] while the framework is between tests.
var stripped struct {
	mu     sync.Mutex
	values map[string]string
}

// rememberStripped keeps one removed value readable until the test ends.
func rememberStripped(t testing.TB, name, value string) {
	t.Helper()
	stripped.mu.Lock()
	defer stripped.mu.Unlock()
	if stripped.values == nil {
		stripped.values = map[string]string{}
	}
	previous, had := stripped.values[name]
	stripped.values[name] = value
	t.Cleanup(func() {
		stripped.mu.Lock()
		defer stripped.mu.Unlock()
		if had {
			stripped.values[name] = previous
			return
		}
		delete(stripped.values, name)
	})
}

// strippedValue reads back what the policy removed during this test.
func strippedValue(name string) (string, bool) {
	stripped.mu.Lock()
	defer stripped.mu.Unlock()
	value, ok := stripped.values[name]
	return value, ok
}

// EnvOption changes what [Env] does. There are two, and both exist for a test
// whose subject is the thing the policy hides.
type EnvOption func(*envConfig)

// KeepHome leaves HOME, home, USERPROFILE, XDG_CACHE_HOME and LocalAppData as
// they are.
//
// It is for a test whose subject is what the *user's* own configuration does,
// and it means [Environment.Cache] is the machine's real cache root: anything
// written there is written into the developer's home directory, so a test that
// keeps the home should read rather than write.
func KeepHome() EnvOption {
	return func(cfg *envConfig) { cfg.keepHome = true }
}

// Inherit keeps the named variables exactly as the process has them.
//
// The names are named for the same reason the policy exists: a test that needs
// `-mod=mod`, or that is *about* a GO_MUTANTS_ variable reaching a child, says
// which one it is keeping and keeps the rest of the policy.
func Inherit(names ...string) EnvOption {
	return func(cfg *envConfig) { cfg.inherit = append(cfg.inherit, names...) }
}

// envConfig is the resolved effect of the options.
type envConfig struct {
	keepHome bool
	inherit  []string
}

// inherited reports whether a variable was named in [Inherit].
func (c *envConfig) inherited(name string) bool {
	return slices.ContainsFunc(c.inherit, func(named string) bool { return sameEnvName(named, name) })
}

// firstInherited returns the first of the given names that was inherited.
func (c *envConfig) firstInherited(names []string) (string, bool) {
	for _, name := range names {
		if c.inherited(name) {
			return name, true
		}
	}
	return "", false
}

// policy is the three directories every row of the environment is derived from.
//
// An empty home means the row group that moves it is not applied at all, which
// is what [KeepHome] asks for.
type policy struct {
	scratch string
	gocache string
	home    string
}

// homeNames are the variables that move with the private home.
//
// Every variable os.UserCacheDir reads on any GOOS is in the list, which is why
// there is no GOOS switch here and no list of platforms to keep in step with
// Go's:
//
//	windows          %LocalAppData%
//	darwin, ios      $HOME/Library/Caches
//	plan9            $home/lib/cache
//	everything else  $XDG_CACHE_HOME, or $HOME/.cache when it is unset
//
// Setting only some of them is the defect this replaces. Three packages
// redirected XDG_CACHE_HOME and LocalAppData, which covers Linux and Windows and
// covers nothing at all on macOS: os.UserCacheDir ignores XDG there, so the
// tests wrote their fixtures into a temporary directory, read the runner's own
// ~/Library/Caches/go-mutants back, and interfered with each other through it.
//
// USERPROFILE is in the list although nothing in Go's cache lookup reads it,
// because it is where Windows keeps the home directory: os.UserHomeDir reads it
// there, git resolves `~` through it, and a child left with the real one would
// write into the developer's profile from a test that had moved every other
// path.
func homeNames() []string {
	return []string{"HOME", "home", "USERPROFILE", "XDG_CACHE_HOME", "LocalAppData"}
}

// homePairs are the rows that move the home, in a stable order.
func homePairs(home string) [][2]string {
	cache := filepath.Join(home, "cache")
	return [][2]string{
		{"LocalAppData", cache},
		{"XDG_CACHE_HOME", cache},
		{"HOME", home},
		// Plan 9 spells it in lower case. On Windows the environment is
		// case-insensitive, so this is the assignment above written twice with
		// the same value, which is harmless; on POSIX it is a variable nothing
		// else reads.
		{"home", home},
		{"USERPROFILE", home},
	}
}

// policyPairs returns every variable the policy fixes, in a stable order.
//
// The order is stable so that the process form and the value form of the policy
// produce the same environment rather than the same set.
func policyPairs(p policy) [][2]string {
	scratch, gocache := p.scratch, p.gocache
	global, system := absentGitConfig(scratch)
	pairs := [][2]string{
		// os.TempDir reads TMPDIR on POSIX and TMP then TEMP on Windows, so all
		// three are set rather than guessing which platform is reading.
		{"TMPDIR", scratch},
		{"TMP", scratch},
		{"TEMP", scratch},
		{"GOCACHE", gocache},
	}
	if p.home != "" {
		pairs = append(pairs, homePairs(p.home)...)
	}
	// The go command's own directories, resolved before anything moved HOME. An
	// empty one is left alone rather than set to "", which the go command would
	// read as "no GOPATH at all".
	dirs := resolveGoDirectories()
	for _, pair := range [][2]string{
		{"GOENV", dirs.env},
		{"GOPATH", dirs.path},
		{"GOMODCACHE", dirs.modCache},
	} {
		if pair[1] != "" {
			pairs = append(pairs, pair)
		}
	}
	return append(pairs,
		[2]string{"GOWORK", "off"},
		[2]string{"GOPROXY", "off"},
		[2]string{"GOSUMDB", "off"},
		[2]string{"GOTOOLCHAIN", "local"},
		[2]string{"GOFLAGS", "-mod=readonly"},
		[2]string{"GIT_CONFIG_GLOBAL", global},
		[2]string{"GIT_CONFIG_SYSTEM", system},
		[2]string{"GIT_AUTHOR_NAME", GitName},
		[2]string{"GIT_AUTHOR_EMAIL", GitEmail},
		[2]string{"GIT_AUTHOR_DATE", GitDate},
		[2]string{"GIT_COMMITTER_NAME", GitName},
		[2]string{"GIT_COMMITTER_EMAIL", GitEmail},
		[2]string{"GIT_COMMITTER_DATE", GitDate},
		// A run inside a GitHub job writes a step summary and a stream of
		// annotations; a test is not a job, and appending to the job's real
		// summary is the one side effect a test cannot undo.
		[2]string{"GITHUB_STEP_SUMMARY", ""},
	)
}

// absentGitConfig names two configuration files that do not exist, under a
// directory that does not exist either.
//
// git reads a missing configuration file as an empty one, which is the whole
// point: a developer's ~/.gitconfig must not decide what these tests observe,
// and an *empty* file would still be a file somebody's tooling could write into.
// The parent is deliberately not created, so nothing appears in the scratch
// directory a suite is about to assert is empty.
func absentGitConfig(parent string) (global, system string) {
	base := filepath.Join(parent, "absent-git-config")
	return filepath.Join(base, "global"), filepath.Join(base, "system")
}

// resolveMovedCache asks where os.UserCacheDir landed once the home has moved,
// and refuses an answer outside the test's own directory.
//
// The guard does not trust [homeNames]: two green platforms is exactly as much
// evidence as one when the third reads a variable nobody set, so this asks
// os.UserCacheDir where it actually resolved and fails the test loudly if the
// answer is the machine's real cache directory. That check is what would have
// caught the macOS gap on the first run anywhere.
func resolveMovedCache(t testing.TB, home string) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("os.UserCacheDir after redirecting it into %s: %v", home, err)
	}
	if !within(home, cache) {
		t.Fatalf("os.UserCacheDir resolves to %s, which is outside this test's own %s: "+
			"%s reads a variable this policy does not set, and the test would be reading and "+
			"writing the machine's real cache directory", cache, home, runtime.GOOS)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatalf("creating the redirected cache root %s: %v", cache, err)
	}
	silenceGoTelemetry(t, home, osConfigDir())
	return cache
}

// createPrivateHome builds the home [Compose] hands a child, and silences go
// telemetry inside it.
//
// It is the value form's half of [resolveMovedCache]: nothing in the process is
// changed, so os.UserCacheDir cannot be asked where it would land and the
// directory is created from the rows instead. The telemetry rule is the same one
// and shares the same writer.
func createPrivateHome(t testing.TB, home string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "cache"), 0o700); err != nil {
		t.Fatalf("creating the composed home %s: %v", home, err)
	}
	silenceGoTelemetry(t, home, configDirUnder(home))
}

// osConfigDir is os.UserConfigDir with its error folded into the empty string,
// which [silenceGoTelemetry] already treats as "nowhere below the home".
func osConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return dir
}

// configDirUnder is os.UserConfigDir for a home the process has not moved to.
//
// It mirrors os.UserConfigDir's own rules, because [Compose] cannot ask: the
// process's HOME is still the developer's, so the real function would answer
// about the real home. An empty result means the config directory is not below
// the given home on this platform — Windows derives it from %AppData%, which the
// policy does not move, and a Linux with XDG_CONFIG_HOME exported has already
// been told where to look — and in both cases every go command on the machine
// reads the setting its owner chose, which is what it would have got anyway.
func configDirUnder(home string) string {
	switch runtime.GOOS {
	case "windows":
		return ""
	case "darwin", "ios":
		return filepath.Join(home, "Library", "Application Support")
	case "plan9":
		return filepath.Join(home, "lib")
	default:
		if os.Getenv("XDG_CONFIG_HOME") != "" {
			return ""
		}
		return filepath.Join(home, ".config")
	}
}

// silenceGoTelemetry turns the go command's telemetry off below the moved HOME.
//
// The telemetry directory is the one thing derived from HOME that pinning the go
// directories cannot hold still: it comes from os.UserConfigDir — under HOME on
// macOS, and on a Linux without XDG_CONFIG_HOME — and no variable names it. A go
// command that finds no mode file there starts in "local" mode, opens counters,
// and forks a sidecar that outlives it to build local reports. So a test that
// drove a single `go version` under a moved HOME left a detached process writing
// into its own temporary directory, and t.TempDir's cleanup failed with
// "directory not empty"; that is how TestDoctorPublishesItsCheckNames failed on
// macOS on 2026-09-03, on a run nothing near it had changed.
//
// The mode file is written only when the config directory is below the moved
// HOME. The machine's real telemetry setting is never touched, and a machine
// whose config directory lives elsewhere keeps whatever mode its owner chose,
// which is what every go command on it gets anyway.
func silenceGoTelemetry(t testing.TB, home, config string) {
	t.Helper()
	if config == "" || !within(home, config) {
		return
	}
	telemetry := filepath.Join(config, "go", "telemetry")
	if err := os.MkdirAll(telemetry, 0o700); err != nil {
		t.Fatalf("creating the telemetry directory below the moved HOME: %v", err)
	}
	if err := os.WriteFile(filepath.Join(telemetry, "mode"), []byte("off\n"), 0o600); err != nil {
		t.Fatalf("turning go telemetry off below the moved HOME: %v", err)
	}
}

// stripActivation removes every GO_MUTANTS_ variable from the process for the
// length of the test.
//
// This is the rule production already follows for the children it starts, and it
// is here for the same reason: a developer with GO_MUTANTS_ACTIVE exported in
// their shell would have the instrumented baseline running a mutant, and a
// GO_MUTANTS_PROBE left over from a debugging session would turn a measurement
// into a probe pass.
//
// The variable is removed rather than blanked. t.Setenv cannot unset, so it is
// set — which registers the restore this test needs and the parallel guard
// t.Setenv implies — and then unset, and the cleanup t.Setenv registered puts
// the original value back or removes the variable again.
func stripActivation(t testing.TB, cfg *envConfig) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !isActivation(name) || cfg.inherited(name) {
			continue
		}
		_, value, _ := strings.Cut(entry, "=")
		rememberStripped(t, name, value)
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("removing %s from the environment: %v", name, err)
		}
	}
}

// isActivation reports whether a variable belongs to go-mutants' own namespace.
func isActivation(name string) bool {
	return strings.HasPrefix(strings.ToUpper(name), "GO_MUTANTS_")
}

// setUnlessInherited gives a variable a value for the length of the test.
func setUnlessInherited(t testing.TB, cfg *envConfig, name, value string) {
	t.Helper()
	if cfg.inherited(name) {
		return
	}
	t.Setenv(name, value)
}

// composeFrom applies the policy to a base environment as a value.
//
// It is the single implementation of the value form, so [Compose] and
// [Environment.Vars] cannot drift apart: the second is this function over an
// environment the first has already been applied to.
func composeFrom(base []string, p policy, cfg *envConfig) []string {
	pairs := policyPairs(p)
	kept := make([]string, 0, len(base)+len(pairs))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if isActivation(name) && !cfg.inherited(name) {
			continue
		}
		kept = append(kept, entry)
	}
	overrides := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		if cfg.inherited(pair[0]) {
			continue
		}
		overrides = append(overrides, pair[0]+"="+pair[1])
	}
	return withEntries(kept, overrides...)
}

// withEntries applies `NAME=value` entries to an environment, replacing what it
// already had for those names and appending the rest in the order given.
func withEntries(base []string, kv ...string) []string {
	out := slices.Clone(base)
	for _, entry := range kv {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			// A caller that passes a bare name means "no value", which is what
			// an entry with no `=` would mean to a child anyway.
			name, entry = entry, entry+"="
		}
		if index := slices.IndexFunc(out, func(existing string) bool {
			key, _, _ := strings.Cut(existing, "=")
			return sameEnvName(key, name)
		}); index >= 0 {
			out[index] = entry
			continue
		}
		out = append(out, entry)
	}
	return out
}

// withoutEntries returns an environment with the named variables removed.
//
// Removed rather than set to the empty string, because the two are not the same
// thing to every program that reads one: git refuses an empty GIT_DIR as an
// invalid path rather than treating it as absent, so blanking a variable there
// turns every command into an error.
func withoutEntries(base []string, names ...string) []string {
	return slices.DeleteFunc(slices.Clone(base), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return slices.ContainsFunc(names, func(name string) bool { return sameEnvName(key, name) })
	})
}

// sameEnvName reports whether two names are the same environment variable.
//
// Case is folded only on Windows, where the environment really is
// case-insensitive. Folding it everywhere conflates HOME with `home` — two
// different variables on POSIX, and the policy sets both, so an override applied
// by folding would silently replace the wrong one and leave the composed
// environment a row short of what it promised.
func sameEnvName(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// within reports whether dir is base or something under it.
//
// The comparison is lexical on purpose. Both paths are built from the same
// t.TempDir, so there is no symlink between them to resolve — and resolving them
// would be the wrong question anyway, since what is being checked is that the
// path handed to the code under test was the redirected one.
func within(base, dir string) bool {
	rel, err := filepath.Rel(base, dir)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// pinned holds the machine's own directories, captured before any test moves
// HOME.
//
// os.UserCacheDir and os.UserConfigDir are derived from HOME on at least one
// platform, so once a test has moved it there is no way to ask what they were.
// Both are pure reads of the environment, so capturing them in init() costs
// nothing. This is the one assumption in the package worth writing down: a
// TestMain that moved HOME before this init() ran would capture the moved
// values, and nothing here can detect that.
var pinned struct {
	userCache  string
	userConfig string
}

func init() {
	if dir, err := os.UserCacheDir(); err == nil {
		pinned.userCache = dir
	}
	if dir, err := os.UserConfigDir(); err == nil {
		pinned.userConfig = dir
	}
}

// goDirectories are the go command's own directories, which the policy pins so
// that a moved HOME does not empty them.
type goDirectories struct {
	env      string
	path     string
	modCache string
}

// resolveGoDirectories answers once per process, and is forced from the top of
// [Env] and [Compose] while HOME is still the machine's own.
//
// It is not resolved in init(), because that would run `go env` in every test
// binary that imports this package — including the pure packages' unit tests,
// which is the tier that is supposed to need no toolchain at all and to start
// instantly. A sync.OnceValue forced from the two entry points pays for the
// probe only in a test that is redirecting an environment for a child, and pays
// for it once.
var resolveGoDirectories = sync.OnceValue(func() goDirectories {
	dirs := goDirectories{
		env:      os.Getenv("GOENV"),
		path:     os.Getenv("GOPATH"),
		modCache: os.Getenv("GOMODCACHE"),
	}
	if dirs.env != "" && dirs.path != "" && dirs.modCache != "" {
		// A machine that exports all three has already answered; asking the go
		// command would only be slower and could only agree.
		return dirs
	}

	// The go command is asked because it is the only thing that knows: GOPATH
	// and GOMODCACHE can be set in the `go env -w` file, which no environment
	// variable names and which go/build does not read.
	for name, value := range goEnvValues() {
		switch name {
		case "GOENV":
			setIfEmpty(&dirs.env, value)
		case "GOPATH":
			setIfEmpty(&dirs.path, value)
		case "GOMODCACHE":
			setIfEmpty(&dirs.modCache, value)
		}
	}

	// The fallback is what this package's ancestor did, and it is still correct
	// whenever the `go env -w` file has nothing to say: build.Default is
	// resolved when the test binary starts, which is before any redirection. A
	// machine with no `go` on PATH at all — a legitimate way to run the unit
	// tier — gets this and pays a cold module cache in the tests that then skip
	// anyway.
	if pinned.userConfig != "" {
		setIfEmpty(&dirs.env, filepath.Join(pinned.userConfig, "go", "env"))
	}
	setIfEmpty(&dirs.path, build.Default.GOPATH)
	if dirs.path != "" {
		setIfEmpty(&dirs.modCache, filepath.Join(dirs.path, "pkg", "mod"))
	}
	return dirs
})

// goEnvValues runs `go env` once for the three directories, and returns nothing
// at all if there is no go command to run or it does not answer.
//
// The probe carries two settings of its own. GOTOOLCHAIN=local keeps a `go`
// whose go.mod asks for another toolchain from downloading one before it will
// answer a question about directories, and GOWORK=off keeps a `go.work` above
// the working directory from changing what it reports. Neither is what is being
// asked about, and both can make the probe slow or fail.
func goEnvValues() map[string]string {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return nil
	}
	names := []string{"GOENV", "GOPATH", "GOMODCACHE"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gobin, append([]string{"env"}, names...)...)
	cmd.Env = withEntries(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(string(out)), "\r\n", "\n"), "\n")
	if len(lines) != len(names) {
		return nil
	}
	values := make(map[string]string, len(names))
	for i, name := range names {
		values[name] = strings.TrimSpace(lines[i])
	}
	return values
}

// setIfEmpty assigns only when the destination has nothing.
func setIfEmpty(dst *string, value string) {
	if *dst == "" {
		*dst = value
	}
}
