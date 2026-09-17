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
