// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package interval composes overlapping byte spans into a forest of nested
// rewrite sites.
//
// The instrumenter discovers mutation candidates one operator at a time, so it
// ends up holding a bag of byte spans that relate to each other in every way
// two ranges can: two operators can propose different rewrites of the exact
// same bytes, one candidate can sit inside another (the condition of an `if`
// inside the statement that holds it), candidates can be unrelated, and — for
// a handful of operator pairs — they can straddle each other's boundary.
// Splicing that bag directly would produce garbage, so it is first turned into
// a forest by [Build]:
//
//   - identical spans become alternatives on one node (mutually exclusive
//     rewrites of the same bytes, emitted as a single else-if chain);
//   - a span nested inside another becomes a child of the smallest span that
//     encloses it;
//   - disjoint spans become siblings;
//   - partial overlap cannot be expressed by nesting at all, so the later of
//     the two items is evicted into the [Conflict] list rather than dropped.
//
// [Forest.InnerFirst] then hands the splicer its sites children-first, so that
// a site's replacement is composed from its children's finished text before the
// site enclosing it is rendered. Offsets still move under every splice — a
// nested rewrite shifts everything after it, the ends of its own ancestors
// included — which is why the splice runs through an offset map. The forest
// settles the order, not the arithmetic.
//
// The spans are [mutation.Span] — the same type the catalogue holds and the
// identity recipe hashes. This package deliberately does not define a span of
// its own: discovery mints spans, the forest arranges them, and the splicer
// applies them, so a private span type here would mean a conversion at both
// ends of that path and two places for an off-by-one to hide. The dependency
// runs one way only: internal/mutation knows nothing about the forest.
//
// Everything here is pure: no files, no AST, no toolchain. Build is a total
// function of the item multiset, and — apart from the documented insertion
// order of alternatives — its output does not depend on the order the caller
// happened to discover the items in. Mutant identities are hashed from these
// spans, so an order-dependent forest would mean order-dependent identities.
package interval

import (
	"fmt"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// Relation classifies how one span sits relative to another. Two non-empty
// spans always stand in exactly one of these relations, and [Build] has a rule
// for each: [Identical] spans become alternatives, [Contains] and [ContainedBy]
// become nesting, [Disjoint] becomes sibling placement and [PartialOverlap]
// becomes a [Conflict].
type Relation int

const (
	// Disjoint means the two spans share no byte.
	Disjoint Relation = iota
	// Identical means the two spans cover exactly the same bytes.
	Identical
	// Contains means the first span covers every byte of the second and more.
	// It is strict containment, unlike the reflexive [mutation.Span.Contains]:
	// a span that covers exactly the same bytes is [Identical] here, because
	// the forest does something else with it.
	Contains
	// ContainedBy is Contains seen from the other side.
	ContainedBy
	// PartialOverlap means the spans share bytes while neither encloses the
	// other. Nesting cannot express this, which is why it is the only relation
	// that costs an item its place in the forest.
	PartialOverlap
)

// String returns the relation's name, so failed assertions read as prose.
func (r Relation) String() string {
	switch r {
	case Disjoint:
		return "disjoint"
	case Identical:
		return "identical"
	case Contains:
		return "contains"
	case ContainedBy:
		return "contained-by"
	case PartialOverlap:
		return "partial-overlap"
	default:
		return fmt.Sprintf("Relation(%d)", int(r))
	}
}

// Relate classifies a against b, in terms of the span predicates the catalogue
// already defines: this is a naming of the five cases, not a second
// implementation of the containment arithmetic.
//
// It is defined for non-empty spans, and that is not a hedge: an empty span
// sitting on a boundary is simultaneously enclosed by the range that ends there
// and disjoint from it, so no single classification is honest. [Build] declines
// to place empty spans for the same reason, which keeps this function and the
// forest it describes in agreement — see [ReasonEmptySpan] for what that means
// for an insertion point, which is a legal span but not a placeable site.
func Relate(a, b mutation.Span) Relation {
	switch {
	case a == b:
		return Identical
	case a.StrictlyContains(b):
		return Contains
	case b.StrictlyContains(a):
		return ContainedBy
	case !a.Overlaps(b):
		return Disjoint
	default:
		return PartialOverlap
	}
}
