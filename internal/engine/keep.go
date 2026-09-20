// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/tempowner"
	"github.com/P4suta/go-mutants/trace"
)

type KeepTemp int

const (
	KeepTempNever KeepTemp = iota
	KeepTempAlways
	KeepTempOnFailure
)

func (k KeepTemp) String() string {
	switch k {
	case KeepTempAlways:
		return "always"
	case KeepTempOnFailure:
		return "on-failure"
	case KeepTempNever:
	}
	return "never"
}

const (
	KeptSnapshot = "snapshot"
	KeptScratch  = "scratch"
)

type PreservedDir struct {
	Kind string
	Path string
}

func comparePreserved(a, b PreservedDir) int {
	if kind := strings.Compare(a.Kind, b.Kind); kind != 0 {
		return kind
	}
	return strings.Compare(a.Path, b.Path)
}

type temporaries struct {
	snapshot     *snapshot.Snapshot
	scratch      string
	scratchOwner *tempowner.Owner
	probe        *snapshot.Snapshot
	workers      []*snapshot.Snapshot
}

func keepsTemporaries(keep KeepTemp, err error) bool {
	switch keep {
	case KeepTempAlways:
		return true
	case KeepTempOnFailure:
		return err != nil && !interrupted(err)
	case KeepTempNever:
	}
	return false
}

func forceRemoveAll(root string) error {
	err := os.RemoveAll(root)
	if err == nil {
		return nil
	}
	widen(root)
	return os.RemoveAll(root)
}

func widen(dir string) {
	_ = os.Chmod(dir, 0o700)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			widen(filepath.Join(dir, entry.Name()))
		}
	}
}

func (s *session) release(temps *temporaries, keep KeepTemp, out *RunOutcome, err error) {
	keeping := keepsTemporaries(keep, err)
	var preserved []PreservedDir

	if scratch, owner := temps.scratch, temps.scratchOwner; scratch != "" {
		remove := func() error { return errors.Join(owner.Release(), forceRemoveAll(scratch)) }
		if s.settle(keeping, "per-run temporary directory", CodeScratchNotRemoved, owner.Keep, remove) {
			preserved = append(preserved, PreservedDir{Kind: KeptScratch, Path: scratch})
		}
	}
	for _, worker := range temps.workers {
		if worker == nil {
			continue
		}
		if s.settle(keeping, "worker snapshot directory", CodeSnapshotNotRemoved, worker.Keep, worker.Cleanup) {
			preserved = append(preserved, PreservedDir{Kind: KeptSnapshot, Path: worker.Dir()})
		}
	}
	if tree := temps.probe; tree != nil {
		if s.settle(keeping, "probe snapshot directory", CodeSnapshotNotRemoved, tree.Keep, tree.Cleanup) {
			preserved = append(preserved, PreservedDir{Kind: KeptSnapshot, Path: tree.Dir()})
		}
	}
	if snap := temps.snapshot; snap != nil {
		if s.settle(keeping, "snapshot directory", CodeSnapshotNotRemoved, snap.Keep, snap.Cleanup) {
			preserved = append(preserved, PreservedDir{Kind: KeptSnapshot, Path: snap.Dir()})
		}
	}

	slices.SortFunc(preserved, comparePreserved)
	for _, directory := range preserved {
		s.trace.Artifact(artifactKindOf(directory.Kind), directory.Path)
		s.emit(DirectoryKept(directory))
	}
	out.Preserved = preserved
}

func (s *session) settle(keep bool, what string, notRemoved Code, record, remove func() error) bool {
	if keep {
		err := record()
		if err == nil {
			return true
		}
		s.warn(CodeTemporaryNotKept, "the "+what+
			" could not be marked kept, so it was removed rather than left for the next run's sweep: "+err.Error())
	}
	if err := remove(); err != nil {
		s.warn(notRemoved, "the "+what+" could not be removed: "+err.Error())
	}
	return false
}

func artifactKindOf(kind string) string {
	if kind == KeptSnapshot {
		return trace.ArtifactKeptSnapshot
	}
	return trace.ArtifactKeptScratch
}
