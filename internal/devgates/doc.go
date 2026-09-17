// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package devgates holds the checks that are about the repository rather than
// about the product.
//
// A gate here asks whether a configuration file, a workflow or a ledger still
// says what it claims -- questions with no runtime behind them, whose subject
// is the tree a contributor edits. Nothing imports this package and it exports
// nothing; the tests are the whole of it, and `doc.go` exists so that the
// directory is a package rather than a directory that happens to hold Go.
//
// It is deliberately not internal/testkit. The harness is linked into every
// test binary in this repository and is held to the standard library plus
// [github.com/google/go-cmp] for exactly that reason, so a gate needing a
// decoder would either compile that decoder into two hundred binaries or be
// written to do without one -- and "written to do without one" is how a gate
// comes to read lines out of a file whose formatter is free to move them. Here
// a gate may decode what it is asked about, because nothing links it but
// itself.
package devgates
