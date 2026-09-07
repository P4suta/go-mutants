// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package app is the module a run inside this workspace is pointed at.
//
// It deliberately imports nothing from `fixture.example/workspace/lib`, and
// that is the fixture's sharpest constraint rather than an omission. A run
// pointed at `app/` snapshots `app/` alone — the workspace file is one
// directory above the root it was given and is not copied — so the snapshot is
// resolved as the single module it contains. An import of the sibling module
// would therefore not be a scope test at all: it would be a `go build` failure
// in the snapshot, and the fixture would stop being able to say anything about
// what a run inside a workspace measures.
//
// Every mutant here is killed by [app_test.go], which is the other half of the
// claim: the sibling module's three mutants all survive on purpose, so a run
// that ever reached across the workspace would report survivors and a lower
// score.
package app

// Fold subtracts the smaller value from the larger one, and adds them when they
// are not ordered that way.
//
// The `>` rather than `>=` is what makes the equal case worth writing down:
// `gt-to-ge` changes the answer only when a and b agree, so a test table
// without that row leaves a survivor.
func Fold(a, b int) int {
	if a > b {
		return a - b
	}
	return a + b
}
