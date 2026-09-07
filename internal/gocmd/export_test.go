// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd

import "testing"

// This file is compiled only under `go test`. Version parsing is the part of
// this package worth testing exhaustively and the part no caller should be
// able to reach: [Locate] is the only supported way to obtain a [Version],
// because a Version that did not come from a real toolchain has no business
// appearing in a report.

// ParseVersion exposes the `go version` parser to the tests.
func ParseVersion(output string) (Version, error) { return parseVersion(output) }

// SameEnvKeyOn exposes the platform half of the GOFLAGS merge rule, so that the
// Windows spelling rule is asserted on the platform that does not use it.
func SameEnvKeyOn(goos, a, b string) bool { return sameEnvKeyOn(goos, a, b) }

// Absolute exposes the working-directory anchoring to the tests.
//
// The only way it fails is a process whose working directory has been taken
// away, and [Locate] cannot be steered into that from the outside: a relative
// explicit path has to resolve for exec.LookPath to hand it back at all, and
// resolving it needs the very directory that would have to be gone.
func Absolute(path string) (string, error) { return absolute(path) }

// FailAbsolutePath makes every resolution against the working directory fail
// with err for the length of the test.
func FailAbsolutePath(t *testing.T, err error) {
	t.Helper()
	original := absolutePath
	absolutePath = func(string) (string, error) { return "", err }
	t.Cleanup(func() { absolutePath = original })
}
