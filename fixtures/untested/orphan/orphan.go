// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package orphan has no test file, and that absence is the whole specimen.
//
// Two things rest on it, and both would be lost the moment somebody added a
// `orphan_test.go` here out of tidiness:
//
//   - Every mutant in this package is settled as an uncovered survivor. No test
//     binary reaches the line, so the coverage pass answers for them without
//     executing anything, and the run reports survivors it never ran rather than
//     survivors it did.
//   - `go test ./orphan/...` is a test command whose every pattern names a real
//     package and which still produces no test binary. That is the one shape the
//     pattern check cannot catch, and it is refused by name rather than left to
//     report a score of zero as though it had looked.
//
// Nothing in the module imports it either, so no other package's test binary
// can reach it by accident.
package orphan

// Half returns n halved, rounded towards zero.
func Half(n int) int {
	return n / 2
}
