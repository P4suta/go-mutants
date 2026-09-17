// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package mutantkit_test

import (
	"path/filepath"
	"testing"

	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestDiscoverWithSendsTheLoadersCompilesToTheGivenCache(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	snap := tinyModule(t)

	cache := filepath.Join(t.TempDir(), "gocache")
	env := append(testkit.Compose(t, t.TempDir()), "GOCACHE="+cache)

	found := mutantkit.DiscoverWith(t, toolchain, snap, env)
	if len(found.Candidates) == 0 {
		t.Fatal("discovery found no candidates in the module, so nothing was compiled and this test " +
			"would pass whatever the loader did with its environment")
	}
	if got := testkit.BuildCacheEntries(t, cache); got == 0 {
		t.Errorf("the package loader wrote no entries into %s, so it compiled the module somewhere "+
			"else — which under a suite's environment is the developer's own build cache", cache)
	}
}

func TestDiscoverSendsTheLoadersCompilesToTheHarnessCache(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "gocache")
	t.Setenv(testkit.BuildCacheEnv, cache)
	if got, err := testkit.BuildCache(); err != nil || !testkit.SamePath(got, cache) {
		t.Fatalf("the harness resolved its build cache to %q (%v), want the private %s", got, err, cache)
	}

	toolchain := mutantkit.Toolchain(t)
	snap := tinyModule(t)

	found := mutantkit.Discover(t, toolchain, snap)
	if len(found.Candidates) == 0 {
		t.Fatal("discovery found no candidates in the module, so nothing was compiled and this test " +
			"would pass whatever the loader did with its environment")
	}
	if got := testkit.BuildCacheEntries(t, cache); got == 0 {
		t.Errorf("the package loader wrote no entries into the harness's build cache %s, so it "+
			"compiled the module in the developer's own", cache)
	}
}

func tinyModule(t *testing.T) *snapshot.Snapshot {
	t.Helper()
	return mutantkit.SnapshotOf(t, testkit.NewModule(t).Module("cache.example/tiny").
		Source("tiny.go", "package tiny\n\n// Less is here to be a candidate.\nfunc Less(a, b int) bool { return a < b }\n").
		Root())
}
