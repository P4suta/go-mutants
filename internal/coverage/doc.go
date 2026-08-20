// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package coverage reads Go coverage profiles and decides which test binaries
// each mutant needs to be measured against.
//
// It is pure. Nothing here starts a process, reads the file system, or knows
// what a snapshot is: [ParseTextfmt] takes bytes and [Map] takes values, which
// is what makes the mapping rule — the one part of coverage-guided selection
// that can silently cost a run its kills — testable as a table.
//
// # Lines only, never columns
//
// A block record in `go tool covdata textfmt` carries both line and column
// coordinates, and this package uses only the lines. That is a correctness
// requirement rather than a simplification.
//
// The profile is collected from the *instrumented* snapshot: go-mutants has
// already rewritten the sources into guard form, and `go test -c -cover` then
// instruments that rewrite. Columns therefore describe the guarded text, and a
// mutant's span was measured against the user's original bytes — the two do not
// agree and cannot be made to. Lines do agree, by construction: the
// instrumenter flattens every mutated copy onto the line it came from and
// leaves the original bytes byte-identical in the guard's `else` branch, so
// line N of the instrumented file is line N of the user's file. That promise is
// stated in internal/instrument's package documentation and held in place by
// its assertLinesUntouched helper, whose own comment names a coverage record as
// one of the things depending on it. This package depends on it and would be
// wrong without it.
//
// Using lines alone over-approximates: two mutants on one line share a verdict,
// and a mutant on a line that a covered block merely touches is treated as
// covered. That is the safe direction. Over-approximating coverage costs time —
// a mutant is executed that could not have been killed — while
// under-approximating costs a *kill*, which is a silently inflated score.
//
// # What "covered" means
//
// A test binary B covers a mutant M when B's profile holds at least one block
// with a non-zero count whose line interval overlaps M's line interval. Three
// consequences are worth stating, because each one is a decision:
//
//   - A file that appears in B's profile with every count at zero is *not*
//     covered by B. The profile said the statements exist and were never
//     reached, which is exactly the fact coverage-guided selection is for.
//   - A file that does not appear in B's profile at all is not covered by B
//     either. With `-coverpkg=<module>/...` a package's blocks appear in a
//     binary's profile when the binary links that package, so absence means B
//     never linked it — see the counter-example in the package tests, where the
//     `user` package is absent from the `core` test binary's profile.
//   - A mutant covered by no binary at all is not executed. It is reported as a
//     survivor with `uncovered` set, because that is what it is: no test in the
//     workspace runs the line, so no test could have caught the edit.
//
// The blanket case — coverage could not be collected or could not be read — is
// not this package's to decide. It hands back what it found and internal/engine
// fails open, running every mutant against every binary; see
// [CodeUnavailable].
package coverage
