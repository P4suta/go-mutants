// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package split is the other half of this module's subject: an edit whose type
// this file cannot spell and a sibling file can.
//
// The module's other package is about a type nothing outside its own package
// can name, which no import fixes. This one is about a name, which one does:
// the package already imports reachable in the file below, so writing that
// import into the file beside it adds no edge the package did not have. That is
// the whole rule import completion follows, and this fixture is what proves the
// instrumented tree compiles under it.
package split

import "fixture.example/unnameable/reachable"

// scaled is why the file beside this one can hold a reachable.Extent without
// naming the package it comes from.
func scaled(n int) reachable.Extent { return reachable.Of(n) }

// Total is an ordinary candidate in the file that does the importing, so that
// the two files are not told apart by having mutants in only one of them.
func Total(a, b int) int { return reachable.Count(scaled(a)) + b }
