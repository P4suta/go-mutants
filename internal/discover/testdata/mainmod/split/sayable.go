// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package split is the fixture for import completion: one file of it imports a
// package, another file of it needs a type from that package, and the two are
// the same package as far as Go is concerned.
//
// The distinction being pinned is between *reach* and *spelling*. A type from a
// package one of these files already imports can be written into the other one,
// because the package compiles with that edge today: no cycle is possible, no
// visibility changes, and go.mod needs nothing. A type from a package none of
// them imports stays a refusal, which is why [Deepest] is here beside
// [Widest].
package split

import "example.com/mini/carrier"

// boxed is the helper that lets the other file hold a carrier.Extent without
// naming this import.
func boxed(n int) carrier.Box { return carrier.Make(n) }

// deepest is the same trick one package further out, written without naming
// deeper: the variable takes the function's type by inference, so no file of
// this package imports that path and none can be made to.
var deepest = carrier.Deep

// Total is an ordinary site in the file that does the importing, so that the
// two files are not told apart by having candidates in only one of them.
func Total(a, b int) int { return carrier.Count(boxed(a).Size()) + b }
