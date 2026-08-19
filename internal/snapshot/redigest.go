// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
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
