// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package snapshot

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type DriftKind uint8

const (
	DriftAdded DriftKind = iota + 1
	DriftRemoved
	DriftChanged
)

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

type Drift struct {
	Kind       DriftKind
	RelPath    string
	WantSize   int64
	WantSHA256 string
	GotSize    int64
	GotSHA256  string
}

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

func (s *Snapshot) restoreOne(change Drift) error {
	dest := filepath.Join(s.Root, filepath.FromSlash(change.RelPath))
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
