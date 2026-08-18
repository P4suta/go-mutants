// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package interval_test

import (
	// The standard cmp is aliased because go-cmp claims the plain name in
	// every test file of this package.
	stdcmp "cmp"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"

	"github.com/P4suta/go-mutants/internal/interval"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// The generators stay inside a small coordinate space on purpose: with starts
// in [0,40] and lengths in [0,20], a dozen spans reliably produce every
// relation that matters — identical spans, deep nesting, shared boundaries,
// partial overlap and the empty spans Build refuses — instead of a scattering
// of disjoint ranges that would exercise one rule. The bounds also keep
// Start+Len far below the uint32 ceiling, so no case is an overflow in
// disguise.
func spanGen() *rapid.Generator[mutation.Span] {
	return rapid.Custom(func(t *rapid.T) mutation.Span {
		start := rapid.Uint32Range(0, 40).Draw(t, "start")
		length := rapid.Uint32Range(0, 20).Draw(t, "length")
		return mutation.Span{StartByte: start, EndByte: start + length}
	})
}

// itemsGen numbers the payloads by insertion index, which is what lets the
// invariants below tell every item apart even when spans coincide.
func itemsGen() *rapid.Generator[[]interval.Item[int]] {
	return rapid.Custom(func(t *rapid.T) []interval.Item[int] {
		spans := rapid.SliceOfN(spanGen(), 0, 12).Draw(t, "spans")
		items := make([]interval.Item[int], len(spans))
		for i, s := range spans {
			items[i] = interval.Item[int]{Span: s, Payload: i}
		}
		return items
	})
}

// TestForestInvariants asserts the properties the instrumenter depends on for
// any set of spans at all, rather than the handful the tables enumerate.
func TestForestInvariants(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		items := itemsGen().Draw(rt, "items")
		forest, conflicts := interval.Build(items)

		nodes, parents := flatten(forest)

		checkAccounting(rt, items, nodes, conflicts)
		checkContainment(rt, forest, nodes, parents)
		checkConflicts(rt, nodes, conflicts)
		checkInnerFirst(rt, forest, nodes)
		checkWalk(rt, forest, nodes, parents)
	})
}

// TestBuildIsOrderIndependent is the determinism property: mutant identities
// are hashed from these spans, so the same candidates discovered in a different
// order must produce the same forest. The one documented exception is the order
// of a node's alternatives (and, for identical spans, of their conflicts),
// which is canonicalised away by payload before comparing.
func TestBuildIsOrderIndependent(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		items := itemsGen().Draw(rt, "items")
		shuffled := rapid.Permutation(items).Draw(rt, "shuffled")

		forest, conflicts := interval.Build(items)
		otherForest, otherConflicts := interval.Build(shuffled)

		if diff := cmp.Diff(canonicalRoots(forest), canonicalRoots(otherForest)); diff != "" {
			rt.Fatalf("forest depends on insertion order (-original +shuffled):\n%s", diff)
		}
		if diff := cmp.Diff(canonicalConflicts(conflicts), canonicalConflicts(otherConflicts)); diff != "" {
			rt.Fatalf("conflicts depend on insertion order (-original +shuffled):\n%s", diff)
		}
	})
}

// flatten walks the forest through the exported fields alone, so that the
// traversal methods can be checked against it instead of against themselves.
func flatten(forest interval.Forest[int]) (nodes []*interval.Node[int], parents map[*interval.Node[int]]*interval.Node[int]) {
	parents = make(map[*interval.Node[int]]*interval.Node[int])

	var descend func(level []*interval.Node[int], parent *interval.Node[int])
	descend = func(level []*interval.Node[int], parent *interval.Node[int]) {
		for _, n := range level {
			nodes = append(nodes, n)
			parents[n] = parent
			descend(n.Children, n)
		}
	}
	descend(forest.Roots(), nil)

	return nodes, parents
}

// checkAccounting is the promise that nothing is lost: every item shows up
// exactly once, either as an alternative on the node for its own span or as a
// conflict carrying the item unchanged.
func checkAccounting(rt *rapid.T, items []interval.Item[int], nodes []*interval.Node[int], conflicts []interval.Conflict[int]) {
	rt.Helper()

	seen := make([]int, len(items))
	count := func(payload int) {
		if payload < 0 || payload >= len(items) {
			rt.Fatalf("payload %d is not one of the %d items", payload, len(items))
		}
		seen[payload]++
	}

	for _, n := range nodes {
		if n.Span.IsEmpty() {
			rt.Fatalf("node %v covers no bytes", n.Span)
		}
		if len(n.Alternatives) == 0 {
			rt.Fatalf("node %v has no alternatives", n.Span)
		}
		for _, payload := range n.Alternatives {
			count(payload)
			if got := items[payload].Span; got != n.Span {
				rt.Fatalf("item %d has span %v but sits on node %v", payload, got, n.Span)
			}
		}
	}

	for _, c := range conflicts {
		count(c.Item.Payload)
		if got := items[c.Item.Payload]; got != c.Item {
			rt.Fatalf("conflict carries %+v, want the original item %+v", c.Item, got)
		}
	}

	for payload, n := range seen {
		if n != 1 {
			rt.Fatalf("item %d (%v) appears %d times in forest+conflicts, want exactly 1",
				payload, items[payload].Span, n)
		}
	}
}

// checkContainment covers the three relations that survive into the forest:
// children are contained by their parent, siblings are disjoint, and — the
// "smallest enclosing span" rule — a node that contains another is always its
// ancestor, never a cousin.
func checkContainment(rt *rapid.T, forest interval.Forest[int], nodes []*interval.Node[int], parents map[*interval.Node[int]]*interval.Node[int]) {
	rt.Helper()

	checkSiblings(rt, forest.Roots())
	for _, n := range nodes {
		checkSiblings(rt, n.Children)
		for _, child := range n.Children {
			if got := interval.Relate(n.Span, child.Span); got != interval.Contains {
				rt.Fatalf("parent %v vs child %v: %s, want contains", n.Span, child.Span, got)
			}
		}
	}

	ancestor := func(a, b *interval.Node[int]) bool {
		for at := parents[b]; at != nil; at = parents[at] {
			if at == a {
				return true
			}
		}
		return false
	}
	for _, a := range nodes {
		for _, b := range nodes {
			if a == b {
				continue
			}
			switch interval.Relate(a.Span, b.Span) {
			case interval.Contains:
				if !ancestor(a, b) {
					rt.Fatalf("%v contains %v but is not its ancestor: the smaller span "+
						"did not attach to the smallest span enclosing it", a.Span, b.Span)
				}
			case interval.PartialOverlap:
				rt.Fatalf("nodes %v and %v partially overlap", a.Span, b.Span)
			case interval.Identical:
				rt.Fatalf("spans %v and %v are identical but did not share a node", a.Span, b.Span)
			case interval.Disjoint, interval.ContainedBy:
			}
		}
	}
}

func checkSiblings(rt *rapid.T, siblings []*interval.Node[int]) {
	rt.Helper()

	for i, a := range siblings {
		for _, b := range siblings[i+1:] {
			if got := interval.Relate(a.Span, b.Span); got != interval.Disjoint {
				rt.Fatalf("siblings %v and %v: %s, want disjoint", a.Span, b.Span, got)
			}
		}
		if i > 0 && siblings[i-1].Span.StartByte >= a.Span.StartByte {
			rt.Fatalf("siblings %v and %v are not ordered by start offset", siblings[i-1].Span, a.Span)
		}
	}
}

// checkConflicts pins the fourth relation: an evicted item either covers no
// bytes or genuinely straddles the boundary of a site that is in the forest.
func checkConflicts(rt *rapid.T, nodes []*interval.Node[int], conflicts []interval.Conflict[int]) {
	rt.Helper()

	for _, c := range conflicts {
		switch c.Reason {
		case interval.ReasonEmptySpan:
			if !c.Item.Span.IsEmpty() {
				rt.Fatalf("item %v evicted as empty but covers %d bytes", c.Item.Span, c.Item.Span.Len())
			}
			if c.Against != (mutation.Span{}) {
				rt.Fatalf("empty-span conflict names %v as the span it overlaps", c.Against)
			}
		case interval.ReasonPartialOverlap:
			if got := interval.Relate(c.Against, c.Item.Span); got != interval.PartialOverlap {
				rt.Fatalf("conflict %v against %v: %s, want partial-overlap", c.Item.Span, c.Against, got)
			}
			if !slices.ContainsFunc(nodes, func(n *interval.Node[int]) bool { return n.Span == c.Against }) {
				rt.Fatalf("conflict names %v, which is not a site in the forest", c.Against)
			}
		default:
			rt.Fatalf("conflict for %v has unknown reason %q", c.Item.Span, c.Reason)
		}
	}
}

// checkInnerFirst asserts the guarantee the splicer rests on: a site is handed
// over only after everything nested inside it, and sites at one level arrive
// left to right.
func checkInnerFirst(rt *rapid.T, forest interval.Forest[int], nodes []*interval.Node[int]) {
	rt.Helper()

	order := visitOrder(rt, forest.InnerFirst, nodes)

	for _, n := range nodes {
		for _, child := range n.Children {
			if order[child] >= order[n] {
				rt.Fatalf("InnerFirst visited parent %v before child %v", n.Span, child.Span)
			}
		}
	}
	checkLeftToRight(rt, "InnerFirst", order, forest.Roots())
	for _, n := range nodes {
		checkLeftToRight(rt, "InnerFirst", order, n.Children)
	}
}

// checkWalk asserts the mirror image, which is what a nesting report wants.
func checkWalk(rt *rapid.T, forest interval.Forest[int], nodes []*interval.Node[int], parents map[*interval.Node[int]]*interval.Node[int]) {
	rt.Helper()

	order := visitOrder(rt, forest.Walk, nodes)

	for _, n := range nodes {
		if parent := parents[n]; parent != nil && order[parent] >= order[n] {
			rt.Fatalf("Walk visited child %v before parent %v", n.Span, parent.Span)
		}
	}
	checkLeftToRight(rt, "Walk", order, forest.Roots())
	for _, n := range nodes {
		checkLeftToRight(rt, "Walk", order, n.Children)
	}
}

// visitOrder runs a traversal and records when each node was reached, failing
// if the traversal skips a node or reaches one twice.
func visitOrder(rt *rapid.T, traverse func(func(*interval.Node[int])), nodes []*interval.Node[int]) map[*interval.Node[int]]int {
	rt.Helper()

	order := make(map[*interval.Node[int]]int, len(nodes))
	traverse(func(n *interval.Node[int]) {
		if _, dup := order[n]; dup {
			rt.Fatalf("traversal visited %v twice", n.Span)
		}
		order[n] = len(order)
	})
	if len(order) != len(nodes) {
		rt.Fatalf("traversal visited %d of %d nodes", len(order), len(nodes))
	}
	return order
}

func checkLeftToRight(rt *rapid.T, traversal string, order map[*interval.Node[int]]int, siblings []*interval.Node[int]) {
	rt.Helper()

	for i := 1; i < len(siblings); i++ {
		if order[siblings[i-1]] >= order[siblings[i]] {
			rt.Fatalf("%s visited sibling %v before %v", traversal, siblings[i].Span, siblings[i-1].Span)
		}
	}
}

// canonicalNode is the forest reduced to what may not vary with insertion
// order: the shape, the spans, and the set — not the sequence — of payloads.
type canonicalNode struct {
	Span     mutation.Span
	Payloads []int
	Children []canonicalNode
}

func canonicalRoots(forest interval.Forest[int]) []canonicalNode {
	return canonicalLevel(forest.Roots())
}

func canonicalLevel(nodes []*interval.Node[int]) []canonicalNode {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]canonicalNode, len(nodes))
	for i, n := range nodes {
		payloads := slices.Clone(n.Alternatives)
		slices.Sort(payloads)
		out[i] = canonicalNode{Span: n.Span, Payloads: payloads, Children: canonicalLevel(n.Children)}
	}
	return out
}

func canonicalConflicts(conflicts []interval.Conflict[int]) []interval.Conflict[int] {
	sorted := slices.Clone(conflicts)
	slices.SortFunc(sorted, func(a, b interval.Conflict[int]) int {
		return stdcmp.Compare(a.Item.Payload, b.Item.Payload)
	})
	return sorted
}
