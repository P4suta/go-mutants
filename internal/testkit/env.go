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

const (
	BuildCacheEnv   = "GO_MUTANTS_TEST_GOCACHE"
	RequireToolsEnv = "GO_MUTANTS_TEST_REQUIRE_TOOLS"
)

const BuildCacheMarker = ".go-mutants-testcache"

const (
	GitName  = "go-mutants tests"
	GitEmail = "tests@go-mutants.invalid"
	GitDate  = "2026-02-18T09:15:00+00:00"
)

type Environment struct {
	Scratch string
	Home    string
	Cache   string
	GoCache string

	vars []string
}

func Env(t testing.TB, opts ...EnvOption) *Environment {
	t.Helper()

	var cfg envConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	resolveGoDirectories()
	gocache, err := BuildCache()
	if err != nil {
		t.Fatalf("resolving the test build cache: %v", err)
	}

	stampBuildCache(t, gocache)

	p := policy{scratch: Scratch(t), gocache: gocache}
	if !cfg.keepHome {
		if name, ok := cfg.firstInherited(homeNames()); ok {
			t.Fatalf("Inherit(%q) asks to keep a variable this policy moves, and the test would then "+
				"read and write the developer's own home directory. A test whose subject is the "+
				"real home wants KeepHome() rather than Inherit(%q).", name, name)
		}
		p.home = Scratch(t)
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

func realHome(t testing.TB) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir with the real HOME kept: %v", err)
	}
	return home
}

func (e *Environment) Vars() []string { return slices.Clone(e.vars) }

func (e *Environment) With(kv ...string) []string {
	return withEntries(e.vars, kv...)
}

func Compose(t testing.TB, scratch string) []string {
	t.Helper()
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

func BuildCache() (string, error) {
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
	return filepath.Join(pinned.userCache, harnessDirName, buildCacheDirName), nil
}

func BuildCacheEntries(t testing.TB, dir string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case entry.IsDir():
			return nil
		}
		switch entry.Name() {
		case "README", "trim.txt", "lock", BuildCacheMarker:
			return nil
		}
		count++
		return nil
	})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return 0
	case err != nil:
		t.Fatalf("walking the build cache %s: %v", dir, err)
	}
	return count
}

func stampBuildCache(t testing.TB, dir string) {
	t.Helper()
	stamped, err := stampHarnessDirectory(dir, BuildCacheMarker, buildCacheMarkerBody)
	switch {
	case err != nil:
		t.Fatalf("creating the test build cache %s (every child `go` command is about to use it): %v", dir, err)
	case !stamped:
		t.Logf("testkit: %s already holds files that are not this harness's, so it was not stamped as "+
			"the test build cache and `mise run test-clean` will refuse to empty it. If it is a cache "+
			"from before this file existed, delete it once and the next run will make it again; "+
			"otherwise point %s at a directory of its own.", dir, BuildCacheEnv)
	}
}

func stampHarnessDirectory(dir, marker, body string) (bool, error) {
	path := filepath.Join(dir, marker)
	if _, err := os.Lstat(path); err == nil {
		return true, nil
	}

	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
			return false, mkErr
		}
	case err != nil:
		return false, nil
	case len(entries) > 0:
		return false, nil
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return true, nil
	}
	if err != nil {
		return false, nil
	}
	_, writeErr := file.WriteString(body)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr) == nil, nil
}

const buildCacheMarkerBody = "This directory is the go-mutants test harness's build cache.\n" +
	"It exists so that the test suites' `go` commands do not fill your own build cache.\n" +
	"`mise run test-cache-status` reports it; `mise run test-clean` empties it.\n" +
	"Nothing in it is precious: deleting it costs one recompile.\n" +
	"This file is also what tells the collector the directory is safe to remove,\n" +
	"which is why nothing removes a directory that does not have one.\n"

var pinnedBuildCache = os.Getenv(BuildCacheEnv)

func harnessSetting(name, atStart string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	if value, ok := strippedValue(name); ok {
		return value
	}
	return atStart
}

var stripped struct {
	mu     sync.Mutex
	values map[string]string
}

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

func strippedValue(name string) (string, bool) {
	stripped.mu.Lock()
	defer stripped.mu.Unlock()
	value, ok := stripped.values[name]
	return value, ok
}

type EnvOption func(*envConfig)

func KeepHome() EnvOption {
	return func(cfg *envConfig) { cfg.keepHome = true }
}

func Inherit(names ...string) EnvOption {
	return func(cfg *envConfig) { cfg.inherit = append(cfg.inherit, names...) }
}

type envConfig struct {
	keepHome bool
	inherit  []string
}

func (c *envConfig) inherited(name string) bool {
	return slices.ContainsFunc(c.inherit, func(named string) bool { return sameEnvName(named, name) })
}

func (c *envConfig) firstInherited(names []string) (string, bool) {
	for _, name := range names {
		if c.inherited(name) {
			return name, true
		}
	}
	return "", false
}

type policy struct {
	scratch string
	gocache string
	home    string
}

func homeNames() []string {
	return []string{"HOME", "home", "USERPROFILE", "XDG_CACHE_HOME", "LocalAppData"}
}

func homePairs(home string) [][2]string {
	cache := filepath.Join(home, "cache")
	return [][2]string{
		{"LocalAppData", cache},
		{"XDG_CACHE_HOME", cache},
		{"HOME", home},
		{"home", home},
		{"USERPROFILE", home},
	}
}

func policyPairs(p policy) [][2]string {
	scratch, gocache := p.scratch, p.gocache
	global, system := absentGitConfig(scratch)
	pairs := [][2]string{
		{"TMPDIR", scratch},
		{"TMP", scratch},
		{"TEMP", scratch},
		{"GOCACHE", gocache},
	}
	if p.home != "" {
		pairs = append(pairs, homePairs(p.home)...)
	}
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
		[2]string{"GITHUB_STEP_SUMMARY", ""},
	)
}

func absentGitConfig(parent string) (global, system string) {
	base := filepath.Join(parent, "absent-git-config")
	return filepath.Join(base, "global"), filepath.Join(base, "system")
}

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

func createPrivateHome(t testing.TB, home string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "cache"), 0o700); err != nil {
		t.Fatalf("creating the composed home %s: %v", home, err)
	}
	silenceGoTelemetry(t, home, configDirUnder(home))
}

func osConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return dir
}

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

func isActivation(name string) bool {
	return strings.HasPrefix(strings.ToUpper(name), "GO_MUTANTS_")
}

func setUnlessInherited(t testing.TB, cfg *envConfig, name, value string) {
	t.Helper()
	if cfg.inherited(name) {
		return
	}
	t.Setenv(name, value)
}

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

func withEntries(base []string, kv ...string) []string {
	out := slices.Clone(base)
	for _, entry := range kv {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
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

func withoutEntries(base []string, names ...string) []string {
	return slices.DeleteFunc(slices.Clone(base), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return slices.ContainsFunc(names, func(name string) bool { return sameEnvName(key, name) })
	})
}

func sameEnvName(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func within(base, dir string) bool {
	rel, err := filepath.Rel(base, dir)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

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

type goDirectories struct {
	env      string
	path     string
	modCache string
}

func ResolveToolchainDirectories() { resolveGoDirectories() }

var resolveGoDirectories = sync.OnceValue(func() goDirectories {
	dirs := goDirectories{
		env:      os.Getenv("GOENV"),
		path:     os.Getenv("GOPATH"),
		modCache: os.Getenv("GOMODCACHE"),
	}
	if dirs.env != "" && dirs.path != "" && dirs.modCache != "" {
		return dirs
	}

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

	if pinned.userConfig != "" {
		setIfEmpty(&dirs.env, filepath.Join(pinned.userConfig, "go", "env"))
	}
	setIfEmpty(&dirs.path, build.Default.GOPATH)
	if dirs.path != "" {
		setIfEmpty(&dirs.modCache, filepath.Join(dirs.path, "pkg", "mod"))
	}
	return dirs
})

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

func setIfEmpty(dst *string, value string) {
	if *dst == "" {
		*dst = value
	}
}
