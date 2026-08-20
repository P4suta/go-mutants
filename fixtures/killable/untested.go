// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package killable

// Untested reports whether a and b differ.
//
// Nothing in this module calls it and no test asserts anything about it, which
// is the point. Its `neq-to-eq` mutant is compiled into the same test binary as
// every other mutant here and is activated by exactly the same mechanism, and it
// still cannot change any test's outcome: it is the fixture's survivor. A run
// that reports it killed has found a bug in activation, not a better test suite.
func Untested(a, b int) bool {
	return a != b
}
