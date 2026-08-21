// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package rejectable

// A Flag is a boolean with a name of its own.
//
// This file is the control, and it is here because it used to be the trap.
//
// The fixture's first two traps lived in a file like this one: a comparison and
// a boolean literal returned as a `Flag`. Neither was a fact about the mutated
// program — both are perfectly good Go — they were facts about a *rewrite form*.
// Form C composes a bool selector, `bool` is not assignable to `Flag`, and so
// the guard, not the mutant, was what the compiler refused.
//
// That stopped being true when discovery started routing an edit with no
// exactly-`bool` expression around it to the statement form instead. A `return`
// wrapped in `if __gm.M[i] { … } else { … }` is well typed whatever the function
// returns, so these four candidates are now ordinary mutants: catalogued,
// instrumented, compiled, executed, and — because the tests below pin what they
// do — killed.
//
// Keeping them is the point. The improvement is only an improvement if
// something fails when it is undone, and a fixture that had simply dropped the
// named boolean would leave "Form S carries a named boolean result" as a claim
// in a changelog rather than as a test. The traps that replaced them are in
// [compare.go] and [limits.go], where they are facts about the mutated program
// that no rewrite form can rescue.
type Flag bool

// Ready reports whether a level has reached the threshold.
//
// The comparison is not wrapped in a conversion on purpose: `level >= 3` in a
// `Flag` result position has type `Flag` rather than `bool`, which is exactly
// the shape that has no Form C site around it. Writing `Flag(level >= 3)`
// instead would put an exactly-`bool` expression back inside the conversion and
// quietly turn this file into a test of the selector again.
func Ready(level int) Flag {
	return level >= 3
}

// Always reports the one answer it has.
//
// The boolean-literal half of the same shape. Its `true` is typed `Flag` in this
// position, so it too is reached through the statement form; the
// return-replacement rule that would have proposed the same `false` is the
// deduplicated one, and the boolean literal wins it for being the more local
// edit.
func Always() Flag {
	return true
}
