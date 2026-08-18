// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package interval

import (
	"cmp"
	"slices"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// Item is one candidate rewrite: the bytes it replaces, plus whatever the
// caller needs to recognise it again. The payload is opaque here — the forest
// never inspects it, it only carries it to the node or conflict it lands in.
type Item[T any] struct {
	// Span is the byte range this candidate rewrites.
	Span mutation.Span
	// Payload identifies the candidate to the caller.
	Payload T
}

// Node is one rewrite site: a byte range plus every candidate that rewrites
// exactly that range.
//
// A node is read-only once [Build] returns. Its fields are exported so that
// tests and reporting can walk it like any other tree, not as an invitation to
// edit it; mutating a node breaks the invariants the traversals rely on.
type Node[T any] struct {
	// Span is the byte range this site rewrites.
	Span mutation.Span

	// Alternatives holds the payload of every item with exactly this span, in
	// the order the caller supplied them. They are mutually exclusive rewrites
	// of the same bytes: the instrumenter emits them as one guard chain, so at
	// most one is ever live.
	//
	// The insertion order is the single documented place where the caller's
	// ordering survives into the result. Everything else about the forest is a
	// function of the spans alone.
	Alternatives []T

	// Children are the sites nested strictly inside this one, ordered by start
	// offset. They are pairwise disjoint, and each is enclosed by this node
	// rather than merely by some ancestor: a child always hangs off the
	// smallest site that encloses it.
	Children []*Node[T]
}

// Forest is a set of rewrite sites arranged by containment. The zero value is
// an empty forest and is safe to traverse.
type Forest[T any] struct {
	roots []*Node[T]
}

// Roots returns the outermost sites, ordered by start offset and pairwise
// disjoint. The slice is the forest's own storage; callers must not modify it.
func (f Forest[T]) Roots() []*Node[T] { return f.roots }

// Reason names why an item could not be placed in the forest. The values are
// kebab-case strings because they are surfaced verbatim as skip reasons in the
// run report, next to the reasons the discovery phase produces.
type Reason string

const (
	// ReasonPartialOverlap means the item straddles the boundary of a site
	// already in the forest. Nesting cannot represent that, and rewriting both
	// would splice into each other's bytes.
	ReasonPartialOverlap Reason = "partial-overlap"

	// ReasonEmptySpan means the item covers no bytes (EndByte at or before
	// StartByte). There is nothing to rewrite, and an empty span cannot be
	// placed unambiguously against the site boundaries it touches: [3,3) is at
	// once enclosed by an open [3,5) and disjoint from it.
	//
	// This is a statement about rewrite sites, not about spans. An empty span
	// is a legal catalogue span — [mutation.NewSpan] accepts one as an
	// insertion point — and the forest is precisely the structure that cannot
	// hold it, so Build reports it here instead of dropping it or guessing at a
	// nesting. A caller that means to splice at an insertion point has to place
	// it itself, outside the forest; a caller that produced one by accident
	// gets a named reason to print.
	ReasonEmptySpan Reason = "empty-span"
)

// Conflict is an item [Build] refused to place, together with why. Nothing is
// ever dropped silently: every input item ends up either in the forest or here,
// so the caller can report each rejected candidate with its own diagnostic.
type Conflict[T any] struct {
	// Item is the evicted candidate, exactly as supplied.
	Item Item[T]
	// Reason is why it was evicted.
	Reason Reason
	// Against is the innermost forest span the item partially overlaps. It is
	// the zero Span when Reason is not ReasonPartialOverlap.
	Against mutation.Span
}

// Build arranges items into a forest of nested rewrite sites and returns the
// items it could not place.
//
// The four span relations are resolved as follows:
//
//   - identical spans collapse into one node, their payloads becoming that
//     node's alternatives in insertion order;
//   - a span enclosed by others becomes a child of the smallest such span;
//   - disjoint spans become siblings under their nearest common enclosing site,
//     ordered by start offset;
//   - partial overlap is unrepresentable, so the later of the two items — later
//     in the canonical order below, not in the caller's slice — is evicted with
//     [ReasonPartialOverlap]. Evicting the later one is what makes the outcome
//     independent of the order items were discovered in.
//
// Items are considered in a canonical order: start ascending, then end
// descending so an enclosing span is always seen before what it encloses, then
// insertion index. The returned conflicts follow that same order. The input
// slice is not modified.
//
// The result is a pure function of the item multiset, except that identical
// spans keep the caller's relative order within Alternatives.
func Build[T any](items []Item[T]) (Forest[T], []Conflict[T]) {
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(x, y int) int {
		a, b := items[x].Span, items[y].Span
		if c := cmp.Compare(a.StartByte, b.StartByte); c != 0 {
			return c
		}
		// End descending: an enclosing span must be visited before the spans it
		// encloses, and this is also what makes identical spans land next to
		// each other, so every group of alternatives is contiguous.
		if c := cmp.Compare(b.EndByte, a.EndByte); c != 0 {
			return c
		}
		return cmp.Compare(x, y)
	})

	var (
		forest    Forest[T]
		conflicts []Conflict[T]
		// stack holds the chain of sites still open at the sweep position,
		// outermost first, so its top is always the smallest enclosing site.
		stack []*Node[T]
	)

	for _, i := range order {
		item := items[i]
		if item.Span.IsEmpty() {
			conflicts = append(conflicts, Conflict[T]{Item: item, Reason: ReasonEmptySpan})
			continue
		}

		// Close every site that ends at or before this span starts. Because the
		// sweep runs in start order, every open site starts at or before this
		// span, so this single test is exactly disjointness — but only for
		// non-empty spans, which is precisely why the empty ones were rejected
		// above. For an empty [3,3) against an open [3,5) it would fire and
		// wrongly hoist the empty span out of a site that encloses it.
		for len(stack) > 0 && stack[len(stack)-1].Span.EndByte <= item.Span.StartByte {
			stack = stack[:len(stack)-1]
		}

		if len(stack) > 0 {
			enclosing := stack[len(stack)-1]
			if item.Span.EndByte > enclosing.Span.EndByte {
				// The spans share bytes, and this one runs past the end of the
				// enclosing site. It cannot enclose that site in turn: equal
				// starts are ordered by descending end, so an open site with
				// this same start would already reach at least this far. The
				// starts therefore differ and this is a partial overlap.
				conflicts = append(conflicts, Conflict[T]{
					Item:    item,
					Reason:  ReasonPartialOverlap,
					Against: enclosing.Span,
				})
				continue
			}
			if enclosing.Span == item.Span {
				enclosing.Alternatives = append(enclosing.Alternatives, item.Payload)
				continue
			}
		}

		node := &Node[T]{Span: item.Span, Alternatives: []T{item.Payload}}
		if len(stack) == 0 {
			forest.roots = append(forest.roots, node)
		} else {
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, node)
		}
		stack = append(stack, node)
	}

	return forest, conflicts
}

// InnerFirst visits every node children before parents, and left to right by
// start offset within each level.
//
// This is the order the splicer needs: a site's replacement is composed from
// its own original bytes with each child's already-rendered text substituted
// in, so no site can be rendered before everything nested inside it has been.
// Left to right within a level is what lets that composition walk the children
// in a single pass, carrying the running length delta forward.
//
// The order buys nothing about offsets, and the splicer must not assume it
// does. Rewriting a nested site changes the length of the bytes it covers, so
// every offset past it moves — its right-hand siblings and the end of every
// site enclosing it included. Splicing through an offset map (equivalently:
// composing bottom-up in parent-relative coordinates) is what keeps a span
// pointing at the bytes it was minted from. The forest settles the order, not
// the arithmetic.
func (f Forest[T]) InnerFirst(visit func(node *Node[T])) {
	type frame struct {
		node *Node[T]
		next int
	}
	// Iterative rather than recursive: nesting depth comes from the analysed
	// source, and this way pathological input costs memory instead of the
	// goroutine stack.
	stack := make([]frame, 0, 8)
	for _, root := range f.roots {
		stack = append(stack, frame{node: root})
		for len(stack) > 0 {
			top := len(stack) - 1
			if stack[top].next < len(stack[top].node.Children) {
				child := stack[top].node.Children[stack[top].next]
				stack[top].next++
				stack = append(stack, frame{node: child})
				continue
			}
			visit(stack[top].node)
			stack = stack[:top]
		}
	}
}

// Walk visits every node parents before children, and left to right by start
// offset within each level. It is the reporting counterpart to
// [Forest.InnerFirst]: outermost site first is how the nesting reads to a
// human, and it is the order an indented dump wants.
func (f Forest[T]) Walk(visit func(node *Node[T])) {
	stack := make([]*Node[T], 0, 8)
	push := func(nodes []*Node[T]) {
		for i := len(nodes) - 1; i >= 0; i-- {
			stack = append(stack, nodes[i])
		}
	}
	push(f.roots)
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		visit(node)
		push(node.Children)
	}
}
