// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package unnameable holds the refusal the reserved skip reason was named for:
// a site whose declared type cannot be written down in this file.
//
// The fixture is two sites in one line of code, and they are two sites on
// purpose. The addition is the one discovery has to *decline* — guarding it
// means declaring a temporary of the type `c` is given, and `counter` is not
// exported, so there is no source form of that declaration and none is invented
// by adding an import. The call on the next line is an ordinary candidate that
// is catalogued, executed and killed. Without the second there would be nothing
// to prove the pass carried on; without the first there would be no skip.
package unnameable

import "fixture.example/unnameable/hidden"

// Counted adds two numbers through a value whose type this file cannot spell.
func Counted(a, b int) int {
	c := hidden.New(a + b)
	return c.Value()
}
