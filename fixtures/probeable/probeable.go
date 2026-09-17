// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package probeable is the fixture the probe session is proved against.
//
// It holds four returns and no other mutable expression, which is the whole
// design: every mutant here is a one-line `return`, so what a probe pass
// records about it is decidable by reading the file. Three of them are probed —
// two return-value mutants and a boolean literal the boolean form measures
// where it stands — and [Doubled]'s is not. The fixture needs both: "every
// probed mutant behaves" only means something beside a mutant that is
// deliberately unprobed, since a consumer has to treat that one as infected by
// every test.
//
// The unprobed one is unprobed for a reason no later form can lift, which is
// what makes it a specimen rather than a snapshot of today's coverage. A probe
// stands in for a mutant by evaluating what the original evaluates, so an
// operand with an effect is one no rewrite may evaluate a second time or skip
// on the mutant's behalf. [Doubled]'s operands are calls.
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
// The catalogue keeps `true-to-false` over the `return-false` proposing the
// same edit, and the boolean form measures the literal where it stands: the two
// readings are `true` and `false`, which differ every time the site is
// evaluated, so a test that reached it names this mutant and one that did not
// is a test that never called Ready.
//
// This used to be the fixture's unprobed specimen, back when no form covered a
// boolean literal. [Doubled] is the specimen now, and it is a better one: it is
// unprobed because of what its operands *are* rather than because of which
// forms happen to exist.
func Ready() bool {
	return true
}

// A Size is a pair of dimensions, and the reason [Doubled] returns one.
//
// No return-value rule proposes anything for a struct, so Doubled's `return`
// carries exactly one mutant: the addition inside it. That keeps the fixture's
// own rule — no two functions share an operator, so a rule names exactly one
// mutant — which is how every test here picks a mutant out of the catalogue.
type Size struct {
	// W is twice the fixture's width, computed rather than written.
	W int
	// H is a literal, so that the struct has a field the addition does not
	// reach and the mutant's blast radius is visible.
	H int
}

// Doubled returns the fixture's width twice over, through calls rather than
// literals.
//
// This is the fixture's unprobed mutant, and it is unprobed for a reason no
// later form can lift: both operands of the addition are calls. A probe stands
// in for a mutant by evaluating what the original evaluates, so the return form
// needs every operand of the statement effect-free — the mutant it stands in
// for *skips* one, and nothing skipped may have mattered — and the boolean form
// needs the same of the whole site, since it evaluates both readings. So
// `add-to-sub` here is catalogued, mutated and killed by TestDoubled, while no
// probe pass can say anything about it.
func Doubled() Size {
	return Size{W: Width() + Width(), H: 1}
}
