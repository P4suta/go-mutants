// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testkit

import (
	"go/parser"
	"go/token"
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
		rec := &recorder{TB: t}
		Compose(rec, scratch)
		if len(rec.fatals) == 0 {
			t.Errorf("Compose(%q) reported nothing, want a refusal", scratch)
			continue
		}
		if !strings.Contains(rec.fatals[0], "scratch") {
			t.Errorf("the refusal of %q does not say what was wrong:\n%s", scratch, rec.fatals[0])
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
			rec := &recorder{TB: t}
			Env(rec, Inherit(name))
			if len(rec.fatals) == 0 {
				t.Fatalf("Env(Inherit(%q)) reported nothing, want a refusal", name)
			}
			for _, needle := range []string{"Inherit", "KeepHome", name} {
				if !strings.Contains(rec.fatals[0], needle) {
					t.Errorf("the refusal does not mention %q:\n%s", needle, rec.fatals[0])
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

// TestTheHarnessImportsNothingFromThisModule is the second half of the layering
// rule, and it is the half no other test can see.
//
// The first half — that production code does not import the harness — is
// enforced by parsing the tree. This one is about the harness's own import list:
// if internal/testkit imported an engine package, the harness would become part
// of the graph it exists to observe. A change to internal/mutation would rebuild
// it, and the tests of that change would be written with helpers compiled from
// the code under test. It is also what keeps a pure package's unit tests free of
// the engine: internal/mutation's tests can use this package without linking
// internal/snapshot behind it.
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
			if strings.HasPrefix(imported, ModulePath+"/") {
				t.Errorf("%s imports %s: the harness may not import the module it tests",
					entry.Name(), imported)
			}
		}
	}
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
