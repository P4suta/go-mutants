// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package rejectable

// Level returns the level this fixture runs at.
//
// TRAP, of the second shape: an untyped constant that stops fitting its
// context. `200 - 100` is 100 and fits a uint8; `sub-to-add` makes it 300,
// which does not, and the compiler refuses the mutated copy with an overflow
// rather than a division. Two shapes are here because a phase that isolated one
// and not the other would look like it worked.
//
// The value is returned rather than assigned so that this trap sits in a
// statement guard, while the one below sits in a declaration guard: the two
// traps of this file are reached through different rewrite forms, which is the
// property that would have caught the fixture's previous traps rotting.
func Level() uint8 {
	return 200 - 100
}

// Ratio returns one, by way of a declaration whose value always vanishes.
//
// TRAP, the division shape again, in the one place the fixture would otherwise
// never exercise: a `:=` is a declaration site, so the mutated copy is written
// into an assignment the guard hoists a `var` out in front of. If a rewrite
// form ever stopped producing a compilable original branch here, this is where
// the instrumented baseline would say so.
//
// Healthy neighbours, again deliberately: the addition below and the value the
// function returns both compile and both die.
func Ratio(v int) int {
	scaled := v * 0
	return scaled + 1
}
