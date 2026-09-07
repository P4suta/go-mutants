// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package snapshot

import (
	"path/filepath"
	"strings"
)

// longPathThreshold is the length at which a path is rewritten into
// extended-length form. MAX_PATH is 260 including the terminating NUL, so 240
// leaves room for a file name to be appended to a directory path that was
// already close to the limit.
const longPathThreshold = 240

// ExtendedPath returns p in the `\\?\` extended-length form when it is long
// enough to be worth it, and p unchanged otherwise.
//
// It is exported because the paths this policy is about are not all this
// package's. A preparation copies the frozen tree a second time, into a
// directory some thirty characters deeper than the snapshot's, and a Windows
// user with a deep module has that much less room; the copy applies the same
// rule by calling this rather than by growing a second one that drifts.
//
// The Go standard library already does something very similar internally, so
// on a current toolchain a long path usually works without this. "Usually" is
// the problem: the conversion has moved between releases and is applied at
// different points by different packages, and a Windows user with a deep
// module under a deep home directory finds out at the worst moment. Doing it
// here makes the behaviour this package's own.
//
// Two rules make the form dangerous to apply carelessly, and both are why the
// short case is left alone:
//
//   - `\\?\` disables all path normalization. Forward slashes, "." and ".."
//     become literal component names, so the path must be fully backslashed
//     and cleaned first.
//   - A UNC path takes a different spelling: `\\server\share` becomes
//     `\\?\UNC\server\share`, not `\\?\\\server\share`.
//
// Relative and drive-relative paths ("C:file") have no extended-length form at
// all and are returned unchanged; every path this package builds is absolute.
func ExtendedPath(p string) string {
	if len(p) < longPathThreshold {
		return p
	}
	if strings.HasPrefix(p, `\\?\`) || strings.HasPrefix(p, `\\.\`) {
		return p
	}
	if !filepath.IsAbs(p) {
		return p
	}
	// Clean both normalizes separators to backslashes and resolves the "." and
	// ".." that the extended form would otherwise take literally.
	cleaned := filepath.Clean(p)
	if strings.HasPrefix(cleaned, `\\`) {
		return `\\?\UNC\` + cleaned[2:]
	}
	return `\\?\` + cleaned
}
