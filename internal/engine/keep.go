// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package engine

import (
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/P4suta/go-mutants/internal/snapshot"
	"github.com/P4suta/go-mutants/internal/tempowner"
	"github.com/P4suta/go-mutants/trace"
)

// A KeepTemp says whether a run leaves its temporary directories behind instead
// of removing them.
//
// It is the escape hatch for the one question a removed directory cannot
// answer: what the tree a mutant actually ran in looked like. A failed baseline,
// a drift gate that fired, an instrumented tree that will not compile — each of
// those is diagnosed by reading the snapshot, and by the time the user knows
// they want to it is gone.
//
// It is opt-in, and that is not timidity. A kept snapshot is a full copy of the
// module, nothing will ever remove it, and a run that kept one unconditionally
// filled a developer's disk twice before this option had a name. The price is
// charged only when somebody asks for it.
//
// It takes no part in a mutant identity, in a verdict, or in the key a cached
// outcome is stored under. See docs/adr/0001-trace-is-not-evidence.md: what
// makes a diagnostic option a diagnostic option is precisely that it changes
// nothing about the run.
type KeepTemp int

// The keep modes.
const (
	// KeepTempNever removes the snapshot and the scratch directory on every
	// path out of a run. It is the zero value, so every caller that never heard
	// of the option keeps nothing.
	KeepTempNever KeepTemp = iota
	// KeepTempAlways keeps them whatever became of the run. It is what somebody
	// standing at a terminal types once, to look at a tree they are about to
	// have questions about.
	KeepTempAlways
	// KeepTempOnFailure keeps them only when the run failed, which is the mode a
	// CI job can afford to leave on: the disk pays for the runs somebody has to
	// diagnose and for no others. An interrupted run is not a failure and keeps
	// nothing; see [keepsTemporaries].
	KeepTempOnFailure
)

// String returns the mode as it is written on the command line, so that the
// flag's spelling and the value's spelling cannot drift apart.
func (k KeepTemp) String() string {
	switch k {
	case KeepTempAlways:
		return "always"
	case KeepTempOnFailure:
		return "on-failure"
	case KeepTempNever:
		return "never"
	}
	return "never"
}

// The kinds of directory a run can preserve. They are the words a console
// prints and the words [PreservedDir.Kind] carries, so they are short and
// fixed.
const (
	// KeptSnapshot is the disposable copy of the workspace.
	KeptSnapshot = "snapshot"
	// KeptScratch is the per-run directory beside it: the compiled test
	// binaries, the per-worker temporary directories, and the coverage data.
	KeptScratch = "scratch"
)

// A PreservedDir is one temporary directory a run left behind on purpose.
type PreservedDir struct {
	// Kind is [KeptSnapshot] or [KeptScratch].
	Kind string
	// Path is the absolute path of the directory, which is outside the
	// workspace because that is where a temporary directory is made.
	Path string
}

// comparePreserved orders preserved directories by kind and then by path.
//
// The order is fixed rather than "whatever the cleanup happened to run in", for
// the reason every other list this package publishes is: two runs of one
// workspace produce two lists that line up row for row, and a caller diffing
// them is looking at what moved rather than at what was appended.
func comparePreserved(a, b PreservedDir) int {
	if kind := strings.Compare(a.Kind, b.Kind); kind != 0 {
		return kind
	}
	return strings.Compare(a.Path, b.Path)
}

// temporaries are the two directories one run makes for itself, held together
// so that [session.release] can settle both from one deferred call.
//
// The fields are filled in as each directory comes into existence, and a zero
// field is a directory that was never made — which is what a run that failed
// before it copied anything leaves behind, and what makes releasing safe to
// defer before either exists.
type temporaries struct {
	// snapshot is the disposable copy of the workspace, or nil.
	snapshot *snapshot.Snapshot
	// scratch is the per-run directory beside it, or empty.
	scratch string
	// scratchOwner holds scratch's lock and marker. It is nil exactly when
	// scratch is empty, because a directory is only recorded here once it has
	// been claimed.
	scratchOwner *tempowner.Owner
}

// keepsTemporaries decides whether one run's directories survive it.
//
// The interruption case is the one worth stating. A Ctrl-C is not a failure:
// nothing went wrong, the user asked for the run to stop, and an `on-failure`
// that filled the disk every time somebody changed their mind is an option
// nobody could leave switched on. `always` is deliberately not qualified that
// way — it is the word the user typed, and a mode that quietly meant "always,
// unless you interrupt" would be a mode that fails at the one moment somebody
// hit Ctrl-C *because* they had seen enough and wanted the tree.
func keepsTemporaries(keep KeepTemp, err error) bool {
	switch keep {
	case KeepTempAlways:
		return true
	case KeepTempOnFailure:
		return err != nil && !interrupted(err)
	case KeepTempNever:
		return false
	}
	return false
}

// release settles the run's temporary directories on every path out of the
// pipeline, and is the only place either of them is removed or kept.
//
// It replaced two deferred cleanups, and the merge is what makes the option
// expressible at all: keeping is a decision about *the run*, and two independent
// defers each knew about one directory and neither knew whether the run had
// failed. The order is the order those two defers ran in — the scratch
// directory, then the snapshot — so a run that keeps nothing publishes exactly
// the warnings, in exactly the order, that it published before this existed.
//
// Nothing here can fail a run. A directory that would not go away is a
// diagnostic to report, and a keep that could not be recorded costs the answer
// rather than the measurement: see [session.settle].
func (s *session) release(temps *temporaries, keep KeepTemp, out *RunOutcome, err error) {
	keeping := keepsTemporaries(keep, err)
	var preserved []PreservedDir

	if scratch, owner := temps.scratch, temps.scratchOwner; scratch != "" {
		// The lock is dropped before the removal: on Windows an open handle
		// inside a directory is exactly what makes RemoveAll fail.
		remove := func() error { return errors.Join(owner.Release(), os.RemoveAll(scratch)) }
		if s.settle(keeping, "per-run temporary directory", CodeScratchNotRemoved, owner.Keep, remove) {
			preserved = append(preserved, PreservedDir{Kind: KeptScratch, Path: scratch})
		}
	}
	if snap := temps.snapshot; snap != nil {
		if s.settle(keeping, "snapshot directory", CodeSnapshotNotRemoved, snap.Keep, snap.Cleanup) {
			preserved = append(preserved, PreservedDir{Kind: KeptSnapshot, Path: snap.Dir()})
		}
	}

	slices.SortFunc(preserved, comparePreserved)
	for _, directory := range preserved {
		// An artifact rather than a note, because nothing went wrong: a kept
		// directory is a path the run produced, like the documents it filed.
		s.trace.Artifact(artifactKindOf(directory.Kind), directory.Path)
		// A conversion rather than a copy field by field: the event and the
		// outcome row say the same two things, and the compiler is the right
		// place for "and they always will".
		s.emit(DirectoryKept(directory))
	}
	out.Preserved = preserved
}

// settle keeps or removes one temporary directory, and reports whether it was
// kept.
//
// A keep the marker did not record is not a keep. The next run's sweep reads
// the marker and finds a lock nobody holds, which is exactly what an abandoned
// directory looks like, so a directory that could not be marked would be
// collected minutes later and the answer somebody asked for would be gone. It
// is removed now instead, with the reason said out loud, rather than left to
// look preserved until it is not.
//
// The root package's Workspace.Close reaches the same conclusion in its own
// keepOrRemove, and the twin is deliberate rather than overlooked. This package
// cannot import the root package — the dependency runs the other way — and what
// the two share is three lines of policy, not a mechanism: half of what is here
// is which code to warn under and what to say, and neither of those belongs in
// internal/tempowner, which owns no event stream and no diagnostic codes. A
// third caller is when it would earn a home of its own.
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

// artifactKindOf is the recording's own spelling of a preserved directory's
// kind. The two vocabularies are deliberately separate: the event stream is not
// a published format and the trace contract is.
func artifactKindOf(kind string) string {
	if kind == KeptSnapshot {
		return trace.ArtifactKeptSnapshot
	}
	return trace.ArtifactKeptScratch
}
