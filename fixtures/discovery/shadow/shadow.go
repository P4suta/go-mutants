// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package shadow declares its own `true`. Go permits that: the predeclared
// booleans live in the universe scope, and any package or any block may put an
// identifier of that name in front of them.
//
// This is the one decision in the discovery phase that cannot be made from
// syntax at all. A walk that matched the identifier's spelling would rewrite a
// user's own constant into the literal `false` and call it a mutant; only the
// type checker knows which `true` is which. The universe `false` at the bottom
// is the control: it still becomes a candidate, so the check cannot pass by
// refusing everything in the package.
package shadow

// true is this package's own constant, and every mention of the name below
// resolves here rather than to the universe. It is an int on purpose — nothing
// about the spelling makes an identifier a boolean.
const true = 1

// Count reads the package's own constant, so the shadowing identifier is used
// as a value in exactly the position a candidate would be found in.
func Count() int { return true }

// Local shadows the name a second time inside a function body, where it is a
// variable rather than a constant at all.
func Local(n int) int {
	true := n + 1
	return true
}

// Untouched holds the universe `false`, which is still the predeclared
// constant here and is still mutated.
func Untouched() bool { return false }
