// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package killable

// IsReady reports whether the fixture is ready, which it always is.
//
// The literal is the whole point: `true-to-false` rewrites it, and the test
// that asserts what this returns is what kills that. A boolean literal in
// return position is also the second
// guard the instrumenter has to get right — the comparisons in [Clamp] are the
// first — so the fixture carries one of each shape rather than several of one.
func IsReady() bool {
	return true
}
