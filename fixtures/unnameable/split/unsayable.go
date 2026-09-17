// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package split

// Widest holds the edit import completion exists for.
//
// `scaled(a) + scaled(b)` has type reachable.Extent, and this file has no name
// for that package. Every other escape is closed exactly as in [Unsayable]: a
// `switch` tag has no statement around it for a statement form to stand in, and
// its type is not boolean, so neither boolean selector has anything to select.
// What is left is the form that writes the type out — and the only reason it
// could not was the missing name. The file beside this one has it, so the
// rewrite is given it, and this is the mutant that proves the result compiles.
//
// The `case` is what makes that mutant worth having. A `switch` whose only
// clause is `default` takes the same branch whatever its tag evaluates to, so
// the edit would be equivalent and the fixture would report a survivor where it
// means to report a kill.
func Widest(a, b int) int {
	switch scaled(a) + scaled(b) {
	case scaled(5):
		return a
	default:
		return b
	}
}
