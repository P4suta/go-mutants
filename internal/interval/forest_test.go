// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package interval_test

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/P4suta/go-mutants/internal/interval"
)

// Shorthands that keep the expected forests in these tables readable as trees.
type (
	item     = interval.Item[string]
	node     = interval.Node[string]
	conflict = interval.Conflict[string]
)

// TestBuild is organised around the four relations two spans can stand in,
// because those are exactly the four rules Build implements.
func TestBuild(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		items         []item
		wantRoots     []*node
		wantConflicts []conflict
	}{
		{
			name:  "no items yield an empty forest",
			items: nil,
		},
		{
			name: "disjoint spans become siblings ordered by start offset",
			items: []item{
				{Span: span(20, 25), Payload: "third"},
				{Span: span(0, 5), Payload: "first"},
				{Span: span(10, 15), Payload: "second"},
			},
			wantRoots: []*node{
				{Span: span(0, 5), Alternatives: []string{"first"}},
				{Span: span(10, 15), Alternatives: []string{"second"}},
				{Span: span(20, 25), Alternatives: []string{"third"}},
			},
		},
		{
			name: "spans that merely touch are disjoint, not nested",
			items: []item{
				{Span: span(0, 5), Payload: "left"},
				{Span: span(5, 9), Payload: "right"},
			},
			wantRoots: []*node{
				{Span: span(0, 5), Alternatives: []string{"left"}},
				{Span: span(5, 9), Alternatives: []string{"right"}},
			},
		},
		{
			name: "identical spans become alternatives in insertion order",
			items: []item{
				{Span: span(4, 9), Payload: "eq-to-neq"},
				{Span: span(20, 24), Payload: "true-to-false"},
				{Span: span(4, 9), Payload: "lt-to-le"},
				{Span: span(4, 9), Payload: "gt-to-ge"},
			},
			wantRoots: []*node{
				{Span: span(4, 9), Alternatives: []string{"eq-to-neq", "lt-to-le", "gt-to-ge"}},
				{Span: span(20, 24), Alternatives: []string{"true-to-false"}},
			},
		},
		{
			name: "a nested span attaches to the smallest enclosing span",
			items: []item{
				// Deliberately scrambled: the innermost candidate is discovered
				// first and the outermost last.
				{Span: span(4, 8), Payload: "inner"},
				{Span: span(22, 28), Payload: "tail"},
				{Span: span(2, 20), Payload: "mid"},
				{Span: span(0, 30), Payload: "outer"},
			},
			wantRoots: []*node{{
				Span:         span(0, 30),
				Alternatives: []string{"outer"},
				Children: []*node{
					{
						Span:         span(2, 20),
						Alternatives: []string{"mid"},
						Children:     []*node{{Span: span(4, 8), Alternatives: []string{"inner"}}},
					},
					{Span: span(22, 28), Alternatives: []string{"tail"}},
				},
			}},
		},
		{
			name: "nesting that shares a boundary is still nesting",
			items: []item{
				{Span: span(0, 10), Payload: "statement"},
				{Span: span(0, 4), Payload: "leading"},
				{Span: span(6, 10), Payload: "trailing"},
			},
			wantRoots: []*node{{
				Span:         span(0, 10),
				Alternatives: []string{"statement"},
				Children: []*node{
					{Span: span(0, 4), Alternatives: []string{"leading"}},
					{Span: span(6, 10), Alternatives: []string{"trailing"}},
				},
			}},
		},
		{
			name: "alternatives and children coexist on one site",
			items: []item{
				{Span: span(0, 10), Payload: "delete-statement"},
				{Span: span(2, 5), Payload: "add-to-sub"},
				{Span: span(0, 10), Payload: "delete-assignment"},
			},
			wantRoots: []*node{{
				Span:         span(0, 10),
				Alternatives: []string{"delete-statement", "delete-assignment"},
				Children:     []*node{{Span: span(2, 5), Alternatives: []string{"add-to-sub"}}},
			}},
		},
		{
			name: "partial overlap evicts the later span",
			items: []item{
				{Span: span(0, 10), Payload: "left"},
				{Span: span(5, 15), Payload: "right"},
			},
			wantRoots: []*node{{Span: span(0, 10), Alternatives: []string{"left"}}},
			wantConflicts: []conflict{{
				Item:    item{Span: span(5, 15), Payload: "right"},
				Reason:  interval.ReasonPartialOverlap,
				Against: span(0, 10),
			}},
		},
		{
			name: "the evicted span is the later one whichever order they arrive in",
			items: []item{
				{Span: span(5, 15), Payload: "right"},
				{Span: span(0, 10), Payload: "left"},
			},
			wantRoots: []*node{{Span: span(0, 10), Alternatives: []string{"left"}}},
			wantConflicts: []conflict{{
				Item:    item{Span: span(5, 15), Payload: "right"},
				Reason:  interval.ReasonPartialOverlap,
				Against: span(0, 10),
			}},
		},
		{
			name: "overlap is reported against the innermost site it crosses",
			items: []item{
				{Span: span(0, 30), Payload: "outer"},
				{Span: span(4, 12), Payload: "inner"},
				{Span: span(8, 20), Payload: "straddler"},
			},
			wantRoots: []*node{{
				Span:         span(0, 30),
				Alternatives: []string{"outer"},
				Children:     []*node{{Span: span(4, 12), Alternatives: []string{"inner"}}},
			}},
			wantConflicts: []conflict{{
				Item:    item{Span: span(8, 20), Payload: "straddler"},
				Reason:  interval.ReasonPartialOverlap,
				Against: span(4, 12),
			}},
		},
		{
			name: "a span nested inside an evicted span still finds its place",
			items: []item{
				{Span: span(0, 10), Payload: "kept"},
				{Span: span(5, 15), Payload: "evicted"},
				{Span: span(6, 9), Payload: "nested"},
			},
			wantRoots: []*node{{
				Span:         span(0, 10),
				Alternatives: []string{"kept"},
				Children:     []*node{{Span: span(6, 9), Alternatives: []string{"nested"}}},
			}},
			wantConflicts: []conflict{{
				Item:    item{Span: span(5, 15), Payload: "evicted"},
				Reason:  interval.ReasonPartialOverlap,
				Against: span(0, 10),
			}},
		},
		{
			name: "spans covering no bytes are evicted rather than placed",
			items: []item{
				{Span: span(0, 10), Payload: "real"},
				{Span: span(4, 4), Payload: "collapsed"},
				{Span: span(9, 2), Payload: "malformed"},
			},
			wantRoots: []*node{{Span: span(0, 10), Alternatives: []string{"real"}}},
			wantConflicts: []conflict{
				{Item: item{Span: span(4, 4), Payload: "collapsed"}, Reason: interval.ReasonEmptySpan},
				{Item: item{Span: span(9, 2), Payload: "malformed"}, Reason: interval.ReasonEmptySpan},
			},
		},
		{
			name: "conflicts are reported in canonical span order",
			items: []item{
				{Span: span(30, 45), Payload: "late"},
				{Span: span(0, 20), Payload: "first"},
				{Span: span(25, 40), Payload: "early"},
				{Span: span(10, 25), Payload: "middle"},
			},
			wantRoots: []*node{
				{Span: span(0, 20), Alternatives: []string{"first"}},
				{Span: span(25, 40), Alternatives: []string{"early"}},
			},
			wantConflicts: []conflict{
				{
					Item:    item{Span: span(10, 25), Payload: "middle"},
					Reason:  interval.ReasonPartialOverlap,
					Against: span(0, 20),
				},
				{
					Item:    item{Span: span(30, 45), Payload: "late"},
					Reason:  interval.ReasonPartialOverlap,
					Against: span(25, 40),
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			forest, conflicts := interval.Build(tc.items)

			if diff := cmp.Diff(tc.wantRoots, forest.Roots(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("forest mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantConflicts, conflicts, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("conflicts mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestBuildDoesNotMutateInput pins that Build sorts a private index vector: the
// caller's slice is its own record of what was discovered, and reordering it
// under the caller would make the documented alternative order meaningless.
func TestBuildDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	items := []item{
		{Span: span(20, 25), Payload: "c"},
		{Span: span(0, 30), Payload: "a"},
		{Span: span(0, 30), Payload: "b"},
		{Span: span(24, 40), Payload: "d"},
	}
	want := append([]item(nil), items...)

	interval.Build(items)

	if diff := cmp.Diff(want, items); diff != "" {
		t.Errorf("Build modified its input (-before +after):\n%s", diff)
	}
}

// TestAlternativesKeepInsertionOrderAtScale pins the one guarantee that is not
// a function of the spans alone. A handful of alternatives would pass even if
// the canonical order fell back on the sort being stable, because Go's sort
// switches to insertion sort for short runs; at this size an unordered
// comparison of equal spans really does shuffle them.
func TestAlternativesKeepInsertionOrderAtScale(t *testing.T) {
	t.Parallel()

	const alternatives = 200

	var (
		items []item
		want  []string
	)
	for i := range alternatives {
		payload := fmt.Sprintf("rule-%03d", i)
		// Interleave an unrelated site so the identical spans are not simply
		// one contiguous input run either.
		items = append(items,
			item{Span: span(0, 8), Payload: payload},
			item{Span: span(uint32(20+2*i), uint32(21+2*i)), Payload: "other"},
		)
		want = append(want, payload)
	}

	forest, conflicts := interval.Build(items)

	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", conflicts)
	}
	roots := forest.Roots()
	if len(roots) != alternatives+1 {
		t.Fatalf("got %d roots, want %d", len(roots), alternatives+1)
	}
	if diff := cmp.Diff(want, roots[0].Alternatives); diff != "" {
		t.Errorf("alternatives lost their insertion order (-want +got):\n%s", diff)
	}
}

// nestedForest is the fixture the traversal tests share:
//
//	[0,30) outer
//	  [2,10) left
//	    [4,6) leaf
//	  [12,20) right
//	[40,50) tail
func nestedForest(t *testing.T) interval.Forest[string] {
	t.Helper()

	forest, conflicts := interval.Build([]item{
		{Span: span(4, 6), Payload: "leaf"},
		{Span: span(40, 50), Payload: "tail"},
		{Span: span(0, 30), Payload: "outer"},
		{Span: span(12, 20), Payload: "right"},
		{Span: span(2, 10), Payload: "left"},
	})
	if len(conflicts) != 0 {
		t.Fatalf("fixture produced conflicts: %v", conflicts)
	}
	return forest
}

func TestInnerFirstVisitsChildrenBeforeParents(t *testing.T) {
	t.Parallel()

	var got []string
	nestedForest(t).InnerFirst(func(n *node) {
		got = append(got, n.Alternatives[0])
	})

	want := []string{"leaf", "left", "right", "outer", "tail"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("InnerFirst order mismatch (-want +got):\n%s", diff)
	}
}

func TestWalkVisitsParentsBeforeChildren(t *testing.T) {
	t.Parallel()

	var got []string
	nestedForest(t).Walk(func(n *node) {
		got = append(got, n.Alternatives[0])
	})

	want := []string{"outer", "left", "leaf", "right", "tail"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Walk order mismatch (-want +got):\n%s", diff)
	}
}

// TestZeroForest guards the documented zero value: an instrumenter that found
// nothing to rewrite still traverses its (empty) forest.
func TestZeroForest(t *testing.T) {
	t.Parallel()

	var forest interval.Forest[string]

	if got := forest.Roots(); len(got) != 0 {
		t.Errorf("Roots() = %v, want empty", got)
	}
	forest.InnerFirst(func(n *node) { t.Errorf("InnerFirst visited %v", n.Span) })
	forest.Walk(func(n *node) { t.Errorf("Walk visited %v", n.Span) })
}
