// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package families

// Between reports whether v lies strictly inside the open range (lo, hi).
//
// KILLED. Four families meet in one `if`: the negation of the whole condition,
// the connective between its halves, both comparisons, and — in the two returns
// — both boolean literals.
//
// The range is open for the reason the killable fixture's Clamp takes an open
// one. With inclusive bounds `<` and `<=` agree at every input, the comparison
// mutants become equivalent ones, and no test can kill them; TestBetween's
// bound rows are the only inputs at which the swapped operators disagree, so
// deleting either of them turns a kill into a survivor.
func Between(v, lo, hi int) bool {
	if v > lo && v < hi {
		return true
	}
	return false
}

// Outside reports whether v lies at or beyond either end of the closed range
// [lo, hi].
//
// KILLED. It holds the other half of the comparison family, and the value it
// returns is an expression rather than a literal — which is what makes it a
// return-replacement site for both `true` and `false`. A `return true` would
// have offered `return false` as the same byte edit the boolean-literal family
// proposes, and deduplication would have kept only the more local rule.
func Outside(v, lo, hi int) bool {
	return v <= lo || v >= hi
}

// Missing reports whether a name is absent from the set.
//
// KILLED. The `!` is the third rule of the condition-negation family:
// `remove-negation` takes a negation away rather than adding one, so it is the
// only one of the three that needs a `!` already written in the source.
func Missing(names map[string]bool, name string) bool {
	return !names[name]
}

// Toggle inverts a flag, and is the fixture's deliberately under-tested
// condition.
//
// SURVIVES, all three of its mutants. TestToggle calls it with both inputs, so
// every line is covered and every mutant is really executed, and then asserts
// nothing at all about what came back. That is the commonest gap a mutation run
// finds in a real suite — a test that exercises rather than checks — and a run
// that did not report these three as survivors would not be measuring anything.
func Toggle(on bool) bool {
	if on {
		return false
	}
	return true
}
