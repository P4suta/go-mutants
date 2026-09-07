// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import "time"

// NormalizeText exposes the free-text rule to the external tests.
var NormalizeText = normalizeText

// FakeGoInstalls is how many times this process has linked or copied the test
// binary as a scripted `go`.
//
// It is the resource claim [FakeGo] rests on — one install per test binary,
// however many fakes a suite builds — and it is a counter rather than a
// directory listing because the shared install is removed when the suite ends.
func FakeGoInstalls() int64 { return fakeGoInstalls.Load() }

// SetFakeGoLinker replaces the hard-link half of an install and returns the
// undo, so the copy fallback can be driven on a machine where linking works.
//
// It is a process-wide write, so a test using it may not be parallel. The
// fallback is not an edge case anywhere it matters: on a Windows runner the
// temporary directory and the build cache are on different volumes, os.Link
// cannot answer, and every install this package makes is a copy.
func SetFakeGoLinker(link func(from, to string) error) func() {
	previous := linkFile
	linkFile = link
	return func() { linkFile = previous }
}

// FakeGoRemovalPolicy exposes how hard the shared install's removal tries on a
// given platform, so the Windows rule can be checked from a machine that is not
// one.
func FakeGoRemovalPolicy(goos string) (attempts int, delay time.Duration) {
	return fakeGoRemovalPolicy(goos)
}

// PlatformLinker exposes the linker a platform gets, so that "Windows never
// links" is a fact about the code rather than about the comment above it.
func PlatformLinker(goos string) func(from, to string) error { return platformLinker(goos) }

// LinkOrCopyExecutable exposes the install step itself, so that each of the
// three answers a linker can give is pinned against a source the test owns
// rather than against wherever `go test` happened to put this binary.
func LinkOrCopyExecutable(from, to string) error { return linkOrCopyExecutable(from, to) }
