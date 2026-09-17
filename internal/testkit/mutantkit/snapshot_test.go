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

const (
	cleanupHelperEnv    = "MUTANTKIT_CLEANUP_HELPER"
	cleanupHelperMarker = "MUTANTKIT_CLEANUP_MARKER"
)

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

func TestSnapshotCleanupHelperProcess(t *testing.T) {
	if !testkit.HelperEnabled(cleanupHelperEnv) {
		return
	}
	snap := mutantkit.Snapshot(t, "simple")
	testkit.WriteFile(t, os.Getenv(cleanupHelperMarker), []byte(snap.Root))
	t.Fatal("failing on purpose, so that the cleanup runs after a failure")
}

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
