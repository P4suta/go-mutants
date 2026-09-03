// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/tempowner"
)

// TestSweepTemporaryRemovesTheRunsOwnOrphans pins what a run collects before it
// writes anything of its own: the snapshot and scratch directories of a run
// that was killed, and nothing else in a directory it shares with the rest of
// the machine.
func TestSweepTemporaryRemovesTheRunsOwnOrphans(t *testing.T) {
	parent := t.TempDir()

	dead := abandonedDirectory(t, parent, snapshot.DirPrefix+"dead")
	deadScratch := abandonedDirectory(t, parent, scratchPrefix+"dead")

	live := abandonedDirectory(t, parent, snapshot.DirPrefix+"live")
	lock, held, err := tempowner.Acquire(tempowner.LockPath(live))
	if err != nil || !held {
		t.Fatalf("holding a live directory's lock (held=%v, err=%v)", held, err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	unrelated := filepath.Join(parent, "someone-elses-work")
	if err = os.Mkdir(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}

	s := &session{}
	s.sweepTemporary(parent)

	for _, gone := range []string{dead, deadScratch} {
		if _, statErr := os.Stat(gone); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("%s survived the run's sweep (%v)", gone, statErr)
		}
	}
	for _, survivor := range []string{live, unrelated} {
		if _, statErr := os.Stat(survivor); statErr != nil {
			t.Errorf("the run's sweep removed %s: %v", survivor, statErr)
		}
	}
	if len(s.warnings) != 0 {
		t.Errorf("a clean sweep published %v, want no warning", s.warnings)
	}
}

// TestSweepTemporaryWarnsWhenAnOrphanSurvives keeps a directory that will not
// go away from ending a run: the results are unaffected and the remedy is a
// deletion in the temporary area, which is exactly what the two neighbouring
// cleanup warnings already say. What makes the orphan refuse is the platform's
// business — obstructRemoval is defined per platform — because a parent that
// is not a directory, the obvious obstacle, is reported as absent on Windows
// and an absent parent is correctly nothing to sweep.
func TestSweepTemporaryWarnsWhenAnOrphanSurvives(t *testing.T) {
	parent := t.TempDir()
	orphan := abandonedDirectory(t, parent, snapshot.DirPrefix+"stuck")
	obstructRemoval(t, parent, orphan)

	s := &session{}
	s.sweepTemporary(parent)

	if len(s.warnings) != 1 || s.warnings[0].Code != string(CodeOrphanNotRemoved) {
		t.Fatalf("a failed sweep published %v, want one %s warning", s.warnings, CodeOrphanNotRemoved)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("the orphan the sweep warned about is gone after all: %v", err)
	}
}

// TestTempPrefixesCoverEveryDirectoryTheRunCreates is the coupling that keeps
// the sweep honest: a future directory created under a fourth prefix would be
// swept by nobody, and the run that leaks it is the run that would have to
// collect it.
func TestTempPrefixesCoverEveryDirectoryTheRunCreates(t *testing.T) {
	for _, prefix := range []string{snapshot.DirPrefix, scratchPrefix} {
		if !slices.Contains(tempPrefixes, prefix) {
			t.Errorf("tempPrefixes does not cover %q", prefix)
		}
	}
}

func abandonedDirectory(t *testing.T, parent, name string) string {
	t.Helper()
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	owner, err := tempowner.Claim(path, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("claiming %s: %v", path, err)
	}
	if err = owner.Release(); err != nil {
		t.Fatalf("releasing %s: %v", path, err)
	}
	return path
}
