// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gocmd

import "testing"

func ParseVersion(output string) (Version, error) { return parseVersion(output) }

func SameEnvKeyOn(goos, a, b string) bool { return sameEnvKeyOn(goos, a, b) }

func Absolute(path string) (string, error) { return absolute(path) }

func FailAbsolutePath(t *testing.T, err error) {
	t.Helper()
	original := absolutePath
	absolutePath = func(string) (string, error) { return "", err }
	t.Cleanup(func() { absolutePath = original })
}
