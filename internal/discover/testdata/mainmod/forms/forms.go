// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package forms holds one site of each guard form the hint can carry. It is
// the fixture for [discover.Guard] rather than for any operator family: the
// edits here are ordinary and what is being pinned is which form each position
// resolves to.
//
// It used to hold one site of each shape the hint *refused* as well, and most
// of this file is those sites. They are still here and they are still the
// shapes they were — a `:=` that redeclares, an initialiser that names the
// variable it declares, a declared type spelled across lines — but each of them
// is now a site rather than a refusal, because the form that reaches them
// arrived after they were written. The doc comment on each says which form
// takes it and why the earlier ones do not, which is more useful than a list of
// refusals would have been: the reason a site resolves to the form it does is
// the whole subject here.
//
// One shape is a refusal still, and it is in package unnameable rather than
// here, because it needs a type from another package that nothing outside that
// package can name.
package forms

// Declared is a Form D site of the `var` kind. The statement declares a name,
// so a statement guard would bury the declaration in a block and everything
// after it would stop compiling.
func Declared(a, b int) int {
	var sum = a + b
	return sum
}

// Short is the same site written with `:=`, which is the form the design plan
// named first.
func Short(a, b int) int {
	product := a * b
	return product
}

// Redeclared is a Form E site, and it is Form D's refusal underneath.
//
// `err` already exists here, so the second `:=` redeclares rather than
// declares, and Form D would have to know which names to hoist and which to
// leave alone — a distinction the hint does not carry, so it declines the whole
// statement. Form E then takes the *initialiser expression* instead, which
// declares nothing at all and leaves the statement exactly as it was.
func Redeclared(a int) (int, error) {
	first, err := split(a)
	if err != nil {
		return 0, err
	}
	second, err := split(first + 1)
	return second, err
}

// split is what Redeclared calls twice.
func split(a int) (int, error) { return a, nil }

// Post is a Form F site, and it used to be a refusal.
//
// A block is not legal in a `for` post statement -- `for i := 0; i < n; if
// __gm.M[3] { i -= 2 } else { i += 2 }` does not parse -- and that is still
// true. What the slot does hold is a *simple* statement, and a call is one:
// `for i := 0; i < n; func() { … }()` parses, runs the guard where the step
// stood, and leaves the loop variable the closure captures the same variable
// the step would have touched.
func Post(n int, out []int) {
	for i := 0; i < n; i += 2 {
		out[0] += i
	}
}

// InitAssign is the same form in an `if` initialiser, which is the shape that
// occurs in real Go: `if err = f(); err != nil` is an assignment in a slot no
// block can stand in.
func InitAssign(n int, out []int) int {
	var half int
	if half = n / 2; half > 0 {
		out[0] = half
	}
	return half
}

// Init is a Form E site, and it is the one place all four earlier forms are
// refused in turn.
//
// A block is not legal in an initialiser slot, so Form S and Form D are out. A
// declaration moved into a closure declares inside the closure, so Form F is
// out. The expression is an `int`, so Form C and Form C' have nothing to
// select. Form E moves the initialiser expression and leaves the declaration
// where it stands.
func Init(n int) int {
	if half := n / 2; half > 0 {
		return half
	}
	return 0
}

// Tag is a Form E site, and it is the shape with no statement around it at all.
//
// The nearest statement to a `switch` tag is the `switch` itself, and no form
// wraps one; walking further out would guard a statement that does not hold the
// edit. A tag is an expression, and an expression of a type this file can spell
// is what Form E needs.
func Tag(a, b int) string {
	switch a + b {
	case 0:
		return "zero"
	}
	return "other"
}

// Statements holds the three Form S statement kinds no other fixture reaches.
func Statements(ch chan int, a, b int) {
	ch <- a + b
	defer sink(a - b)
	go sink(a * b)
}

// sink is what Statements defers and spawns.
func sink(int) {}

// BoolCalls holds the three statements a call may be written as — on its own,
// after `defer`, and after `go` — around a call whose result is the universe
// bool. That result is the only thing separating it from Statements above.
//
// A bool-valued expression is normally a Form C site: the guard wraps it and
// the compiler settles the typing. Not here. Form C renders a parenthesized
// `||` expression, and none of these three positions accepts one — a bool that
// is not used is a compile error, and the operand of `defer` and of `go` has to
// be a call. So the call is passed over and the statement around it is the
// site, exactly as it is for a call that returns nothing.
func BoolCalls(a, b int) {
	ok(a + b)
	defer ok(a - b)
	go ok(a * b)
}

// ok is what BoolCalls calls three times. It returns the universe bool rather
// than a named boolean type, so nothing but position keeps Form C away.
func ok(n int) bool { return n > 0 }

// Limit is what the two shadowing refusals below declare over. It is package
// state on purpose: a shadowed name has to resolve to something outside the
// statement being rewritten, or there is nothing for a hoist to rebind.
var Limit = 10

// Shadowed is a Form E site, and it is the clearest case of why the closure
// goes where the expression was rather than in front of it.
//
// Go begins a declared name's scope at the *end* of its specification, so the
// `total` on the right of the inner `:=` is the one declared above the block.
// Form D hoists `var total int;` in front of that assignment, putting the new
// name in scope first and reading a zero out of it — a program that compiles
// and computes something else, which is why Form D refuses this site rather
// than rewriting it. Form E moves nothing: the closure sits inside the
// initialiser, which is before that end, so the `total` inside it resolves to
// the enclosing declaration exactly as the original did.
func Shadowed(n int) int {
	total := n
	{
		total := total * 2
		n = total
	}
	return n
}

// Widened is the same shape in the `var` form, over a name this package
// declares rather than one an enclosing block does. Form D refuses both
// identically, and Form E takes both identically, which is the point of having
// the pair: neither form may be safe on one of them and not the other.
func Widened(n int) int {
	var Limit = Limit + n*2
	return Limit
}

// CrossSpec is the `var` block whose specs refer to one another.
//
// A parenthesized `var` block is one statement and therefore one Form D site,
// and Form D hoists every name in it at once. The `Limit` in the first spec is
// this package's and would stop being so; the `a` in the second is already in
// scope where it stands and would keep meaning what it means. Weighing each
// spec against its own names alone would accept the site and rebind that first
// reference, so every name the block declares is collected before any
// initialiser in it is looked at — and the whole site is refused.
//
// Form E then takes each initialiser expression on its own, which is right for
// the same reason it is right in Shadowed: a closure inside the first spec's
// initialiser is before the end of the second spec, so the `Limit` in it
// resolves to this package's, exactly as the original does.
func CrossSpec(n int) int {
	var (
		a     = Limit + n*2
		Limit = a + 1
	)
	return a + Limit
}

// Widen is the declared type spelled across lines.
//
// Form D turns a declaration into an assignment by cutting the declaring tokens
// out in place, and the type is one of them: the bytes removed here hold a line
// break, and removing it would move every line below. Padding the cut is not an
// escape — `scale func(\n…\n) int = mk(n)` padded back to its own height reads
// `scale \n\n = mk(n)`, where the scanner ends the statement after `scale` and
// the program is a different one. So Form D refuses, and Form E takes the
// addition inside the call, which cuts nothing and moves nothing.
func Widen(n int, mk func(int) func(int) int) int {
	var scale func(
		v int,
	) int = mk(n + 1)
	return scale(n)
}

// Widest is the same shape over the other variable-length cut. A spec with no
// initialiser is not an assignment and cannot become one, so Form D would
// remove it whole and take its multi-line type with it — while the spec beside
// it, on the same statement and so on the same site, is what holds the
// candidate. Form D refuses the site and Form E takes the expression.
func Widest(n int) int {
	var (
		total struct {
			hi int
		}
		start = n + 1
	)
	total.hi = start
	return total.hi
}

// Named is a Form C' site: a condition whose type is a named boolean rather
// than the universe one.
//
// Form C composes an untyped boolean selector, and one of those cannot be put
// where a `Flag` was expected. Form C' writes the same selector with a
// conversion at each end, which is legal in both directions because a defined
// boolean type's underlying type is `bool`.
func Named(f Flag, n int) int {
	if f {
		return n
	}
	return 0
}

// A Flag is a boolean with a name of its own, for Named above.
type Flag bool
