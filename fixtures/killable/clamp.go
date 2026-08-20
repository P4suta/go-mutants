// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package killable is the fixture the end-to-end kill is proved against.
//
// Every function here exists to be mutated, and each one has a predetermined
// fate: the two mutants in [Clamp] and the one in [IsReady] are killed by the
// tests in this package, and the one in [Untested] survives because nothing
// calls it. Both halves are needed. "A mutant died" only proves the mechanism
// works if the same instrumented tree also shows a mutant living — a run where
// everything dies is indistinguishable from a tree that fails to build, and one
// where everything survives is indistinguishable from activation that never
// happened.
//
// Each function lives in its own file, and no two of them use the same operator.
// That is what lets a test name a mutant by (path, rule) alone and get exactly
// one back, without depending on the catalogue's order or on an identity that
// changes whenever a byte of this fixture moves.
//
// The module is deliberately tiny and imports nothing outside the standard
// library's testing package: it is snapshotted, instrumented, built, and tested
// four times over in a single integration test, so every second spent here is
// paid for repeatedly.
package killable

// Clamp confines v to the open range (lo, hi), where both bounds are exclusive:
// a value at or below lo becomes lo+1, and a value at or above hi becomes hi-1.
// Callers pass lo < hi.
//
// The exclusive bounds are load-bearing, and "correcting" them to the inclusive
// ones a reader expects breaks this fixture in a way no compiler and no test in
// this package will catch. A clamp with inclusive bounds returns the bound
// itself at the bound, so `v < hi` and `v <= hi` agree on every input there is:
// at v == hi both answer hi, and at every other value the comparison lands the
// same way. The boundary mutants would then be equivalent mutants — unkillable
// by any test, because no input distinguishes them — and the integration test
// that activates one and expects a failure would be waiting for a failure that
// cannot happen. Returning lo+1 and hi-1 is what makes each boundary
// observable, and therefore what makes these mutants killable at all.
func Clamp(v, lo, hi int) int {
	if v < hi {
		if v > lo {
			return v
		}
		return lo + 1
	}
	return hi - 1
}
