// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package mutantkit

import "time"

var NormalizeText = normalizeText

func FakeGoInstalls() int64 { return fakeGoInstalls.Load() }

func SetFakeGoLinker(link func(from, to string) error) func() {
	previous := linkFile
	linkFile = link
	return func() { linkFile = previous }
}

func FakeGoRemovalPolicy(goos string) (attempts int, delay time.Duration) {
	return fakeGoRemovalPolicy(goos)
}

func PlatformLinker(goos string) func(from, to string) error { return platformLinker(goos) }

func LinkOrCopyExecutable(from, to string) error { return linkOrCopyExecutable(from, to) }
