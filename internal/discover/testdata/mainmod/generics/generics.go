// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package generics separates the value code of a generic function, which is
// mutated like any other, from the type positions around it, which are not.
package generics

// Ordered is the constraint the live comparison below is written against.
type Ordered interface {
	~int | ~string
}

// Max holds a comparison in a generic function body: type parameters change
// nothing about a body being ordinary code.
//
// Its returns are the other half of that. A type parameter's underlying type is
// its constraint, which is an interface, so a return-replacement rule reading
// the underlying type carelessly would offer to rewrite `return a` into `return
// nil` in a function that returns an int. Neither return here is a candidate.
func Max[T Ordered](a, b T) T {
	if a > b {
		return a
	}
	return b
}

// sized has a type parameter whose constraint is an array type, which is the
// only way a value expression can appear inside a type parameter list at all.
func sized[T [len([1]bool{true})]byte](v T) T { return v }

// Instantiate writes its type argument out explicitly and hides a boolean
// literal in it the same way, so that an explicit instantiation has something
// to suppress.
func Instantiate() byte {
	var v [1]byte
	return sized[[len([1]bool{false})]byte](v)[0]
}

// boxed is sized's type-declaration twin: a type parameter list on a *type*
// rather than on a function. The two are separate syntax nodes, and a walk
// that only knew about the function one would leave this constraint's literal
// mutable.
type boxed[T [len([1]bool{true})]byte] struct{ v T }

// Unbox instantiates boxed, so that the declaration is not the only mention of
// it in the file.
func Unbox(b boxed[[1]byte]) byte { return b.v[0] }

// pair takes two type parameters, which is what makes an explicit
// instantiation of it a list of type arguments rather than a single one —
// again a different syntax node, and again one a walk can miss on its own.
type pair[K comparable, V any] struct {
	key   K
	value V
}

// InstantiatePair writes both type arguments out and hides a boolean literal
// in each, so that every position in the list has something to suppress.
func InstantiatePair() int {
	p := pair[[len([1]bool{true})]byte, [len([1]bool{false})]int]{}
	return len(p.key) + len(p.value)
}
