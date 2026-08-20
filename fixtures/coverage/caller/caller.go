// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package caller is the second test binary, and the only reason
// [core.Differs] is reached at all.
//
// It holds no comparison and no boolean literal of its own, so it contributes
// nothing to the catalogue: the three mutants of this fixture all live in
// package core, and this package's job is to be a *different binary* that
// reaches one of them.
package caller

import "fixture.example/coverage/core"

// Changed reports whether a and b differ, by asking core.
func Changed(a, b int) bool {
	return core.Differs(a, b)
}
