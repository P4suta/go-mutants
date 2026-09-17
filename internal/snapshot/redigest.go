// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// A DriftKind says how a path in the snapshot stopped agreeing with the
// manifest.
type DriftKind uint8

// The three ways a snapshot can drift. A path has exactly one of them, so
// sorting drift by path alone is a total order.
const (
	// DriftAdded is a path present in the snapshot and absent from the
	// manifest.
	DriftAdded DriftKind = iota + 1
	// DriftRemoved is a path present in the manifest and absent from the
	// snapshot.
	DriftRemoved
	// DriftChanged is a path present in both whose bytes differ.
	DriftChanged
)

// String returns the lowercase name of the kind, which is also the spelling
// used in reports.
func (k DriftKind) String() string {
	switch k {
	case DriftAdded:
		return "added"
	case DriftRemoved:
		return "removed"
	case DriftChanged:
		return "changed"
	default:
		return "unknown"
	}
}

// A Drift is one disagreement between the manifest and the snapshot as it
// stands now. Both sides are carried, zero where the path does not exist on
// that side, so a caller can report "1.2 kB became 0 bytes" without walking
// the tree a second time.
type Drift struct {
	// Kind is how the path drifted.
	Kind DriftKind
	// RelPath is the '/'-normalized path relative to the snapshot root.
	RelPath string
	// WantSize and WantSHA256 are what the manifest recorded. They are zero
	// and empty for [DriftAdded].
	WantSize   int64
	WantSHA256 string
	// GotSize and GotSHA256 are what the snapshot holds now. They are zero and
	// empty for [DriftRemoved].
	GotSize   int64
	GotSHA256 string
}

// Redigest re-walks the snapshot and reports every way it no longer matches
// the manifest, sorted by path.
//
// This is the gate behind a specific hazard. All workers share one snapshot,
// so a test that writes into its own package directory — a golden file it
// "updates", a database it creates in testdata — corrupts the tree that every
// later mutant is tested against, and the run's results quietly become
// unreproducible. Running this after the instrumented baseline turns that into
// a named list of files and an exit code instead.
//
// Two deliberate choices:
//
// The walk applies no exclusions. [Options.Exclude] describes what is worth
// copying out of a user's tree; here every byte under the snapshot root is
// ours, and something appearing inside a directory that would have been
// excluded on the way in is exactly the surprise worth reporting.
//
// A symbolic link, junction, or device found in the snapshot is returned as an
// [Error], not as a [Drift]. It is not drift in a file's contents, it is a
// tree that has grown a shape this package refuses to reason about, and both
// answers reach the caller as the same failed run.
//
// Redigest compares against the manifest [Create] recorded. After the
// instrumentation phase has rewritten the snapshot on purpose, that manifest
// no longer describes the tree; the caller holding the intended rewrites is
// the one that knows which of the reported paths were its own doing.
func (s *Snapshot) Redigest() ([]Drift, error) {
	w := &walker{root: s.Root}
	if err := w.walk(""); err != nil {
		return nil, err
	}
	if err := w.rejection(); err != nil {
		return nil, err
	}

	want := make(map[string]Entry, len(s.Manifest))
	for _, e := range s.Manifest {
		want[e.RelPath] = e
	}

	var drifts []Drift
	seen := make(map[string]struct{}, len(w.files))
	for _, f := range w.files {
		seen[f.rel] = struct{}{}
		size, sum, err := hashFile(f.abs)
		if err != nil {
			return nil, &Error{Code: CodeWalk, Path: f.rel, Message: "cannot read the file in the snapshot", Err: err}
		}
		recorded, ok := want[f.rel]
		switch {
		case !ok:
			drifts = append(drifts, Drift{Kind: DriftAdded, RelPath: f.rel, GotSize: size, GotSHA256: sum})
		case recorded.SHA256 != sum || recorded.Size != size:
			drifts = append(drifts, Drift{
				Kind:       DriftChanged,
				RelPath:    f.rel,
				WantSize:   recorded.Size,
				WantSHA256: recorded.SHA256,
				GotSize:    size,
				GotSHA256:  sum,
			})
		}
	}
	for _, e := range s.Manifest {
		if _, ok := seen[e.RelPath]; !ok {
			drifts = append(drifts, Drift{
				Kind:       DriftRemoved,
				RelPath:    e.RelPath,
				WantSize:   e.Size,
				WantSHA256: e.SHA256,
			})
		}
	}
	slices.SortFunc(drifts, func(a, b Drift) int { return strings.Compare(a.RelPath, b.RelPath) })
	return drifts, nil
}

// Restore puts the snapshot back the way [Create] left it, by copying from the
// tree it was made of.
//
// It is [Snapshot.Redigest] with an answer instead of a report: every changed
// or removed file is copied again from [Snapshot.SourceRoot], every added file
// is deleted, and the drifts it found are returned so a caller can say what it
// undid. A snapshot that has not drifted is not touched at all.
//
// # What it is for
//
// One thing, and the design of the whole isolation feature rests on it: a
// worker's private copy of an instrumented tree has to be identical before
// every mutant it runs. Without that, mutant *k* is measured against whatever
// mutant *k-1*'s tests wrote, which is the same corruption the shared-snapshot
// drift gate exists to catch, moved into a place nobody is watching. Copying
// the whole tree again between mutants would be correct and unaffordable;
// copying back only what moved is the same answer at the cost of one walk.
//
// # What it does not restore
//
// A directory that was created and left empty. The manifest lists regular
// files and nothing else -- [Redigest] says why -- so an empty directory is
// invisible to both, and a test that creates one and asks whether it exists
// would see it on its second mutant. That is a narrower gap than it sounds
// and it is the manifest's gap rather than this method's: a directory holding
// a file is restored, because the file is.
//
// Permissions and modification times are the copy's, not the original's,
// exactly as they are for a fresh [Create].
//
// # The postcondition
//
// Every file it copies is digested as it is written and compared with what the
// manifest recorded. A disagreement is [CodeRestoreFailed] rather than a
// silent success, because the only way to reach it is for the *source* tree to
// have changed -- and a caller restoring from a tree it believes is frozen has
// to be told that it is not.
func (s *Snapshot) Restore() ([]Drift, error) {
	drifts, err := s.Redigest()
	if err != nil {
		return nil, err
	}
	for _, change := range drifts {
		if err := s.restoreOne(change); err != nil {
			return nil, err
		}
	}
	return drifts, nil
}

// restoreOne undoes one drift.
func (s *Snapshot) restoreOne(change Drift) error {
	dest := filepath.Join(s.Root, filepath.FromSlash(change.RelPath))
	// Removed first in every case, including the changed one: copyFile opens
	// its destination O_EXCL on purpose, and that refusal is worth keeping for
	// the walk that uses it.
	if err := os.RemoveAll(ExtendedPath(dest)); err != nil {
		return &Error{
			Code:    CodeRestoreFailed,
			Path:    change.RelPath,
			Message: "the drifted file could not be removed before being restored",
			Err:     err,
		}
	}
	if change.Kind == DriftAdded {
		return nil
	}

	src := filepath.Join(s.SourceRoot, filepath.FromSlash(change.RelPath))
	info, err := os.Stat(ExtendedPath(src))
	if err != nil {
		return &Error{
			Code:    CodeRestoreFailed,
			Path:    change.RelPath,
			Message: "the tree this snapshot was made of no longer holds the file",
			Err:     err,
		}
	}
	if mkErr := os.MkdirAll(filepath.Dir(ExtendedPath(dest)), 0o755); mkErr != nil {
		return &Error{
			Code:    CodeRestoreFailed,
			Path:    change.RelPath,
			Message: "the directory holding the restored file could not be created",
			Err:     mkErr,
		}
	}
	_, digest, err := copyFile(src, dest, info.Mode(), info.ModTime())
	if err != nil {
		return &Error{
			Code:    CodeRestoreFailed,
			Path:    change.RelPath,
			Message: "the file could not be copied back",
			Err:     err,
		}
	}
	if digest != change.WantSHA256 {
		return &Error{
			Code: CodeRestoreFailed,
			Path: change.RelPath,
			Message: "the restored file digests " + digest + " and the manifest recorded " +
				change.WantSHA256 + ", so the tree this snapshot was made of has itself changed",
		}
	}
	return nil
}
