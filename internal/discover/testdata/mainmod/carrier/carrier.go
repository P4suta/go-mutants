// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package carrier exports a named numeric type, so that a file which does not
// import it can still hold expressions of it -- by way of a helper in a sibling
// file that does.
package carrier

import "example.com/mini/deeper"

// Extent is the exported named numeric type. Arithmetic over two of them has
// this type, which is the one shape where an edit's own expression and every
// expression enclosing it needs this package named.
type Extent int

// Box carries one.
type Box struct{ n int }

// Make returns a Box.
func Make(n int) Box { return Box{n: n} }

// Size is the Extent of a Box.
func (b Box) Size() Extent { return Extent(b.n) }

// Count reads an Extent back as an ordinary int.
func Count(e Extent) int { return int(e) }

// Deep reaches one package further, and returns a type this module's split
// package has no path to at all.
func Deep(n int) deeper.Thing { return deeper.Of(n) }
