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

// TestDiscoverWithSendsTheLoadersCompilesToTheGivenCache is why the pipeline has
// an environment-taking form at all.
//
// Discovery reads types, so go/packages asks the go command for export data, so
// a discovery pass compiles the module and everything below it. That makes it
// the heaviest writer of build cache entries in every suite that drives it —
// heavier than the builds those suites are actually about — and each entry is
// keyed on a snapshot path that exists for one run. Under the process's own
// environment all of that lands in the developer's cache, which is the failure
// one dedicated cache exists to prevent, and nothing in a passing suite would
// ever have said so.
//
// The assertion is made against a cache of the test's own rather than against
// the harness's, because the harness's is shared: it is warm, other tests write
// into it while this one runs, and "something appeared in it" would be true
// whatever this pass did. An empty directory nobody else can reach makes the
// claim exactly as strong as it sounds.
func TestDiscoverWithSendsTheLoadersCompilesToTheGivenCache(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	snap := tinyModule(t)

	cache := filepath.Join(t.TempDir(), "gocache")
	// Appended rather than substituted: os/exec keeps the last assignment of a
	// name, which is the same rule [testkit.Environment.With] applies.
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

// TestDiscoverSendsTheLoadersCompilesToTheHarnessCache is the same claim for the
// form that composes its own environment, and it is the one that matters most:
// that form is what a test with nothing to share calls, and it used to leave the
// loader on the process's environment.
//
// The harness's cache is redirected into a directory of this test's own, which
// is what makes the assertion an oracle rather than an observation. The real
// harness cache is shared and warm, so "it grew" would be true however this pass
// behaved; a private one is written to by exactly this discovery or by nothing.
// That redirection is a process-wide variable, so this test may not be parallel —
// which is the whole price, paid once.
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

// tinyModule is a module of one import-free file, snapshotted and aged.
//
// A corpus fixture would do, and would cost a second per dependency the loader
// has to compile into a cache these tests deliberately start empty. What is
// being watched is whether *anything* was written, so the cheapest module that
// holds a candidate says it as well as the largest one would.
func tinyModule(t *testing.T) *snapshot.Snapshot {
	t.Helper()
	return mutantkit.SnapshotOf(t, testkit.NewModule(t).Module("cache.example/tiny").
		Source("tiny.go", "package tiny\n\n// Less is here to be a candidate.\nfunc Less(a, b int) bool { return a < b }\n").
		Root())
}
