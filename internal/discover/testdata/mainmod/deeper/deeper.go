// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package deeper exists to be two packages away. Nothing in package split
// imports it, and nothing in package split can: import completion adds a path
// one of a package's own files already has, and no file of split has this one.
package deeper

// Thing is exported, numeric, and therefore perfectly nameable by any file that
// imports this package. That is what makes it the right refusal to pin: the
// type is not the problem, the reach is.
type Thing int

// Of makes one.
func Of(n int) Thing { return Thing(n) }

// Count reads one back.
func Count(t Thing) int { return int(t) }
