// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package testsupport holds the test helpers that more than one package needs.
//
// It is imported only from _test files, and it exists for the one kind of
// helper a package cannot keep to itself: the ones whose correctness depends on
// a rule of the operating system rather than on anything this project decides.
// A copy of such a helper per package is a copy of the rule per package, and
// the rule is what drifts — see [CacheDir], which is here because three
// packages each had their own version of it and all three were wrong on macOS.
package testsupport

import (
	"go/build"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// CacheDir redirects [os.UserCacheDir] into a directory of the test's own and
// returns the directory it now resolves to.
//
// The returned path is the cache *root* — the directory go-mutants puts its
// `go-mutants` directory in, not that directory itself — because that is what
// os.UserCacheDir returns and what the code under test joins onto. It exists by
// the time this returns, which is the shape every platform's real cache root
// has, so a test cannot pass on one platform because the parent happened to be
// missing on it.
//
// Every variable os.UserCacheDir reads on any GOOS is set, which is why there
// is no GOOS switch here and no list of platforms to keep in step with Go's:
//
//	windows          %LocalAppData%
//	darwin, ios      $HOME/Library/Caches
//	plan9            $home/lib/cache
//	everything else  $XDG_CACHE_HOME, or $HOME/.cache when it is unset
//
// Setting only some of them is the defect this helper replaces. Three packages
// redirected XDG_CACHE_HOME and LocalAppData, which covers Linux and Windows
// and covers nothing at all on macOS: os.UserCacheDir ignores XDG there, so the
// tests wrote their fixtures into a temporary directory, read the runner's own
// `~/Library/Caches/go-mutants` back, and interfered with each other through
// it. Two green platforms is exactly as much evidence as one when the third
// reads a different variable, so the guard below does not trust the list above:
// it asks os.UserCacheDir where it actually landed and fails the test loudly if
// the answer is outside the test's own directory. That check is what would have
// caught the macOS gap on the first run anywhere.
//
// Callers must use the returned path rather than deriving one from a base of
// their own. Deriving it is the same mistake in a new place: only this function
// knows what the redirection resolved to on the platform the test is running
// on.
func CacheDir(t *testing.T) string {
	t.Helper()

	// Before HOME moves, and for the reason [pinGoDirectories] documents.
	pinGoDirectories(t)

	base := t.TempDir()
	cache := filepath.Join(base, "cache")
	t.Setenv("LocalAppData", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", base)
	// Plan 9 spells it in lower case. On Windows the environment is
	// case-insensitive, so this is the assignment above written twice with the
	// same value, which is harmless; on POSIX it is a variable nothing else
	// reads.
	t.Setenv("home", base)

	dir, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("os.UserCacheDir after redirecting it into %s: %v", base, err)
	}
	if !within(base, dir) {
		t.Fatalf("os.UserCacheDir resolves to %s, which is outside this test's own %s: "+
			"%s reads a variable this helper does not set, and the test would be reading and "+
			"writing the machine's real cache directory", dir, base, runtime.GOOS)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating the redirected cache root %s: %v", dir, err)
	}
	return dir
}

// pinGoDirectories keeps the go command's own directories where they were.
//
// os.UserCacheDir is not the only thing derived from HOME: the go command's
// build cache, module cache, GOPATH and `go env -w` file are too, on the same
// platforms and from the same variable. Moving HOME without this would hand
// every `go build` and `go test -c` a test drives an empty module cache —
// dependencies re-downloaded, or unresolvable with no network — and a cold
// build cache, which is the standard library recompiled once per test rather
// than a cache hit.
//
// None of these is part of what any test using [CacheDir] measures.
// internal/cache's key deliberately excludes GOMODCACHE and GOCACHE, on the
// grounds that they change how fast a build is and not what it produces, and
// that is the same reason it is safe to hold them still here.
//
// Each is set only when the environment does not already carry it, and to the
// value the go command would have derived for itself, so a machine that
// exports one keeps its own. [build.Default] is resolved when the test binary
// starts, which is before any of this redirection, so it still holds the real
// GOPATH. A `go env -w` setting is not consulted — go/build does not read that
// file either — and the worst that costs is the cold cache this is avoiding,
// which is what every one of these tests had before.
func pinGoDirectories(t *testing.T) {
	t.Helper()
	if cache, err := os.UserCacheDir(); err == nil {
		setIfUnset(t, "GOCACHE", filepath.Join(cache, "go-build"))
	}
	if config, err := os.UserConfigDir(); err == nil {
		setIfUnset(t, "GOENV", filepath.Join(config, "go", "env"))
	}
	if gopath := build.Default.GOPATH; gopath != "" {
		setIfUnset(t, "GOPATH", gopath)
		setIfUnset(t, "GOMODCACHE", filepath.Join(gopath, "pkg", "mod"))
	}
}

// setIfUnset gives a variable a value for the duration of the test, unless the
// environment already has one.
func setIfUnset(t *testing.T, name, value string) {
	t.Helper()
	if os.Getenv(name) == "" {
		t.Setenv(name, value)
	}
}

// within reports whether dir is base or something under it.
//
// The comparison is lexical on purpose. Both paths are built from the same
// t.TempDir, so there is no symlink between them to resolve — and resolving
// them would be the wrong question anyway, since what is being checked is that
// the path handed to the code under test was the redirected one.
func within(base, dir string) bool {
	rel, err := filepath.Rel(base, dir)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
