// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package core holds two mutants a probe pass has two different answers about,
// and that is the whole fixture.
//
// A probe tree records, per mutant, whether a test binary could have observed
// it -- whether the site's two readings ever disagreed while that binary ran.
// The two answers it can give are what a run acts on, and each needs a mutant
// of its own:
//
//   - [Gate]'s comparison is read differently by the two binaries. This
//     package's own tests pass the boundary value, where `<` and `<=` disagree,
//     so its binary names the mutant; the caller's tests never do, so its
//     binary does not. The mutant is covered by both and *narrowed* to one.
//   - [Same]'s multiplication is read identically by everything. `n * 1` and
//     `n / 1` are the same number for every int, so no binary can name it, and
//     the run reports it as a survivor without executing it at all.
//
// The second is a genuinely equivalent mutant, and that is not a weakness of
// the fixture. A mutant no test could ever distinguish is exactly the class a
// probe pass exists to stop paying for, and one contrived enough to be obvious
// is the only kind a reader can check by eye.
//
// No two functions share a rule, so a mutant can be named by rule alone.
package core

// Gate reports whether v is below lo.
//
// `lt-to-le` is the fixture's narrowed mutant. At v == lo the two readings
// disagree, and only this package's tests go there.
func Gate(v, lo int) bool {
	return v < lo
}

// Same returns n, the long way round.
//
// `mul-to-div` is the fixture's settled mutant: `n * 1` and `n / 1` are the
// same number for every int, so no execution of this line can tell them apart.
// The divisor is the constant 1, which is what lets the site be probed at all --
// a division by something that might be zero would panic in a tree meant to be
// the original program, and discovery refuses those.
func Same(n int) int {
	return n * 1
}
