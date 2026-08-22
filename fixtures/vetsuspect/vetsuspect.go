// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package vetsuspect is the fixture for the one thing an instrumented snapshot
// holds that is legal Go and that the go command refuses to compile.
//
// Every other fixture is about what go-mutants finds, rewrites or measures.
// This one is about what the *toolchain* thinks of the rewrite. A Form C guard
// splices each mutant of an expression in beside the original, rendered from
// the pristine bytes with that single edit applied — so mutating the `||` of
// `s == "." || s == ".."` writes `s == "." && s == ".."` into the snapshot
// verbatim. That expression is perfectly legal, always false, and precisely
// what vet's `bools` analyzer exists to report:
//
//	suspect and: s == "." && s == ".."
//
// `go test` and `go test -c` run a default subset of vet before they compile,
// which includes `bools`, so an instrumented tree containing this shape used to
// stop the run twice over — at GOM4013 when the instrumented baseline ran the
// test command, and at GOM7505 when a per-package test binary was built — with
// a diagnostic about generated code the user never wrote. The engine now issues
// both against the instrumented tree with `-vet=off` merged into GOFLAGS, and
// this fixture is what proves it: without the suppression the run dies before
// any mutant executes; with it, every mutant here is measured and killed.
//
// # The invariant
//
// The two functions below are not interchangeable with any other pair of
// boolean helpers, and an edit that "simplifies" them disarms the fixture while
// leaving it compiling and green. `bools` reports only two families of shape:
// a duplicated operand, and a comparison of one operand against two different
// constants — `x == c1 && x == c2`, which cannot be true, and
// `x != c1 || x != c2`, which cannot be false. So the source must offer both of
// those as *mutants*, which means it must be written as their opposites:
//
//   - [IsDot] is `==` and `==` joined by `||`, so that `or-to-and` produces the
//     suspect `&&`.
//   - [NotDot] is `!=` and `!=` joined by `&&`, so that `and-to-or` produces the
//     suspect `||`.
//
// Both directions are kept because the boolean-connective family has exactly
// two rules and each one reaches a different half of the analyzer. Comparing
// against the same constant twice, or against something that is not a constant,
// or with a relational operator instead of an equality one, all leave vet with
// nothing to say — and the regression would go unnoticed.
//
// The real code this was found in reads
// `cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../")`, in
// go-mutants' own internal/mutation/id.go. The shape is ordinary path handling,
// not a contrivance.
package vetsuspect

// IsDot reports whether s is one of the two relative directory names.
//
// KILLED, every mutant. Its `||` is the fixture's first trap: `or-to-and`
// rewrites it to `s == "." && s == ".."`, which is what vet calls a suspect
// and. TestIsDot's rows are what kill the rest — a row that should be true for
// the connective and the `false` replacement, a row that should be false for
// the equality swaps and the `true` one.
func IsDot(s string) bool {
	return s == "." || s == ".."
}

// NotDot reports whether s is neither of them.
//
// KILLED, every mutant. It is not merely the negation of [IsDot] written twice:
// its `&&` is the other trap, because `and-to-or` rewrites it to
// `s != "." || s != ".."`, which is what vet calls a suspect or. Replacing this
// function with `return !IsDot(s)` would leave the package doing the same thing
// and would delete half of what the fixture proves.
func NotDot(s string) bool {
	return s != "." && s != ".."
}
