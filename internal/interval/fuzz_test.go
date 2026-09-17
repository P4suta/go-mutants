// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package interval_test

import (
	"testing"

	"github.com/P4suta/go-mutants/internal/interval"
	"github.com/P4suta/go-mutants/internal/mutation"
)

func FuzzBuild(f *testing.F) {
	f.Add([]byte{0, 10, 2, 8, 4, 6})
	f.Add([]byte{0, 4, 4, 8})
	f.Add([]byte{0, 4, 0, 4})
	f.Add([]byte{0, 8, 4, 12})
	f.Add([]byte{5, 5})
	f.Add([]byte{})
	f.Add([]byte{1})
	f.Add([]byte{0, 255, 0, 255, 0, 255})

	f.Fuzz(func(t *testing.T, encoded []byte) {
		items := itemsFrom(encoded)
		forest, conflicts := interval.Build(items)

		placed := map[int]int{}
		var walk func([]*interval.Node[int], mutation.Span, bool)
		walk = func(nodes []*interval.Node[int], parent mutation.Span, hasParent bool) {
			for _, node := range nodes {
				for _, payload := range node.Alternatives {
					placed[payload]++
				}
				if len(node.Alternatives) == 0 {
					t.Fatalf("a node at %v holds no alternatives, so it rewrites nothing", node.Span)
				}
				if hasParent && !contains(parent, node.Span) {
					t.Fatalf("a child span %v is not inside its parent %v", node.Span, parent)
				}
				walk(node.Children, node.Span, true)
			}
		}
		walk(forest.Roots(), mutation.Span{}, false)

		for _, conflict := range conflicts {
			placed[conflict.Item.Payload]++
		}
		if len(placed) != len(items) {
			t.Fatalf("Build was given %d items and accounted for %d", len(items), len(placed))
		}
		for payload, count := range placed {
			if count != 1 {
				t.Fatalf("item %d is accounted for %d times, want exactly once", payload, count)
			}
		}
	})
}

func itemsFrom(encoded []byte) []interval.Item[int] {
	items := make([]interval.Item[int], 0, len(encoded)/2)
	for i := 0; i+1 < len(encoded); i += 2 {
		items = append(items, interval.Item[int]{
			Span:    mutation.Span{StartByte: uint32(encoded[i]), EndByte: uint32(encoded[i+1])},
			Payload: len(items),
		})
	}
	return items
}

func contains(outer, inner mutation.Span) bool {
	return outer.StartByte <= inner.StartByte && inner.EndByte <= outer.EndByte
}
