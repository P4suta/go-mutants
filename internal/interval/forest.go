// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package interval

import (
	"cmp"
	"slices"

	"github.com/P4suta/go-mutants/internal/mutation"
)

type Item[T any] struct {
	Span    mutation.Span
	Payload T
}

type Node[T any] struct {
	Span mutation.Span

	Alternatives []T

	Children []*Node[T]
}

type Forest[T any] struct {
	roots []*Node[T]
}

func (f Forest[T]) Roots() []*Node[T] { return f.roots }

type Reason string

const (
	ReasonPartialOverlap Reason = "partial-overlap"

	ReasonEmptySpan Reason = "empty-span"
)

type Conflict[T any] struct {
	Item    Item[T]
	Reason  Reason
	Against mutation.Span
}

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
		if c := cmp.Compare(b.EndByte, a.EndByte); c != 0 {
			return c
		}
		return cmp.Compare(x, y)
	})

	var (
		forest    Forest[T]
		conflicts []Conflict[T]
		stack     []*Node[T]
	)

	for _, i := range order {
		item := items[i]
		if item.Span.IsEmpty() {
			conflicts = append(conflicts, Conflict[T]{Item: item, Reason: ReasonEmptySpan})
			continue
		}

		for len(stack) > 0 && stack[len(stack)-1].Span.EndByte <= item.Span.StartByte {
			stack = stack[:len(stack)-1]
		}

		if len(stack) > 0 {
			enclosing := stack[len(stack)-1]
			if item.Span.EndByte > enclosing.Span.EndByte {
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

func (f Forest[T]) InnerFirst(visit func(node *Node[T])) {
	type frame struct {
		node *Node[T]
		next int
	}
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
