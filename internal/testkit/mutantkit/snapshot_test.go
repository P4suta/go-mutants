// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build integration

package mutantkit_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

// The variables that switch the two helper tests below on, and the file the
// second one reports through. None begins with GO_MUTANTS_, because the
// environment policy strips that whole prefix from every child it composes.
const (
	cleanupHelperEnv    = "MUTANTKIT_CLEANUP_HELPER"
	cleanupHelperMarker = "MUTANTKIT_CLEANUP_MARKER"
)

// TestSnapshotAgesTheCopy is cmd/go's two-second index cutoff, as an assertion.
//
// The go command puts a package directory's index into the build cache only when
// every file in it is at least two seconds old. A snapshot taken a moment ago is
// therefore a different input from the same tree on a user's disk: the first `go
// list` over it indexes nothing, the second one might, and any assertion about
// cache entries, misses, or a build being reused becomes a function of how long
// the copy took. [snapshot.Create] carries the source's own modification times
// across, which is most of the answer — but os.CopyFS and every synthesized file
// stamp "now", so the constructor ages the whole tree afterwards rather than
// trusting where each file came from.
func TestSnapshotAgesTheCopy(t *testing.T) {
	snap := mutantkit.Snapshot(t, "simple")

	cutoff := time.Now().Add(-testkit.TreeAge).Add(time.Minute)
	err := filepath.WalkDir(snap.Root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			t.Errorf("%s was modified at %s, which is inside cmd/go's index cutoff (want at or before %s)",
				path, info.ModTime(), cutoff)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the snapshot at %s: %v", snap.Root, err)
	}
}

// TestSnapshotCleanupHelperProcess is not a test: it is the failing test
// [TestSnapshotCleanupRunsEvenWhenTheTestFails] re-executes. It returns silently
// when it was not asked for, so an ordinary run neither runs it nor reports it
// as skipped.
func TestSnapshotCleanupHelperProcess(t *testing.T) {
	if !testkit.HelperEnabled(cleanupHelperEnv) {
		return
	}
	snap := mutantkit.Snapshot(t, "simple")
	testkit.WriteFile(t, os.Getenv(cleanupHelperMarker), []byte(snap.Root))
	t.Fatal("failing on purpose, so that the cleanup runs after a failure")
}

// TestSnapshotCleanupRunsEvenWhenTheTestFails is why the removal is registered
// before anything else in the constructor can go wrong.
//
// A failing test is exactly the case that leaves debris: the ageing walk that
// follows [snapshot.Create] can fail the test, and a cleanup registered after it
// would never be reached. The claim is checked from outside, in a child process,
// because a test cannot observe its own cleanups running.
//
// Two things are asserted together, and it takes both. The snapshot is gone —
// which t.TempDir's own removal would also achieve — and the child said nothing
// about a cleanup that failed, which is what the wrong ordering produces: a
// snap.Cleanup that runs after the temporary directory has already been removed
// reports the directory it cannot find.
func TestSnapshotCleanupRunsEvenWhenTheTestFails(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "snapshot-root")
	env := testkit.Compose(t, t.TempDir())
	env = append(env, cleanupHelperEnv+"=1", cleanupHelperMarker+"="+marker)

	result := testkit.Exec(t, ".", env, testkit.HelperArgv("TestSnapshotCleanupHelperProcess")...)

	if result.Err != nil {
		t.Fatalf("the helper test could not be run: %v\n%s", result.Err, result.Output)
	}
	if result.ExitCode == 0 {
		t.Fatalf("the helper test passed, so it never reached the failure it exists to produce:\n%s", result.Output)
	}
	if !strings.Contains(string(result.Output), "failing on purpose") {
		t.Fatalf("the helper test failed for some other reason:\n%s", result.Output)
	}
	if strings.Contains(string(result.Output), "cleaning up the snapshot at") {
		t.Errorf("the snapshot's own cleanup reported a failure, so it ran too late:\n%s", result.Output)
	}

	root := string(testkit.ReadFile(t, marker))
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the snapshot at %s outlived the failing test that made it (stat: %v)", root, err)
	}
}

// TestInstrumentProducesATreeThatStillBuilds is the pipeline helper end to end,
// and it is the assertion the four suites that wrote this sequence out by hand
// each made in their own words: an instrumented tree holds every mutant at once
// and still compiles, because that is what makes one build serve them all.
func TestInstrumentProducesATreeThatStillBuilds(t *testing.T) {
	toolchain := mutantkit.Toolchain(t)
	snap := mutantkit.Snapshot(t, "simple")
	catalog := mutantkit.Instrument(t, toolchain, snap)

	if catalog.Len() == 0 {
		t.Fatal("the fixture catalogued no mutants, so nothing was instrumented")
	}
	env := mutantkit.Activate(testkit.Compose(t, t.TempDir()), "")
	mutantkit.RequireExit(t, mutantkit.RunGo(t, toolchain, snap.Root, env, "build", "./..."), 0,
		"building the instrumented tree")

	mutant := catalog.Mutants()[0]
	suite := mutantkit.RunSuite(t, toolchain, snap.Root, mutantkit.Activate(env, mutant.ID))
	if suite.Err != nil {
		t.Fatalf("running the fixture's suite under a mutant: %v\n%s", suite.Err, suite.Output)
	}
	if lines := mutantkit.Describe(catalog, []string{mutant.ID}); !strings.Contains(lines[0], mutant.Rule.Name) {
		t.Errorf("Describe rendered %q, which does not name the rule", lines[0])
	}
}
