// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package split

// Widest is the site import completion exists for.
//
// `boxed(a).Size() + boxed(b).Size()` has type carrier.Extent, and this file
// does not import carrier. Every escape is closed deliberately, exactly as in
// package unnameable: a switch tag has no statement around it for Form S, Form
// D or Form F to stand in, and its type is not boolean, so Form C and Form C'
// have nothing to select. What is left is Form E, which has to write the type
// out -- and the only reason it could not was that this file has no name for
// the package. Its sibling does, so the edit lives and the import is added.
func Widest(a, b int) int {
	switch boxed(a).Size() + boxed(b).Size() {
	default:
		return a
	}
}

// Deepest is the refusal that is left, and it is the rule's own boundary.
//
// The type here is deeper.Thing: exported, numeric, and nameable by any file
// that imports its package. No file of this package does, and completion never
// invents an edge the package does not already have -- so this stays
// SkipUnnameableDeclType while the function above it does not.
func Deepest(a, b int) int {
	switch deepest(a) + deepest(b) {
	default:
		return a
	}
}
