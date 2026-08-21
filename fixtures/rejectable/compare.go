// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package rejectable is the fixture compile validation is proved against.
//
// It holds two kinds of candidate side by side, in the same package and in the
// same files, and the whole point is that a run has to tell them apart by
// compiling them:
//
//   - Candidates whose mutated copy is not a program. internal/instrument
//     writes the guard for those exactly as it writes any other and says so out
//     loud — deciding it there would mean type-checking every file to ask a
//     question the compiler answers for free — which makes isolating them this
//     phase's job.
//   - Candidates that are perfectly fine and must survive the isolation
//     untouched, in the same files as the traps, so that "reject the file" is
//     not an answer that passes.
//
// # What makes a trap a trap here
//
// Both trap shapes are facts about the *mutated program* rather than about the
// guard that carries it, and that is deliberate. The fixture's first traps were
// facts about a guard form — a comparison returned as a named boolean type,
// whose Form C selector evaluated to plain `bool` — and they stopped being
// traps the day discovery started routing named booleans to the statement form.
// A trap that a change of rewrite shape can disarm proves nothing after that
// change, and disarms silently: the module still compiles, its tests still
// pass, and the phase is left with nothing to isolate.
//
// So the two shapes here are ones no rewrite can rescue:
//
//   - A constant division by zero. `v*0` becomes `v/0` under mul-to-div, and
//     `v / 0` is not Go in any context, guarded or not.
//   - A constant that no longer fits. `200 - 100` returned as a `uint8` becomes
//     `200 + 100` under sub-to-add, and 300 does not fit in a byte.
//
// Both are in [limits.go] as well as here, and each file keeps healthy
// candidates outnumbering its traps, so isolating them is real halving rather
// than a coin toss.
package rejectable

// InRange reports whether v lies within the inclusive range [lo, hi].
//
// The healthy candidates this file is mostly made of, and they are here to be
// counted. A file with one trap and one healthy candidate cannot tell a
// bisection from a coin toss; this function alone holds seven candidates that
// all compile and all die, so isolating the single bad one takes real halving,
// and so a bug which threw away the other half of a split would leave several
// accepted mutants missing rather than one.
func InRange(v, lo, hi int) bool {
	if v < lo {
		return false
	}
	if v > hi {
		return false
	}
	return true
}

// Erased returns one, by way of a multiplication that always vanishes.
//
// TRAP, and the reason this file has one at all: the file the bisection has to
// work hardest on is also the file that must come out of it with every healthy
// mutant intact. `mul-to-div` rewrites `v*0` as `v/0`, which is a constant
// division by zero and a compile error wherever it is written — inside a guard
// branch as much as anywhere else.
//
// The `+ 1` is not decoration. It gives the trap two healthy neighbours in its
// own statement — the addition and the whole returned value — so the trap
// cannot be isolated by rejecting the statement it sits in.
func Erased(v int) int {
	return v*0 + 1
}
