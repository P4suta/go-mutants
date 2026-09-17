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

func TestPathAgreesWithTestkit(t *testing.T) {
	t.Run("the default directory", func(t *testing.T) {
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

}

func TestKeptRootAgreesWithTestkit(t *testing.T) {
	requireDefaultKeptRootUntouched(t)

	t.Run("the default root", func(t *testing.T) {
		t.Setenv(KeepEnv, "")
		t.Setenv(KeepDirEnv, "")

		e := Env(t, KeepHome())
		want, err := KeepRoot()
		if err != nil {
			t.Fatalf("KeepRoot: %v", err)
		}
		if got := testcacheSays(t, e.Vars(), "path", "--kept"); !SamePath(got, want) {
			t.Errorf("`testcache path --kept` printed %s, but testkit resolved %s: the two copies "+
				"of the rule have drifted, and the suites are filing evidence under a root nothing empties",
				got, want)
		}
	})

	t.Run("the named root", func(t *testing.T) {
		t.Setenv(KeepEnv, "")
		named := filepath.Join(t.TempDir(), "named-kept")
		t.Setenv(KeepDirEnv, named)

		e := Env(t, KeepHome())
		want, err := KeepRoot()
		if err != nil {
			t.Fatalf("KeepRoot with %s set: %v", KeepDirEnv, err)
		}
		if want != named {
			t.Fatalf("KeepRoot resolved %s rather than the named %s", want, named)
		}
		if got := testcacheSays(t, e.With(KeepDirEnv+"="+named), "path", "--kept"); !SamePath(got, want) {
			t.Errorf("`testcache path --kept` printed %s, want the named %s", got, want)
		}
	})

	t.Run("the marker that licenses emptying it", func(t *testing.T) {
		t.Setenv(KeepEnv, "")
		e := Env(t, KeepHome())
		if got := testcacheSays(t, e.Vars(), "path", "--kept", "--marker"); got != KeptMarker {
			t.Errorf("`testcache path --kept --marker` printed %s, but testkit stamps %s: the "+
				"collector would look for a file the harness never writes, and every kept directory "+
				"would be refused as somebody else's", got, KeptMarker)
		}
	})
}

func TestAnExplicitlyEmptyBuildCacheOverrideMeansTheDefault(t *testing.T) {
	if pinned.userCache == "" {
		t.Skip("this platform has no user cache directory, so there is no default to fall back to")
	}
	want := filepath.Join(pinned.userCache, "go-mutants-test", "go-build")

	t.Setenv(BuildCacheEnv, filepath.Join(t.TempDir(), "named-by-the-job"))
	t.Setenv(BuildCacheEnv, "")

	got, err := BuildCache()
	if err != nil {
		t.Fatalf("BuildCache with an empty %s: %v", BuildCacheEnv, err)
	}
	if got != want {
		t.Errorf("BuildCache with an empty %s = %s, want the default %s", BuildCacheEnv, got, want)
	}

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

func TestMarkerNamesAgreeWithTestcache(t *testing.T) {
	e := Env(t, KeepHome())

	if got := testcacheSays(t, e.Vars(), "path", "--marker"); got != BuildCacheMarker {
		t.Errorf("`testcache path --marker` printed %q, but this package writes %q", got, BuildCacheMarker)
	}
	kept := testcacheSays(t, e.Vars(), "path", "--kept", "--marker")
	if kept == BuildCacheMarker || kept == "" {
		t.Errorf("the kept scratch marker is %q, want a name of its own", kept)
	}
}

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

	Env(t)
	_ = Compose(t, t.TempDir())
	if got := ReadFile(t, marker); string(got) != string(first) {
		t.Errorf("a second resolution rewrote the marker:\n%s", got)
	}
}

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

func testcacheSays(t testing.TB, env []string, args ...string) string {
	t.Helper()
	gobin := GoBinary(t)
	argv := append([]string{gobin, "run", "./internal/devtools/testcache"}, args...)
	result := Exec(t, Root(t), env, argv...)
	RequireExit(t, result, 0, "`go run ./internal/devtools/testcache "+strings.Join(args, " ")+"`")
	return strings.TrimSpace(string(result.Stdout))
}

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

	e := Env(t)
	for name, value := range want {
		switch name {
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

func TestComposeRefusesAScratchItCannotUse(t *testing.T) {
	t.Parallel()

	for _, scratch := range []string{"", filepath.Join("relative", "scratch"), filepath.Join(t.TempDir(), "does-not-exist")} {
		rec := expectFatal(t, func(tb testing.TB) { Compose(tb, scratch) })
		if report := rec.first(t, "Compose("+strconv.Quote(scratch)+")"); !strings.Contains(report, "scratch") {
			t.Errorf("the refusal of %q does not say what was wrong:\n%s", scratch, report)
		}
	}
}

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

func TestEnvKeepHomeLeavesTheRealHomeAlone(t *testing.T) {
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

var AllowedThirdPartyImports = []string{"github.com/google/go-cmp"}

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

func standardLibrary(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

func allowedThirdParty(path string) bool {
	for _, allowed := range AllowedThirdPartyImports {
		if path == allowed || strings.HasPrefix(path, allowed+"/") {
			return true
		}
	}
	return false
}

func lookupEnv(env []string, name string) (string, bool) {
	value, found := "", false
	for _, entry := range env {
		key, v, ok := strings.Cut(entry, "=")
		if ok && sameEnvName(key, name) {
			value, found = v, true
		}
	}
	return value, found
}

func countEnv(env []string, name string) int {
	count := 0
	for _, entry := range env {
		if key, _, ok := strings.Cut(entry, "="); ok && sameEnvName(key, name) {
			count++
		}
	}
	return count
}

func difference(a, b []string) []string {
	var only []string
	for _, entry := range a {
		if !slices.Contains(b, entry) {
			only = append(only, entry)
		}
	}
	return only
}
