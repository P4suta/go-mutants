// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package memorybound is the fixture the memory bound is proved against.
//
// It is `runaway` with one number changed, and that number is the whole reason
// it exists. Both fixtures hold a loop whose condition, negated, is true for
// exactly the input the original ran zero times for, so the mutant never
// returns. What separates them is how much memory one turn of the loop costs.
//
// `runaway` appends an `int`: eight bytes a turn. It was written when a mutant
// that does not return was stopped by a budget — the timeout first, then the
// memory bound — and eight bytes a turn reaches a bound in the time either of
// those allows. It no longer gets there. [ADR 0013] settles a runaway by
// counting its work instead, and the ceiling is `max(1<<20, observed × 1024)`,
// so a loop the census saw go round three times is stopped after a little over
// a million turns. A million turns of eight bytes is eight megabytes, and the
// bound the memory tests set is two hundred and fifty-six.
//
// So `runaway` proves what it says it proves and no longer proves the bound:
// the ceiling settles it first, in about a tenth of a second, and the memory
// tests that pointed at it saw zero mutants killed by the bound. That is the
// product being right — counting is cheaper and faster than waiting, which is
// what ADR 0013 argues — and a fixture that had stopped being about what its
// own documentation said it was about.
//
// A megabyte a turn crosses two hundred and fifty-six megabytes in two hundred
// and fifty-six turns, four thousand times inside the smallest ceiling this
// mechanism ever sets. The bound is reached because it is reached first, not
// because anything was turned off.
//
// The bytes are written and not merely asked for. Resident memory is what the
// sampler reads, and a large `make` can come back as pages the kernel has
// promised and not yet handed over, so a fixture that allocated without writing
// would grow a heap the bound never sees. [bytes.Repeat] writes every byte, and
// it does it inside the standard library — which is not instrumented, so the
// write does not become a second counted loop with a ceiling of its own.
//
// The two controls are `runaway`'s, for `runaway`'s reasons: `gt-to-ge` runs one
// turn too many and an assertion catches it, and `return-nil` is settled by the
// rows that count something. Together they are what makes "the runaway was
// killed by the bound" distinguishable from "the suite went red for another
// reason".
//
// [ADR 0013]: https://github.com/P4suta/go-mutants/blob/main/docs/adr/0013-a-mutant-that-does-not-return-is-decided-by-work.md
package memorybound

import "bytes"

// BlockSize is what one turn of the loop costs in resident bytes.
const BlockSize = 1 << 20

// Blocks returns n blocks of [BlockSize] bytes, each filled with its own index,
// and no blocks at all for a non-positive n.
func Blocks(n int) [][]byte {
	out := [][]byte{}
	remaining := n
	for remaining > 0 {
		out = append(out, bytes.Repeat([]byte{byte(remaining)}, BlockSize))
		remaining--
	}
	return out
}
