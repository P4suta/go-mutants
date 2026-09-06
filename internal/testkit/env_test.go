// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestEnvTurnsGoTelemetryOffInTheMovedHome pins the one file [Env] writes into
// the HOME it moves.
//
// The go command derives its telemetry directory from os.UserConfigDir, which
// on macOS — and on a Linux without XDG_CONFIG_HOME — is derived from HOME, and
// there is no variable of its own to pin it with. A go command that finds no
// mode file there starts in "local" mode: it opens counters and forks a sidecar
// that outlives it. So a test that drove a single `go version` under a moved
// HOME left a detached process writing into its own temporary directory, and
// t.TempDir's cleanup failed with "directory not empty" — which is how
// TestDoctorPublishesItsCheckNames failed on macOS on 2026-09-03. Telemetry is
// turned off in the moved HOME, and only there.
func TestEnvTurnsGoTelemetryOffInTheMovedHome(t *testing.T) {
	e := Env(t)
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("os.UserConfigDir after moving HOME: %v", err)
	}
	if !within(e.Home, config) {
		t.Skipf("%s derives the config directory from something other than HOME (%s), so nothing is written", runtime.GOOS, config)
	}

	data := ReadFile(t, filepath.Join(config, "go", "telemetry", "mode"))
	if mode, _, _ := strings.Cut(strings.TrimSpace(string(data)), " "); mode != "off" {
		t.Errorf("the telemetry mode below the moved HOME is %q, want %q", mode, "off")
	}
}

// TestEnvRedirectsTheUserCacheDirectoryIntoTheTest is the assertion that would
// have caught the defect this whole helper replaces.
//
// os.UserCacheDir reads a different variable on every platform — %LocalAppData%
// on Windows, $HOME/Library/Caches on macOS, $XDG_CACHE_HOME or $HOME/.cache
// elsewhere — and three packages here each set two of them. That covers Linux
// and Windows and covers nothing at all on macOS, where the tests wrote their
// fixtures into a temporary directory and read the developer's real
// ~/Library/Caches/go-mutants back. Two green platforms is exactly as much
// evidence as one when the third reads a variable nobody set, so the check is
// not on the list of variables: it asks os.UserCacheDir where it actually
// landed.
func TestEnvRedirectsTheUserCacheDirectoryIntoTheTest(t *testing.T) {
	e := Env(t)

	dir, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("os.UserCacheDir after redirecting it: %v", err)
	}
	if !SamePath(dir, e.Cache) {
		t.Errorf("os.UserCacheDir = %s, but Env reported %s", dir, e.Cache)
	}
	if !within(e.Home, dir) {
		t.Fatalf("os.UserCacheDir resolves to %s, which is outside this test's own %s: %s reads a "+
			"variable the policy does not set, and the test would be reading and writing the "+
			"machine's real cache directory", dir, e.Home, runtime.GOOS)
	}
	if info, err := os.Stat(e.Cache); err != nil || !info.IsDir() {
		t.Errorf("the redirected cache root %s is not a directory that exists: %v", e.Cache, err)
	}
}

// TestEnvRedirectsTheTemporaryDirectoryOnEveryPlatform is what makes "nothing
// was left behind" an assertion rather than a guess.
//
// A machine running `go test ./...` has several packages writing into the shared
// temporary directory at once, so a leak of one is indistinguishable from a leak
// of another. os.TempDir reads TMPDIR on POSIX and TMP then TEMP on Windows, and
// all three are set rather than guessing which platform is reading — the guess
// is what left the cli suite asserting on a directory it did not own.
func TestEnvRedirectsTheTemporaryDirectoryOnEveryPlatform(t *testing.T) {
	e := Env(t)

	if !SamePath(os.TempDir(), e.Scratch) {
		t.Errorf("os.TempDir = %s, want the scratch directory %s", os.TempDir(), e.Scratch)
	}
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		if got := os.Getenv(name); got != e.Scratch {
			t.Errorf("%s = %q, want %q", name, got, e.Scratch)
		}
		if got, _ := lookupEnv(e.Vars(), name); got != e.Scratch {
			t.Errorf("the composed %s = %q, want %q", name, got, e.Scratch)
		}
	}
	if got := Entries(t, e.Scratch); len(got) != 0 {
		t.Errorf("the scratch directory is not empty at the start of the test: %q", got)
	}
}

// TestEnvPinsGoCacheToTheTestOwnedDirectory keeps the suites' child `go`
// commands out of the developer's build cache.
//
// The bloat is not go-mutants compiling itself: it is the thousands of `go
// build`, `go test -c` and `go list` commands the suites drive against fixtures
// and synthesized modules, each keyed on an absolute path that exists for one
// run. That is what took ~/.cache/go-build to 14 GB on the machine this was
// written on. A cache per test is the other failure — the standard library
// recompiled once per test — so it is one dedicated, persistent directory that a
// developer or CI can name and delete whole.
func TestEnvPinsGoCacheToTheTestOwnedDirectory(t *testing.T) {
	e := Env(t)

	want, err := BuildCache()
	if err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
	if e.GoCache != want {
		t.Errorf("Env reported GoCache %s, want %s", e.GoCache, want)
	}
	if !filepath.IsAbs(e.GoCache) {
		t.Errorf("GOCACHE %s is not absolute, and the go command refuses a relative one", e.GoCache)
	}
	if within(e.Home, e.GoCache) {
		t.Errorf("GOCACHE %s is below the moved HOME %s, so every test starts with a cold cache", e.GoCache, e.Home)
	}
	if got := os.Getenv("GOCACHE"); got != e.GoCache {
		t.Errorf("GOCACHE = %q, want %q", got, e.GoCache)
	}
	if got, _ := lookupEnv(e.Vars(), "GOCACHE"); got != e.GoCache {
		t.Errorf("the composed GOCACHE = %q, want %q", got, e.GoCache)
	}
}

// TestBuildCacheEntriesCountsWhatWasCompiledRatherThanWhatWasOpened is the
// distinction the helper exists for.
//
// A go command that opens a cache creates all 256 shard directories, a README
// and a trim record before it has written a single entry, so "the directory has
// things in it" is true of a cache nothing ever compiled into. A test asking
// where a compile went therefore has to look past the bookkeeping, and getting
// that wrong makes the assertion pass for the failure it was written to catch.
func TestBuildCacheEntriesCountsWhatWasCompiledRatherThanWhatWasOpened(t *testing.T) {
	t.Parallel()

	cache := t.TempDir()
	for _, name := range []string{"README", "trim.txt", "lock", BuildCacheMarker} {
		WriteFile(t, filepath.Join(cache, name), []byte("bookkeeping\n"))
	}
	for _, shard := range []string{"00", "a3", "ff"} {
		if err := os.MkdirAll(filepath.Join(cache, shard), 0o700); err != nil {
			t.Fatalf("creating the shard %s: %v", shard, err)
		}
	}
	if got := BuildCacheEntries(t, cache); got != 0 {
		t.Errorf("an opened cache holds %d compiled entries, want 0", got)
	}

	WriteFile(t, filepath.Join(cache, "a3", "a3f0ab-d"), []byte("an object\n"))
	WriteFile(t, filepath.Join(cache, "a3", "a3f0ab-a"), []byte("its action\n"))
	if got := BuildCacheEntries(t, cache); got != 2 {
		t.Errorf("a cache holding two entries counted %d", got)
	}

	if got := BuildCacheEntries(t, filepath.Join(cache, "never-created")); got != 0 {
		t.Errorf("a cache directory that does not exist counted %d entries, want 0", got)
	}
}

// TestBuildCacheHonoursTheNamedDirectory is the CI half of the same rule: a
// runner names a cache under its own temporary area, which dies with the runner
// and is never restored from an actions cache, so a job cannot inherit
// yesterday's 14 GB.
func TestBuildCacheHonoursTheNamedDirectory(t *testing.T) {
	named := filepath.Join(t.TempDir(), "named-cache")
	t.Setenv(BuildCacheEnv, named)

	got, err := BuildCache()
	if err != nil {
		t.Fatalf("BuildCache with %s set: %v", BuildCacheEnv, err)
	}
	if got != named {
		t.Errorf("BuildCache = %s, want the named %s", got, named)
	}
	if e := Env(t); e.GoCache != named {
		t.Errorf("Env used %s rather than the named %s", e.GoCache, named)
	}

	// The variable names itself out of the environment: [Env] removes every
	// GO_MUTANTS_ variable, this one included, so a BuildCache or a Compose
	// called after the redirection would resolve the default and send the child
	// to a cache the caller had explicitly moved away from. CI is exactly this
	// arrangement — the variable is set once for the job, and every test then
	// redirects its own environment.
	if _, present := os.LookupEnv(BuildCacheEnv); present {
		t.Fatalf("Env left %s in the environment", BuildCacheEnv)
	}
	if got, err := BuildCache(); err != nil || got != named {
		t.Errorf("BuildCache after Env = %s (%v), want the named %s", got, err, named)
	}
	if got, _ := lookupEnv(Compose(t, t.TempDir()), "GOCACHE"); got != named {
		t.Errorf("the composed GOCACHE after Env = %q, want the named %q", got, named)
	}

	t.Setenv(BuildCacheEnv, filepath.Join("relative", "cache"))
	if got, err := BuildCache(); err == nil {
		t.Errorf("BuildCache with a relative %s returned %s, want an error naming the variable", BuildCacheEnv, got)
	} else if !strings.Contains(err.Error(), BuildCacheEnv) {
		t.Errorf("the error does not name the variable: %v", err)
	}
}

// TestPathAgreesWithTestkit is the seam between this package and the tool that
// cleans up after it.
//
// internal/devtools/testcache resolves the same directory from the same rule,
// and it may not import this package to do it: it is a production `main`, and
// nothing that ships may link the testing package. So the rule exists twice —
// once here for the suites, once there for `mise run test-clean`,
// `test-cache-status` and the `exec` wrapper the long tasks run through — and
// this is what keeps the two copies the same. If they ever drift, one half of
// the feature fills a directory the other half never empties, and the only
// symptom is a disk that fills up a month later.
//
// The home is kept rather than moved, and that is the point of the test rather
// than a shortcut: the tool resolves os.UserCacheDir in its own process, this
// package pins the real one at init() before any test redirects it, and the two
// answers can only be compared under the home they were both derived from.
func TestPathAgreesWithTestkit(t *testing.T) {
	t.Run("the default directory", func(t *testing.T) {
		// Cleared first, because CI names a cache for the whole job and this
		// subtest is about the rule that applies when nobody has. Without this
		// the two sides are asked different questions: the child is handed an
		// environment [Env] has stripped the variable out of, so it resolves the
		// default, while BuildCache here reads the value through that stripping
		// and resolves the job's — and the test fails on CI and only on CI,
		// saying the two copies have drifted when they agree perfectly. An
		// explicitly empty value means "no override" on both sides;
		// TestAnExplicitlyEmptyBuildCacheOverrideMeansTheDefault is where that is
		// pinned.
		t.Setenv(BuildCacheEnv, "")

		e := Env(t, KeepHome())
		want, err := BuildCache()
		if err != nil {
			t.Fatalf("BuildCache: %v", err)
		}
		if value, ok := lookupEnv(e.Vars(), BuildCacheEnv); ok && value != "" {
			t.Fatalf("the child would be handed %s=%s, so it would not be resolving the default at all",
				BuildCacheEnv, value)
		}
		if got := testcacheSays(t, e.Vars(), "path"); !SamePath(got, want) {
			t.Errorf("`testcache path` printed %s, but testkit resolved %s: the two copies of the "+
				"rule have drifted, and the suites are filling a directory nothing empties", got, want)
		}
	})

	t.Run("the named directory", func(t *testing.T) {
		e := Env(t, KeepHome())
		named := filepath.Join(t.TempDir(), "named-cache")
		// Named after [Env] rather than before it, so that the `go run` which
		// builds the tool still compiles into the shared build cache: what is
		// being compared here is the rule, not how long a cold cache takes to
		// fill. The child is then handed the variable explicitly, which is
		// exactly the arrangement CI runs in — the job names a directory, and
		// every test redirects its own environment underneath it.
		t.Setenv(BuildCacheEnv, named)
		want, err := BuildCache()
		if err != nil {
			t.Fatalf("BuildCache with %s set: %v", BuildCacheEnv, err)
		}
		if want != named {
			t.Fatalf("BuildCache resolved %s rather than the named %s, so this test would compare "+
				"the tool against the wrong answer", want, named)
		}
		if got := testcacheSays(t, e.With(BuildCacheEnv+"="+named), "path"); !SamePath(got, want) {
			t.Errorf("`testcache path` printed %s, but testkit resolved the named %s", got, want)
		}
	})

	t.Run("the kept scratch root", func(t *testing.T) {
		e := Env(t, KeepHome())

		// T5: replace with testkit.KeepRoot(). Until the keep policy exists there
		// is no function here to compare against, so the rule is written out once
		// — and writing it out is itself the point: when KeepRoot arrives it has
		// to produce this, and this test is where the two meet.
		want := filepath.Join(pinned.userCache, "go-mutants-test", "kept")
		if got := testcacheSays(t, e.Vars(), "path", "--kept"); !SamePath(got, want) {
			t.Errorf("`testcache path --kept` printed %s, want %s", got, want)
		}

		named := filepath.Join(t.TempDir(), "named-kept")
		if got := testcacheSays(t, e.With(KeepDirEnv+"="+named), "path", "--kept"); !SamePath(got, named) {
			t.Errorf("`testcache path --kept` printed %s, want the named %s", got, named)
		}
	})
}

// TestAnExplicitlyEmptyBuildCacheOverrideMeansTheDefault is the rule that lets a
// test opt out of a cache the job named.
//
// [BuildCache] reads the variable through three layers — the live environment,
// what [Env] stripped out of it during this test, and the value the process
// started with — because a test that redirects its environment must not silently
// lose a directory CI named. The cost of that fallback is that "unset" cannot be
// expressed by unsetting: t.Setenv cannot remove a variable, and the process's
// starting value would answer for it anyway. So an explicitly empty value has to
// mean the default, and it has to mean it at every layer, or a test that clears
// the variable gets the job's cache back from underneath itself and compares two
// different questions. That is exactly what made TestPathAgreesWithTestkit fail
// on CI and pass on every developer machine.
func TestAnExplicitlyEmptyBuildCacheOverrideMeansTheDefault(t *testing.T) {
	if pinned.userCache == "" {
		t.Skip("this platform has no user cache directory, so there is no default to fall back to")
	}
	want := filepath.Join(pinned.userCache, "go-mutants-test", "go-build")

	// The arrangement CI runs in: a directory named for the whole job, before
	// this test says anything.
	t.Setenv(BuildCacheEnv, filepath.Join(t.TempDir(), "named-by-the-job"))
	t.Setenv(BuildCacheEnv, "")

	got, err := BuildCache()
	if err != nil {
		t.Fatalf("BuildCache with an empty %s: %v", BuildCacheEnv, err)
	}
	if got != want {
		t.Errorf("BuildCache with an empty %s = %s, want the default %s", BuildCacheEnv, got, want)
	}

	// And still, once Env has removed the whole GO_MUTANTS_ namespace from the
	// process: the empty value is what it remembers, so the fallback answers with
	// it rather than reaching further back.
	e := Env(t, KeepHome())
	if e.GoCache != want {
		t.Errorf("Env used %s, want the default %s", e.GoCache, want)
	}
	if got, err := BuildCache(); err != nil || got != want {
		t.Errorf("BuildCache after Env = %s (%v), want the default %s", got, err, want)
	}
	if got, _ := lookupEnv(Compose(t, t.TempDir()), "GOCACHE"); got != want {
		t.Errorf("the composed GOCACHE = %q, want the default %q", got, want)
	}
}

// TestMarkerNamesAgreeWithTestcache is the second half of the ownership rule,
// and the half that decides whether the first half does anything at all.
//
// The harness stamps the build cache it resolves, and the collector removes only
// a directory carrying that stamp. Two spellings of the file name would not fail
// anywhere: the harness would write one file, the collector would look for
// another, `mise run test-clean` would politely refuse to empty a cache that had
// been the harness's all along, and the only symptom would be a directory that
// grew until somebody noticed. Neither package can import the other, so this is
// what holds them together.
func TestMarkerNamesAgreeWithTestcache(t *testing.T) {
	e := Env(t, KeepHome())

	if got := testcacheSays(t, e.Vars(), "path", "--marker"); got != BuildCacheMarker {
		t.Errorf("`testcache path --marker` printed %q, but this package writes %q", got, BuildCacheMarker)
	}
	// The kept root's marker is the tool's business alone until the keep policy
	// lands, so this asserts only that it is a different file: one name for both
	// would make emptying a build cache license emptying the kept diagnostics.
	kept := testcacheSays(t, e.Vars(), "path", "--kept", "--marker")
	if kept == BuildCacheMarker || kept == "" {
		t.Errorf("the kept scratch marker is %q, want a name of its own", kept)
	}
}

// TestEnvStampsTheBuildCacheItOwns is the harness taking responsibility for the
// directory it names.
//
// Nothing in a path says who made it. `GO_MUTANTS_TEST_GOCACHE=$HOME` is an
// absolute path like any other, and a collector that trusted the variable would
// run `go clean -cache` and os.RemoveAll against a home directory. So ownership
// is written down: whatever resolves this directory creates it and leaves a file
// saying what it is, and internal/devtools/testcache removes nothing that does
// not carry that file. The harness is the owner and the tool is the collector,
// which is why the stamp is written here rather than only there — a developer
// who has run the suites once has a cache the collector can empty, without
// having run the collector first.
func TestEnvStampsTheBuildCacheItOwns(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "go-build")
	t.Setenv(BuildCacheEnv, cache)

	e := Env(t)
	if e.GoCache != cache {
		t.Fatalf("Env used %s rather than the named %s", e.GoCache, cache)
	}
	marker := filepath.Join(cache, BuildCacheMarker)
	first := ReadFile(t, marker)
	if len(first) == 0 {
		t.Errorf("%s is empty, and it is the only explanation whoever finds this directory gets", marker)
	}

	// Idempotent, because every test in the repository calls one of these.
	Env(t)
	_ = Compose(t, t.TempDir())
	if got := ReadFile(t, marker); string(got) != string(first) {
		t.Errorf("a second resolution rewrote the marker:\n%s", got)
	}
}

// TestEnvDoesNotStampADirectoryItDidNotMake is the sharp edge of that rule.
//
// A stamp written into whatever the variable happened to name would be a
// permission slip the harness issues to the collector on somebody else's
// behalf: point the variable at a directory full of work, run any test in this
// repository, and `mise run test-clean` would then delete it with the marker's
// blessing. A directory that already holds files that are not ours is left
// exactly as it is — the cache still works, and nothing will ever remove it,
// which is the right half of the trade to keep.
func TestEnvDoesNotStampADirectoryItDidNotMake(t *testing.T) {
	occupied := t.TempDir()
	WriteFile(t, filepath.Join(occupied, "thesis.txt"), []byte("chapter one\n"))
	t.Setenv(BuildCacheEnv, occupied)

	Env(t)

	if _, err := os.Lstat(filepath.Join(occupied, BuildCacheMarker)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the harness stamped a directory full of somebody else's files: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(occupied, "thesis.txt")); err != nil {
		t.Errorf("the file that was already there is gone: %v", err)
	}
}

// testcacheSays runs the devtool and returns the single line it prints.
//
// `go run` rather than a built binary, because the tool has no install step and
// every task in mise.toml invokes it exactly this way: what is tested is what
// runs. It is the only child in this file, and it obeys the same skip-or-fail
// policy as everything else that needs a toolchain — a developer without `go`
// skips, and a CI job without one fails.
func testcacheSays(t testing.TB, env []string, args ...string) string {
	t.Helper()
	gobin := GoBinary(t)
	argv := append([]string{gobin, "run", "./internal/devtools/testcache"}, args...)
	result := Exec(t, Root(t), env, argv...)
	RequireExit(t, result, 0, "`go run ./internal/devtools/testcache "+strings.Join(args, " ")+"`")
	return strings.TrimSpace(string(result.Stdout))
}

// TestEnvStripsActivationAndPinsTheGoSettings is the rule production already
// follows for the children it starts, applied to the children a test starts.
//
// A developer with GO_MUTANTS_ACTIVE exported in their shell would otherwise
// have the instrumented baseline running a mutant, and every suite here that
// composes an environment strips the prefix for that reason. The go settings are
// the neighbouring hazard: a fixture with no dependencies must never reach the
// network to build, a go.work above the temporary directory must not join itself
// to the snapshot, a GOTOOLCHAIN that downloads another compiler must not be
// reachable, and a GOFLAGS from the developer's shell must not decide what any
// of it resolves against.
func TestEnvStripsActivationAndPinsTheGoSettings(t *testing.T) {
	t.Setenv("GO_MUTANTS_ACTIVE", "some-mutant-id")
	t.Setenv("GO_MUTANTS_PROBE", "1")
	t.Setenv("GOFLAGS", "-mod=mod -tags=whatever")
	t.Setenv("GOWORK", "/somewhere/go.work")

	e := Env(t)

	for _, name := range []string{"GO_MUTANTS_ACTIVE", "GO_MUTANTS_PROBE"} {
		if value, ok := os.LookupEnv(name); ok {
			t.Errorf("%s is still set in the process, to %q", name, value)
		}
	}
	for _, entry := range e.Vars() {
		if strings.HasPrefix(strings.ToUpper(entry), "GO_MUTANTS_") {
			t.Errorf("the composed environment still carries %q", entry)
		}
	}

	want := map[string]string{
		"GOWORK":      "off",
		"GOPROXY":     "off",
		"GOSUMDB":     "off",
		"GOTOOLCHAIN": "local",
		"GOFLAGS":     "-mod=readonly",
	}
	for name, value := range want {
		if got := os.Getenv(name); got != value {
			t.Errorf("%s = %q in the process, want %q", name, got, value)
		}
		if got, _ := lookupEnv(e.Vars(), name); got != value {
			t.Errorf("the composed %s = %q, want %q", name, got, value)
		}
	}
}

// TestEnvKeepsTheModuleCacheWarm is the reason the real go directories are
// captured before anything moves HOME.
//
// The build cache is not the only thing derived from HOME: so are the module
// cache, GOPATH and the `go env -w` file, on the same platforms and from the
// same variable. Moving HOME without pinning them hands every `go build` a
// test drives an empty module cache — dependencies re-downloaded, or
// unresolvable with GOPROXY=off — and none of that is part of what any of these
// tests measures.
func TestEnvKeepsTheModuleCacheWarm(t *testing.T) {
	e := Env(t)

	for _, name := range []string{"GOPATH", "GOMODCACHE", "GOENV"} {
		value, ok := lookupEnv(e.Vars(), name)
		if !ok || value == "" {
			t.Errorf("the composed environment does not pin %s", name)
			continue
		}
		if !filepath.IsAbs(value) {
			t.Errorf("%s = %q, which is not absolute", name, value)
		}
		if within(e.Home, value) {
			t.Errorf("%s = %s, which is below the moved HOME %s: the cache would be cold on every "+
				"platform that derives it from HOME", name, value, e.Home)
		}
		if got := os.Getenv(name); got != value {
			t.Errorf("%s = %q in the process, want the composed %q", name, got, value)
		}
	}
}

// TestComposeMatchesEnvWithoutTouchingTheProcess is what lets a suite be
// parallel.
//
// [Env] calls t.Setenv, and a test that calls t.Setenv may not call t.Parallel —
// which is why not one integration test in this repository was parallel. A test
// whose children are all started with an explicit environment does not need the
// process changed at all, so the policy is available as a value; the two forms
// have to apply the same rows, or "parallelise this suite" would quietly mean
// "change what its children see".
//
// The process is deliberately dirty before Compose is called, and Compose is
// called *before* Env: composing over an environment Env has already made
// hermetic would prove nothing at all, since every row would already hold the
// value being asserted.
func TestComposeMatchesEnvWithoutTouchingTheProcess(t *testing.T) {
	t.Setenv("GO_MUTANTS_ACTIVE", "a-mutant-from-the-shell")
	t.Setenv("GO_MUTANTS_PROBE", "1")
	t.Setenv("GOFLAGS", "-mod=mod -tags=dirty")
	t.Setenv("GOWORK", filepath.Join(t.TempDir(), "go.work"))
	t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(t.TempDir(), "summary.md"))
	dirty := slices.Sorted(slices.Values(os.Environ()))

	scratch := t.TempDir()
	composed := Compose(t, scratch)

	after := slices.Sorted(slices.Values(os.Environ()))
	if !slices.Equal(dirty, after) {
		t.Errorf("Compose changed the process environment:\nremoved: %q\nadded: %q",
			difference(dirty, after), difference(after, dirty))
	}
	for name, want := range map[string]string{
		"GO_MUTANTS_ACTIVE": "a-mutant-from-the-shell",
		"GOFLAGS":           "-mod=mod -tags=dirty",
	} {
		if got := os.Getenv(name); got != want {
			t.Errorf("Compose changed the process's %s to %q, want the dirty %q", name, got, want)
		}
	}

	// Row for row against the policy table, on an environment that disagreed
	// with every one of them.
	home := filepath.Join(scratch, "home")
	cache := filepath.Join(home, "cache")
	global, system := absentGitConfig(scratch)
	gocache, err := BuildCache()
	if err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
	dirs := resolveGoDirectories()
	want := map[string]string{
		"TMPDIR": scratch, "TMP": scratch, "TEMP": scratch,
		"HOME": home, "home": home, "USERPROFILE": home,
		"XDG_CACHE_HOME": cache, "LocalAppData": cache,
		"GOCACHE": gocache,
		"GOENV":   dirs.env, "GOPATH": dirs.path, "GOMODCACHE": dirs.modCache,
		"GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off",
		"GOTOOLCHAIN": "local", "GOFLAGS": "-mod=readonly",
		"GIT_CONFIG_GLOBAL": global, "GIT_CONFIG_SYSTEM": system,
		"GIT_AUTHOR_NAME": GitName, "GIT_AUTHOR_EMAIL": GitEmail, "GIT_AUTHOR_DATE": GitDate,
		"GIT_COMMITTER_NAME": GitName, "GIT_COMMITTER_EMAIL": GitEmail, "GIT_COMMITTER_DATE": GitDate,
		"GITHUB_STEP_SUMMARY": "",
	}
	for name, value := range want {
		if value == "" && (name == "GOENV" || name == "GOPATH" || name == "GOMODCACHE") {
			continue
		}
		got, ok := lookupEnv(composed, name)
		if !ok {
			t.Errorf("the composed environment has no %s at all", name)
			continue
		}
		if got != value {
			t.Errorf("the composed %s = %q, want %q", name, got, value)
		}
	}
	for _, entry := range composed {
		if isActivation(entry) {
			t.Errorf("the composed environment still carries %q", entry)
		}
	}
	if got := os.Getenv("PATH"); got != "" {
		if composedPath, _ := lookupEnv(composed, "PATH"); composedPath != got {
			t.Errorf("the composed PATH = %q, want the inherited %q", composedPath, got)
		}
	}

	// And the process form applies the same rows to the process.
	e := Env(t)
	for name, value := range want {
		switch name {
		// The two forms own different directories: Env's are the test's own
		// t.TempDirs, Compose's are derived from the scratch it was handed.
		case "TMPDIR", "TMP", "TEMP", "HOME", "home", "USERPROFILE",
			"XDG_CACHE_HOME", "LocalAppData", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM":
			continue
		}
		if value == "" && (name == "GOENV" || name == "GOPATH" || name == "GOMODCACHE") {
			continue
		}
		if got := os.Getenv(name); got != value {
			t.Errorf("Env set %s to %q in the process, want %q", name, got, value)
		}
	}
	if os.Getenv("HOME") != e.Home {
		t.Errorf("Env's process HOME is %q but it reported %q", os.Getenv("HOME"), e.Home)
	}
	if e.Home == "" || SamePath(e.Home, home) {
		t.Errorf("Env's home is %q, want a private directory of its own rather than Compose's", e.Home)
	}
}

// TestComposeGivesTheChildAPrivateHome is the row [Compose] used not to carry,
// and it is the one that matters most for a parallel suite.
//
// A child `go` command with the developer's HOME writes into the developer's
// telemetry directory, reads their `go env -w` file through it, and — on macOS
// and on a Linux without XDG_CACHE_HOME — files go-mutants' own history and
// outcome cache into their real cache directory. The value form has to move the
// home for the same reason the process form does; the only difference is that it
// derives it from the scratch it was handed rather than from a t.TempDir of its
// own.
func TestComposeGivesTheChildAPrivateHome(t *testing.T) {
	t.Parallel()

	scratch := t.TempDir()
	composed := Compose(t, scratch)
	home := filepath.Join(scratch, "home")

	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		t.Fatalf("the private home %s is not a directory that exists: %v", home, err)
	}
	for _, name := range []string{"HOME", "home", "USERPROFILE"} {
		if got, _ := lookupEnv(composed, name); got != home {
			t.Errorf("the composed %s = %q, want %q", name, got, home)
		}
	}

	config := configDirUnder(home)
	if config == "" {
		t.Skipf("%s derives the config directory from something the policy does not move, so nothing is written", runtime.GOOS)
	}
	data := ReadFile(t, filepath.Join(config, "go", "telemetry", "mode"))
	if mode, _, _ := strings.Cut(strings.TrimSpace(string(data)), " "); mode != "off" {
		t.Errorf("the telemetry mode below the composed home is %q, want %q", mode, "off")
	}
}

// TestComposeRefusesAScratchItCannotUse fails at the call rather than in the
// child.
//
// Every row of the policy is derived from the scratch directory — the temporary
// directory, the private home, the absent git configuration — so a scratch that
// is empty, relative or missing produces an environment whose failures all
// surface as something else: a `go build` that cannot create a temporary file, a
// git that reads a configuration file that turned out to exist, a HOME the child
// creates in the current working directory.
func TestComposeRefusesAScratchItCannotUse(t *testing.T) {
	t.Parallel()

	for _, scratch := range []string{"", filepath.Join("relative", "scratch"), filepath.Join(t.TempDir(), "does-not-exist")} {
		rec := expectFatal(t, func(tb testing.TB) { Compose(tb, scratch) })
		if report := rec.first(t, "Compose("+strconv.Quote(scratch)+")"); !strings.Contains(report, "scratch") {
			t.Errorf("the refusal of %q does not say what was wrong:\n%s", scratch, report)
		}
	}
}

// TestEnvNeutralisesGit keeps a developer's own ~/.gitconfig out of what these
// tests observe, and keeps every commit a test makes byte-identical.
//
// The configuration files are pointed at paths that do not exist rather than at
// empty ones, because an empty file is still a file somebody's tooling can
// write into. The identity and the dates are fixed because a commit's hash is
// derived from them: without that, two runs of the same test produce two
// different repositories and nothing about a commit can be asserted.
func TestEnvNeutralisesGit(t *testing.T) {
	e := Env(t)

	for _, name := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		path := os.Getenv(name)
		if path == "" {
			t.Errorf("%s is not set", name)
			continue
		}
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s = %s, which exists; git would read it", name, path)
		}
		if got, _ := lookupEnv(e.Vars(), name); got != path {
			t.Errorf("the composed %s = %q, want %q", name, got, path)
		}
	}

	want := map[string]string{
		"GIT_AUTHOR_NAME":     GitName,
		"GIT_AUTHOR_EMAIL":    GitEmail,
		"GIT_AUTHOR_DATE":     GitDate,
		"GIT_COMMITTER_NAME":  GitName,
		"GIT_COMMITTER_EMAIL": GitEmail,
		"GIT_COMMITTER_DATE":  GitDate,
	}
	for name, value := range want {
		if got := os.Getenv(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
		if got, _ := lookupEnv(e.Vars(), name); got != value {
			t.Errorf("the composed %s = %q, want %q", name, got, value)
		}
	}
}

// TestEnvBlanksTheGitHubStepSummary keeps a run under `act`, or a test run
// inside a CI job, from appending to the job's own summary file: the console
// writes a step summary when the variable names one, and every test but the one
// about that behaviour is not a job.
func TestEnvBlanksTheGitHubStepSummary(t *testing.T) {
	t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(t.TempDir(), "summary.md"))
	e := Env(t)

	if got := os.Getenv("GITHUB_STEP_SUMMARY"); got != "" {
		t.Errorf("GITHUB_STEP_SUMMARY = %q, want it blank", got)
	}
	if got, ok := lookupEnv(e.Vars(), "GITHUB_STEP_SUMMARY"); !ok || got != "" {
		t.Errorf("the composed GITHUB_STEP_SUMMARY = %q (present: %v), want it present and blank", got, ok)
	}
}

// TestEnvKeepHomeLeavesTheRealHomeAlone covers the one case the policy cannot
// serve: a test whose subject is what the *user's* configuration does.
func TestEnvKeepHomeLeavesTheRealHomeAlone(t *testing.T) {
	// os.UserHomeDir rather than $HOME: on Windows the home directory comes from
	// %USERPROFILE% and $HOME is very often unset, so reading $HOME would have
	// compared two empty strings and passed without asserting anything.
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("this platform has no user home directory to compare against: %v", err)
	}
	if realHome == "" {
		t.Fatal("os.UserHomeDir returned an empty path, so this test would assert nothing")
	}
	realCache, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("this platform has no user cache directory to compare against: %v", err)
	}

	e := Env(t, KeepHome())

	if got, err := os.UserHomeDir(); err != nil || !SamePath(got, realHome) {
		t.Errorf("os.UserHomeDir = %q (%v), want the real %q", got, err, realHome)
	}
	if !SamePath(e.Home, realHome) {
		t.Errorf("Env reported Home %s, want the real %s", e.Home, realHome)
	}
	if !SamePath(e.Cache, realCache) {
		t.Errorf("Env reported Cache %s, want the real %s", e.Cache, realCache)
	}
	if !SamePath(os.TempDir(), e.Scratch) {
		t.Errorf("KeepHome also kept the temporary directory: os.TempDir = %s", os.TempDir())
	}
}

// TestEnvInheritKeepsTheNamedVariables is the escape hatch, and it is named
// rather than general: a test that needs `-mod=mod`, or that is *about* a
// GO_MUTANTS_ variable reaching a child, says which one it is keeping and keeps
// the rest of the policy.
func TestEnvInheritKeepsTheNamedVariables(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GO_MUTANTS_ACTIVE", "kept-mutant")

	e := Env(t, Inherit("GOFLAGS", "GO_MUTANTS_ACTIVE"))

	if got := os.Getenv("GOFLAGS"); got != "-mod=mod" {
		t.Errorf("GOFLAGS = %q, want the inherited %q", got, "-mod=mod")
	}
	if got := os.Getenv("GO_MUTANTS_ACTIVE"); got != "kept-mutant" {
		t.Errorf("GO_MUTANTS_ACTIVE = %q, want the inherited %q", got, "kept-mutant")
	}
	if got, _ := lookupEnv(e.Vars(), "GOFLAGS"); got != "-mod=mod" {
		t.Errorf("the composed GOFLAGS = %q, want the inherited %q", got, "-mod=mod")
	}
	if got, _ := lookupEnv(e.Vars(), "GO_MUTANTS_ACTIVE"); got != "kept-mutant" {
		t.Errorf("the composed GO_MUTANTS_ACTIVE = %q, want the inherited %q", got, "kept-mutant")
	}
	if got := os.Getenv("GOPROXY"); got != "off" {
		t.Errorf("inheriting two variables changed the rest of the policy: GOPROXY = %q", got)
	}
}

// TestVarsIsACopyAndWithOverrides pins the two things a caller does with the
// composed environment: hand it to a child, and hand a child one more variable.
//
// Vars returns a copy because a caller that appended to the slice it was handed
// would be appending to the next caller's environment; With overrides in place
// rather than appending a second entry for the same name, because a duplicate
// key is resolved differently by os/exec, by the C library and by a shell.
func TestVarsIsACopyAndWithOverrides(t *testing.T) {
	e := Env(t)

	first := e.Vars()
	first[0] = "SCRIBBLE=1"
	if e.Vars()[0] == "SCRIBBLE=1" {
		t.Error("writing to the slice Vars returned changed the environment behind it")
	}

	got := e.With("GOFLAGS=-mod=mod", "TESTKIT_EXTRA=yes")
	if value, _ := lookupEnv(got, "GOFLAGS"); value != "-mod=mod" {
		t.Errorf("With did not override GOFLAGS: %q", value)
	}
	if value, _ := lookupEnv(got, "TESTKIT_EXTRA"); value != "yes" {
		t.Errorf("With did not add TESTKIT_EXTRA: %q", value)
	}
	if count := countEnv(got, "GOFLAGS"); count != 1 {
		t.Errorf("GOFLAGS appears %d times in the composed environment, want once", count)
	}
	if value, _ := lookupEnv(e.Vars(), "GOFLAGS"); value != "-mod=readonly" {
		t.Errorf("With changed the environment it was called on: GOFLAGS = %q", value)
	}
}

// TestEnvRefusesToInheritTheHomeItIsMoving turns an impossible combination into
// a message about the combination.
//
// Inheriting HOME while the policy moves it leaves os.UserCacheDir resolving to
// the developer's real cache directory, which trips the guard in the move — and
// that guard's message is about a platform reading a variable nobody set, which
// is exactly the wrong thing to read when the cause is two options that cannot
// both hold. A test that wants the real home wants KeepHome.
func TestEnvRefusesToInheritTheHomeItIsMoving(t *testing.T) {
	for _, name := range []string{"HOME", "XDG_CACHE_HOME", "LocalAppData", "USERPROFILE"} {
		t.Run(name, func(t *testing.T) {
			rec := expectFatal(t, func(tb testing.TB) { Env(tb, Inherit(name)) })
			report := rec.first(t, "Env(Inherit("+strconv.Quote(name)+"))")
			for _, needle := range []string{"Inherit", "KeepHome", name} {
				if !strings.Contains(report, needle) {
					t.Errorf("the refusal does not mention %q:\n%s", needle, report)
				}
			}
		})
	}
}

// TestWithFoldsTheNameOnlyWhereTheEnvironmentDoes is a POSIX correctness rule
// that reads like a Windows one.
//
// The policy sets both HOME and `home` — the first is what every POSIX platform
// reads and the second is what Plan 9 reads — and on Windows they are one
// variable, because its environment is case-insensitive. So an override applied
// by folding case everywhere would replace `home` when a caller asked for HOME,
// which on Linux and macOS is a different variable being silently overwritten,
// and would leave the composed environment one row short.
func TestWithFoldsTheNameOnlyWhereTheEnvironmentDoes(t *testing.T) {
	t.Parallel()

	base := []string{"HOME=/upper", "home=/lower", "PATH=/bin"}
	got := withEntries(base, "HOME=/replaced")

	if runtime.GOOS == "windows" {
		if len(got) != len(base) {
			t.Errorf("withEntries = %q, want the same %d entries with HOME replaced", got, len(base))
		}
		return
	}
	if want := []string{"HOME=/replaced", "home=/lower", "PATH=/bin"}; !slices.Equal(got, want) {
		t.Errorf("withEntries = %q, want %q", got, want)
	}
}

// AllowedThirdPartyImports is every import outside the standard library the
// harness may reach for, and the list is deliberately one entry long.
//
// github.com/google/go-cmp is on it because [Golden] prints a diff, and a diff
// of two multi-line documents written by hand is either wrong or is go-cmp
// again. It is already a direct dependency of this module, it pulls nothing in
// behind it, and it is test-only in practice everywhere it is used.
//
// A second entry is a decision rather than an import: the harness is linked into
// the test binary of every pure package in this repository, so its dependencies
// are dependencies those packages' tests compile and run.
var AllowedThirdPartyImports = []string{"github.com/google/go-cmp"}

// TestTheHarnessImportsNothingFromThisModule is the second half of the layering
// rule, and it is the half no other test can see.
//
// The first half — that production code does not import the harness — is
// enforced by parsing the tree. This one is about the harness's own import list,
// and it makes two claims.
//
// Nothing from this module. If internal/testkit imported an engine package, the
// harness would become part of the graph it exists to observe: a change to
// internal/mutation would rebuild it, and the tests of that change would be
// written with helpers compiled from the code under test. It is also what keeps
// a pure package's unit tests free of the engine — internal/mutation's tests can
// use this package without linking internal/snapshot behind it.
//
// And nothing outside the standard library except what
// [AllowedThirdPartyImports] names. The harness is linked into the test binary
// of every pure package here, so a dependency it takes is a dependency all of
// them compile; the list is short so that adding to it is a decision somebody
// makes rather than an import somebody writes.
//
// internal/testkit/mutantkit is deliberately outside the scan: it exists to hold
// the helpers that do need engine types, and it is imported only from external
// test packages.
func TestTheHarnessImportsNothingFromThisModule(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(Root(t), "internal", "testkit")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing the imports of %s: %v", path, err)
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("reading the import path %s in %s: %v", spec.Path.Value, path, err)
			}
			switch {
			case strings.HasPrefix(imported, ModulePath+"/"):
				t.Errorf("%s imports %s: the harness may not import the module it tests",
					entry.Name(), imported)
			case !standardLibrary(imported) && !allowedThirdParty(imported):
				t.Errorf("%s imports %s, which is neither the standard library nor one of %v. "+
					"Every test binary in this repository that links the harness would compile it: "+
					"add it to AllowedThirdPartyImports on purpose, or do without",
					entry.Name(), imported, AllowedThirdPartyImports)
			}
		}
	}
}

// standardLibrary reports whether an import path names a standard library
// package.
//
// The rule is the go command's own: a path whose first element has no dot in it
// is in the standard library, because a module path's first element is a
// hostname. It needs no toolchain and no package list, which is what the unit
// tier requires of it.
func standardLibrary(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

// allowedThirdParty reports whether a non-standard import is on the list.
//
// Prefixes are matched rather than exact paths, because a module publishes
// packages: go-cmp is `github.com/google/go-cmp/cmp` today and could grow a
// second package tomorrow, and the decision was about the module.
func allowedThirdParty(path string) bool {
	for _, allowed := range AllowedThirdPartyImports {
		if path == allowed || strings.HasPrefix(path, allowed+"/") {
			return true
		}
	}
	return false
}

// lookupEnv reads one variable out of a composed environment, the way a child
// process would.
func lookupEnv(env []string, name string) (string, bool) {
	value, found := "", false
	for _, entry := range env {
		key, v, ok := strings.Cut(entry, "=")
		if ok && sameEnvName(key, name) {
			// Last wins, which is what os/exec's own deduplication does.
			value, found = v, true
		}
	}
	return value, found
}

// countEnv counts the entries naming one variable.
func countEnv(env []string, name string) int {
	count := 0
	for _, entry := range env {
		if key, _, ok := strings.Cut(entry, "="); ok && sameEnvName(key, name) {
			count++
		}
	}
	return count
}

// difference returns the entries of a that are not in b.
func difference(a, b []string) []string {
	var only []string
	for _, entry := range a {
		if !slices.Contains(b, entry) {
			only = append(only, entry)
		}
	}
	return only
}
