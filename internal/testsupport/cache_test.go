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

	if got := os.Getenv("GOFLAGS"); got != "-mod=readonly" {
		t.Errorf("GOFLAGS = %q, so CacheDir is not applying the harness policy", got)
	}
}
