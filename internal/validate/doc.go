// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package validate compiles an instrumented snapshot and finds out, one build
// at a time, which of its mutants are real.
//
// [Validate] is the whole of it: hand it a snapshot and a catalogue and it
// comes back with the mutants that compile, the mutants that do not together
// with the compiler's reason for each, and a snapshot holding exactly the
// former.
//
// # Why a phase exists for this
//
// Instrumentation is a byte rewrite. It writes a guard around an expression, a
// statement, or a declaration and leaves typing to the compiler, which is what
// makes the guard forms trustworthy and is also why some of them cannot
// compile: a mutated copy can be a program the compiler refuses — `v * 0`
// swapped into `v / 0` is a constant division by zero, and an operator swap can
// push an untyped constant out of the range its context allows. See
// internal/instrument on why deciding that during instrumentation would mean
// type-checking every file to ask a question the compiler answers for free, and
// answering it conservatively would drop candidates that were fine.
//
// So the compiler is this phase's oracle, and the phase's job is to ask it
// precisely enough that one bad candidate costs one candidate rather than a
// file, a package, or a run.
//
// # The shape of the search
//
// The fast path is one build. Every catalogued mutant is instrumented at once,
// `go build ./...` compiles the tree, and a green build accepts the whole
// catalogue. That is the ordinary case, and making it ordinary is the entire
// point of the schemata design: one build serves every mutant.
//
// A red build starts a search, and everything about its shape follows from one
// requirement — whether a subset of one file compiles has to be a question
// about that file:
//
//  1. Every catalogued file is restored to its pristine bytes and the tree is
//     built again. A failure here is not something this phase can fix, so it
//     stops with [CodeNotMutantInduced] rather than bisecting a tree that was
//     already broken and blaming whichever candidate it tested first. A success
//     also establishes what the search below assumes: the empty subset compiles.
//  2. The files the compiler named are searched one at a time, while every
//     undecided file stays pristine. Each search halves while halving is
//     cheaper than scanning, scans below [linearThreshold], verifies every join,
//     and falls back to scanning when a join fails — which is what makes a pair
//     of candidates that only fail together an ordinary case rather than a
//     wrong answer. Each file ends up holding the largest subset of its
//     candidates that a build was seen to accept.
//  3. The undecided files go back in whole and the tree is built again.
//     Whatever the compiler names this time is searched the same way. The loop
//     ends on a green build; a red one with nothing left undecided means
//     candidates in separate files interact, which is [CodeStillFailing].
//
// The generated runtime package is written once, by the instrumentation pass,
// and never regenerated. Its activation array is sized by the full catalogue
// and every guard in the tree spells its own dense index, so a runtime rebuilt
// from a subset would renumber flags that files elsewhere still read — every
// later rewrite goes through [instrument.InstrumentFile], which writes guards
// and nothing else.
//
// # Rejections are data
//
// A candidate that cannot compile is an expected outcome, not a failure. It
// comes back as a [Rejection] carrying its identity, the coordinates discovery
// would have printed for it, and the compiler's own words about the build that
// condemned it — captured at the moment of rejection, because by the time the
// phase finishes the tree compiles and that message no longer exists anywhere.
//
// The errors this package returns are the other thing entirely: a build that
// could not be run, a tree that was broken before go-mutants touched it, a
// cancellation. Those stop the run. A mutant that will not compile does not.
//
// # Determinism
//
// The same snapshot and the same catalogue produce the same accepted set, the
// same rejections in catalogue order, and the same bytes on disk. Every list
// this package builds is ordered by the catalogue or by path rather than by
// discovery order or by map iteration, and the search itself is a deterministic
// function of the verdicts it receives. It inherits one assumption from the
// toolchain — that compiling the same bytes reports the same diagnostics — which
// is worth stating out loud, because the search would inherit any drift in it.
package validate
