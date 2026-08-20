// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package core holds three comparisons with three different coverage fates.
//
// The whole fixture exists to make "which test binary reaches this line" a
// question with three different answers in one module, and every one of them is
// a claim the integration tests assert:
//
//   - [AboveZero] is reached only by this package's own tests, so exactly one of
//     the two test binaries covers its mutant.
//   - [Differs] is reached only by the caller package's tests, so exactly the
//     *other* binary covers its mutant. This is the case coverage-guided
//     selection is for: a mutant that lives here and can only be killed there.
//   - [Orphan] is reached by nothing, so no binary covers its mutant, and the
//     run reports it as an uncovered survivor without ever starting a process
//     for it.
//
// Each function carries exactly one mutable site and no two share a rule, so a
// mutant can be named by (path, rule) alone without depending on the
// catalogue's order or on an identity that moves when a byte of this fixture
// does. Nothing here uses an operator family outside comparison, which keeps
// the catalogue at three whatever else the profile selects.
package core

// AboveZero reports whether v is positive.
//
// Its `>` is killed by this package's own tests, which is only true because
// [TestAboveZero] asks about zero: `>` and `>=` agree on every other input, and
// a test that only checked 1 and -1 would leave the mutant alive and this
// fixture proving nothing.
func AboveZero(v int) bool {
	return v > 0
}

// Differs reports whether a and b are unequal.
//
// Nothing in this package calls it and no test here asserts anything about it.
// The caller package does both, which is what makes its mutant covered by
// exactly one binary — and the one that is not the binary of the package the
// mutant lives in.
func Differs(a, b int) bool {
	return a != b
}

// Orphan reports whether a is below b.
//
// Nothing anywhere calls it. Its mutant is compiled into both test binaries and
// is activated by exactly the same mechanism as the other two; what it does not
// have is a single test that reaches the line, which is what the run has to
// notice before it spends a process finding out.
func Orphan(a, b int) bool {
	return a < b
}
