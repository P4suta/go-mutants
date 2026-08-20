// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package rejectable is the fixture compile validation is proved against.
//
// It holds two kinds of candidate side by side, in the same package and in the
// same files, and the whole point is that a run has to tell them apart by
// compiling them:
//
//   - Candidates whose guard cannot compile. Form C evaluates to `bool`, so a
//     bool-valued expression sitting where the named boolean type [Flag] is
//     wanted becomes a type error the moment it is guarded. internal/instrument
//     instruments them anyway and says so out loud — deciding it there would
//     mean type-checking every file to ask a question the compiler answers for
//     free — which makes isolating them this phase's job.
//   - Candidates that are perfectly fine and must survive the isolation
//     untouched, in the same files as the traps, so that "reject the file" is
//     not an answer that passes.
//
// [Flag] is what makes the traps traps. A comparison yields an *untyped*
// boolean value, which is assignable to any boolean type, so `return x > y`
// compiles in a function returning Flag; the guard that replaces it is an
// ordinary `&&`/`||` expression over a `bool` variable, which is typed, and
// `bool` is not assignable to `Flag`. Nothing about that is visible in the
// bytes discovery produced — it is a fact about the context the bytes sit in —
// which is exactly why the compiler is the oracle here.
package rejectable

// Flag is a named boolean type: a distinct type whose underlying type is bool.
//
// Assignment between it and `bool` is not allowed in either direction, and that
// is not incidental to this fixture — it is the whole mechanism. "Correcting"
// this to `type Flag = bool`, a type alias, would leave every function below
// compiling exactly as it does now, every test passing, and every trap in this
// file silently accepted, so the validation phase would be proved against a
// module that has nothing to isolate.
type Flag bool

// Above reports whether x is above y.
//
// TRAP. The comparison is an untyped boolean value and assignable to Flag as it
// stands; guarded, it is a typed `bool` and is not. Its mutant must come back
// rejected, with the compiler's own words attached and this file and line
// named.
func Above(x, y int) Flag {
	return x > y
}

// Ready reports whether the fixture is ready, which it always is.
//
// TRAP, of the second shape: an untyped boolean *constant* rather than a
// comparison. It is assignable to Flag for the same reason and stops being so
// for the same reason, and it is here because a phase that isolated one shape
// and not the other would look like it worked.
func Ready() Flag {
	return true
}

// Enabled reports whether x and y differ.
//
// Healthy, and deliberately in the same file as both traps. The comparison is
// bound to a variable of inferred type `bool` before anything converts it, so
// the guard replaces an expression whose context wants precisely what the guard
// produces. Its mutant has to survive validation: a phase that rejected a whole
// file, or that stopped bisecting once it had found something, would take this
// one down with the two above it and no test that only counted rejections would
// notice.
func Enabled(x, y int) Flag {
	ok := x != y
	return Flag(ok)
}
