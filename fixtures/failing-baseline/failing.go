// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package failingbaseline is the fixture whose tests do not pass.
//
// It compiles cleanly, and that is the whole point: the failure has to surface
// at the baseline test step, with the tail of the test output attached, rather
// than at the build step. A fixture that did not compile would prove that the
// build gate works and would say nothing about the baseline gate.
package failingbaseline

// Double returns twice v. The implementation is correct; the test below asserts
// something else on purpose.
func Double(v int) int {
	return v * 2
}
