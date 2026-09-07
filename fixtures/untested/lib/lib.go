// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package lib is the half of this fixture that has a test file.
//
// It is here so that the module is not one where *nothing* is tested: a run
// over a module with no test binary at all is a different case, already stated
// by `policy.require_mutants` and by the score, and it would make the orphan
// package's uncovered survivors indistinguishable from a suite that was never
// built.
package lib

// Double returns twice n.
func Double(n int) int {
	return n * 2
}
