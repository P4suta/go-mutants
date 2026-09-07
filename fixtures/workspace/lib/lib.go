// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package lib is the sibling module the workspace joins to `app`, and the half
// of the fixture a run pointed at `app/` must never reach.
//
// Its one function is under-tested on purpose: [lib_test.go] calls it and
// asserts nothing about the answer, so all three of its mutants survive —
// `neq-to-eq` on the comparison, and `return-true` and `return-false` on the
// returned expression. That is what makes the absence observable. A run that
// leaked across the workspace would still be green — nothing here fails — and
// would differ only in reporting three survivors and a lower score, which is a
// difference a test can state.
package lib

// Differs reports whether a and b are different values.
func Differs(a, b int) bool {
	return a != b
}
