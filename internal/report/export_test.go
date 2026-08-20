// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package report

// This file is compiled only under `go test`.
//
// The ownership claim is the one part of the history store the public API
// cannot be made to exercise properly. [History.WorkspaceDir] derives the
// directory from the workspace digest, so two different workspaces never name
// one directory and the collision the marker exists to catch cannot be staged
// through [History.Write] at all. The tests reach past that to the claim
// itself, which is the only place the two-workspaces-one-directory race can be
// written down.

// Claim exposes the ownership claim to the tests. See [History].
func Claim(dir, workspaceDigest string) error { return claim(dir, workspaceDigest) }

// CreateMarkerInPlace exposes the claim's fallback, which no test could
// otherwise reach: it is used only on a filesystem that refuses to hard-link,
// and the machines these tests run on do not have one. What it must still be is
// a refusal rather than a replacement, and that is checkable here.
func CreateMarkerInPlace(path, content string) error { return createMarkerInPlace(path, content) }

// ErrMarkerExists exposes the sentinel both create paths answer an already
// claimed directory with.
var ErrMarkerExists = errMarkerExists
