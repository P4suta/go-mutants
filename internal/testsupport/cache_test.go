// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package testsupport_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testsupport"
)

// TestCacheDirIsTheEnvironmentsCacheRoot keeps the forwarder honest for the
// fourteen call sites that still go through it.
//
// Each of them uses the returned path as the cache *root* — the directory
// go-mutants puts its own `go-mutants` directory in — and each of them then
// asserts on what the code under test wrote below it. A forwarder that returned
// the moved HOME, or the build cache, or a path that did not exist yet would
// leave every one of those tests passing against a directory nothing writes to.
func TestCacheDirIsTheEnvironmentsCacheRoot(t *testing.T) {
	cache := testsupport.CacheDir(t)

	resolved, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("os.UserCacheDir after the redirection: %v", err)
	}
	if !testkit.SamePath(cache, resolved) {
		t.Errorf("CacheDir returned %s, but os.UserCacheDir resolves to %s", cache, resolved)
	}
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Errorf("the cache root %s is not a directory that exists: %v", cache, err)
	}

	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("CacheDir did not move HOME")
	}
	if rel, err := filepath.Rel(home, cache); err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("the cache root %s is outside the moved HOME %s", cache, home)
	}

	// The rest of the policy comes with it, which is the reason the helper is a
	// forwarder rather than its own implementation.
	if got := os.Getenv("GOFLAGS"); got != "-mod=readonly" {
		t.Errorf("GOFLAGS = %q, so CacheDir is not applying the harness policy", got)
	}
}
