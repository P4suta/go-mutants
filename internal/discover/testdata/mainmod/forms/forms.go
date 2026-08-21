// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package forms holds one site of each guard form the hint can carry, and one
// site of each shape it refuses. It is the fixture for [discover.Guard] rather
// than for any operator family: the edits here are ordinary and what is being
// pinned is where the instrumenter would have to put the switch.
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

// Redeclared is the refusal a short declaration earns by redeclaring rather
// than declaring: `err` already exists here, so the hint would have to say
// which names Form D declares and which it leaves alone. v1 declines the whole
// site instead of rewriting half of it.
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

// Post is the refusal a `for` post statement earns. A block is not legal
// there: `for i := 0; i < n; if __gm.M[3] { i -= 2 } else { i += 2 }` does not
// parse, so a hint pointing at it would be a hint the instrumenter could not
// use.
func Post(n int, out []int) {
	for i := 0; i < n; i += 2 {
		out[0] += i
	}
}

// Init is the same refusal in an `if` initialiser.
func Init(n int) int {
	if half := n / 2; half > 0 {
		return half
	}
	return 0
}

// Tag is the refusal a `switch` tag earns. The nearest statement is the
// `switch` itself, and no form wraps one; walking further out would guard a
// statement that does not hold the edit.
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

// Shadowed is the refusal a short declaration earns by naming, in its own
// initialiser, a variable it declares.
//
// Go begins a declared name's scope at the *end* of its specification, so the
// `total` on the right of the inner `:=` is the one declared above the block.
// Form D would hoist `var total int;` in front of that assignment, putting the
// new name in scope first and reading a zero out of it. The rewritten program
// compiles and computes something else, which is the whole reason the site is
// refused instead of rewritten: nothing later in the run would notice.
func Shadowed(n int) int {
	total := n
	{
		total := total * 2
		n = total
	}
	return n
}

// Widened is the same refusal in the `var` form, over a name this package
// declares rather than one an enclosing block does. The two forms hoist
// identically, so neither is safe on its own evidence.
func Widened(n int) int {
	var Limit = Limit + n*2
	return Limit
}

// CrossSpec is the refusal a `var` block earns for a reference that crosses
// from one of its specs to another.
//
// A parenthesized `var` block is one statement and therefore one site, and
// Form D hoists every name in it at once. The `Limit` in the first spec is this
// package's and would stop being so; the `a` in the second is already in scope
// where it stands and would keep meaning what it means. Weighing each spec
// against its own names alone would accept this site and rebind that first
// reference, so every name the block declares is collected before any
// initialiser in it is looked at.
func CrossSpec(n int) int {
	var (
		a     = Limit + n*2
		Limit = a + 1
	)
	return a + Limit
}

// Widen is the refusal a declared type spelled across lines earns.
//
// Form D turns a declaration into an assignment by cutting the declaring tokens
// out in place, and the type is one of them: the bytes removed here hold a line
// break, and removing it would move every line below. Padding the cut is not an
// escape — `scale func(\n…\n) int = mk(n)` padded back to its own height reads
// `scale \n\n = mk(n)`, where the scanner ends the statement after `scale` and
// the program is a different one. The addition inside the call is what makes
// the refusal observable: without an edit at this site there is no hint to
// compute and nothing to record.
func Widen(n int, mk func(int) func(int) int) int {
	var scale func(
		v int,
	) int = mk(n + 1)
	return scale(n)
}

// Widest is the same refusal over the other variable-length cut. A spec with no
// initialiser is not an assignment and cannot become one, so it is removed
// whole and takes its multi-line type with it — while the spec beside it, on
// the same statement and so on the same site, is what holds the candidate.
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
