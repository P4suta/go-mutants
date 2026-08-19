// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package broken does not type-check. Discovery has to say so instead of
// quietly producing a smaller catalogue.
package broken

// Count returns a string from a function declared to return an int.
func Count() int {
	return "not an int"
}
