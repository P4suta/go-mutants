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

// The projection's coordinate arithmetic and the HTML page's escaping are
// exposed for the same reason as the claim: they are the two places where being
// almost right produces a document that validates, renders, and points at the
// wrong characters. A test that could only reach them through a whole run would
// have to work backwards from a rendered page to find out which of the two was
// wrong.

// UTF16Position converts a byte offset in src into the published coordinate.
func UTF16Position(src []byte, offset int) Position {
	return newSourceIndex(src).position(offset)
}

// UTF16OffsetAt converts a 1-based line and 1-based byte column back into a
// byte offset, which is how a rejected mutant's coordinate is projected.
func UTF16OffsetAt(src []byte, line, column int) int {
	return newSourceIndex(src).offsetAt(line, column)
}

// UTF16Units counts the UTF-16 code units in s.
func UTF16Units(s string) int { return utf16Units(s) }

// EscapeScriptData exposes the JSON island's escaping.
func EscapeScriptData(document []byte) []byte { return escapeScriptData(document) }

// Bootstrap is the page's own script, whose bytes the Content-Security-Policy
// hashes.
const Bootstrap = bootstrap

// BreakVendoredViewer makes the vendored-asset check fail, and returns the
// function that puts it back. See [verifyViewer] for why the seam exists; a
// test that uses it cannot be parallel.
func BreakVendoredViewer(cause error) func() {
	previous := verifyViewer
	verifyViewer = func() (string, error) { return "", cause }
	return func() { verifyViewer = previous }
}
