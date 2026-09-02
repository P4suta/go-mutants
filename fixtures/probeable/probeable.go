// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package probeable is the fixture the probe session is proved against.
//
// It holds three returns and no other mutable expression, which is the whole
// design: every mutant here is a one-line `return` of a constant, so what a
// probe pass records about it is decidable by reading the file. Two of the
// three are return-value mutants and are probed; the third is a boolean literal
// and is not, because the boolean-literal rule wins deduplication over the
// return-value rule proposing the same edit and no probe form covers it. The
// fixture needs both: "every probed mutant behaves" only means something beside
// a mutant that is deliberately unprobed, since a consumer has to treat that
// one as infected by every test.
//
// The functions return values that differ from the constant their mutant would
// return — 3 rather than 0, "probe" rather than "" — on every call. That is
// what makes a probe of a test that calls one a *positive* fact rather than a
// coincidence: the site is infected the moment it is evaluated, so a test
// listed as not having infected it is a test that never reached it.
//
// No two functions share an operator, so a test names a mutant by rule alone
// and gets exactly one back, whatever order the catalogue settles on.
//
// The module is deliberately tiny and imports nothing outside the standard
// library's testing package: a session is prepared over it — snapshotted,
// instrumented twice, built twice, verified — for every test that uses it.
package probeable

// Width returns the fixture's width, which is never zero.
//
// `return-zero-numeric` rewrites the literal to 0, and TestWidth is what kills
// that. Never zero is the load-bearing half: a function that returned 0 for
// some input would leave its probe silent on a test exercising that input, and
// a silent probe is indistinguishable here from a test that never called it.
func Width() int {
	return 3
}

// Label returns the fixture's name, which is never empty.
//
// `return-empty-string` rewrites the literal to "", and TestLabel kills it. It
// is the second probed mutant so that a probe of one test can be shown to name
// one mutant and not the other — a measurement that reported every probed
// mutant for every test would pass a test with only one of them.
func Label() string {
	return "probe"
}

// Ready reports whether the fixture is ready, which it always is.
//
// This is the fixture's unprobed mutant. The catalogue keeps `true-to-false`
// over the `return-false` proposing the same edit, and a boolean literal has no
// probe form, so the mutant is catalogued, mutated, and killed by TestReady
// while no probe pass can ever say anything about it.
func Ready() bool {
	return true
}
