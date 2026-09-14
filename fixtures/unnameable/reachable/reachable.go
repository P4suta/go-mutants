// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package reachable is hidden's opposite, and the fixture needs both.
//
// Its named numeric type is *exported*, so any file that imports this package
// can write it down. What decides whether a guard may use it is therefore not
// the type at all but the reach: whether the file being rewritten has a name
// for this package, or can be given one. See package split.
package reachable

// Extent is the exported named numeric type. Arithmetic over two of them has
// this type, which is the shape that makes a `switch` tag unwritable by a file
// with no name for this package.
type Extent int

// Of makes one.
func Of(n int) Extent { return Extent(n) }

// Count reads one back as an ordinary int.
func Count(e Extent) int { return int(e) }
