// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package tagged is the build-constraint fixture: two candidates, one of which
// only exists when the `special` tag is on.
//
// The whole module is two boolean literals and the two tests that pin them, and
// the smallness is the point. What is under test is not what the operators do —
// every other fixture covers that — but that the *file set* a run compiles is
// the file set the environment asked for: a catalogue of one becomes a
// catalogue of two under `GOFLAGS=-tags=special`, and the outcomes measured
// under one tag are not reused under the other.
//
// One candidate per file and one rule per candidate is what keeps the count
// legible: a run's catalogue size is the number of files it compiled.
package tagged

// Plain is the candidate every run of this fixture sees.
func Plain() bool {
	return true
}
