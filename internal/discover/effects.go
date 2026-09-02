// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
)

// The two grammars a probe hint is decided by.
//
// # What a probe has to be
//
// A probe tree runs the original program and records, per mutant, whether the
// value at its site ever differed from the constant the mutant would have put
// there. The inference a consumer then draws is "the site never differed, so no
// test that ran can have killed this mutant", and that inference needs the
// mutant's execution to be the original's execution with one value swapped.
//
// A Form S mutant of `return E0, E1` at result 0 is `return K, E1`. It does not
// evaluate E0 at all. So the probe stands in for it only where evaluating E0 is
// nothing but computing a value, and three separate things can make that false.
//
// # E: the statement's operands may have no effects
//
// If the probed operand calls, receives or appends, the mutant makes none of
// those happen: `return compute(), nil` mutated to `return 0, nil` never calls
// compute, and a test watching for what compute did kills a mutant the probe
// reports as never having differed.
//
// The operands *beside* it matter for a second reason, and it is the reason the
// rule is stated over the whole statement rather than over one result. The
// rewrite evaluates the operands in source order, and that is not the order the
// compiler uses: the spec leaves the order of a plain variable read relative to
// a call in another operand unspecified, and gc performs the read after the
// calls. For `return s.n, set()` where set writes to s, the probe reads s.n
// first and compares a value the original binary never returned — and answers
// "did not differ" for a mutant a test really can kill. Hoisting the calls the
// way gc does would recover such statements; it is not worth the machinery, so
// the whole statement is refused instead.
//
// With every operand in E no operand has an effect, every evaluation order
// yields the same values, and the probe's execution is the original's.
//
// # P: the probed operand may not panic
//
// If the operand panics in the original, the mutant that replaced it with a
// constant does not. The two programs diverge, and the divergence is invisible
// to the probe: the `if` that would have recorded the difference is never
// reached, so nothing is written and the log reads exactly as it reads for a
// site that never differed. The test passed, so that panic was recovered
// somewhere, and the run is not fail-closed either.
//
// P is a subset of E and is asked of one operand rather than of all of them. A
// panic in another operand is harmless: the original and the mutant both panic
// there identically, since the mutated operand had no effects to change what
// the other one does, and the probe recording nothing is then the truth.
//
// # Neither grammar is the whole language
//
// Both are written as allowlists, and both refuse shapes that are in fact safe:
// an indirect builtin, a shift by a variable that is provably in range, an
// interface comparison against a value whose dynamic type is comparable. Each
// costs one unprobed mutant, which costs a run the executions it could have
// skipped and costs it nothing else. The reverse mistake costs a verdict.
//
// The third condition — a floating-point or complex result, where `-0.0 != 0`
// is false — is a fact about the comparison rather than about the operand, and
// [floatingResult] states it beside these two because it is refused in the same
// place and for the same purpose.
//
// The grammars are here rather than inside the return probe because the next
// probe form needs both of them: a bool-valued site is measured by evaluating
// the site once and comparing, which needs "the whole site is effect-free" and
// "the mutated evaluation cannot panic" in exactly these senses.

// effectFreeBuiltins are the predeclared functions whose call computes a value
// and does nothing else.
//
// The absentees are the point. `append`, `copy`, `delete`, `clear` and `close`
// write to something the program can observe afterwards; `panic` and `recover`
// change where execution goes; `print` and `println` write to standard error.
// `make` and `new` allocate, which no other operand can see — a mutant that
// skips the allocation computes the same values everywhere else — so they are
// effects to nobody and are in.
var effectFreeBuiltins = map[string]bool{
	"len":     true,
	"cap":     true,
	"min":     true,
	"max":     true,
	"real":    true,
	"imag":    true,
	"complex": true,
	"new":     true,
	"make":    true,
}

// panicFreeBuiltins are those of them whose call cannot panic. `make` is the
// one that can: a negative or overflowing length is a run-time panic, and the
// length is an ordinary expression the compiler cannot check.
var panicFreeBuiltins = map[string]bool{
	"len":     true,
	"cap":     true,
	"min":     true,
	"max":     true,
	"real":    true,
	"imag":    true,
	"complex": true,
	"new":     true,
}

// effectFree reports whether evaluating an expression changes nothing a later
// evaluation could observe: grammar E above.
//
// Every function call, every method call and every receive is an effect, and so
// is everything reachable through them, which is why a call is admitted only as
// a conversion or as one of [effectFreeBuiltins]. A function literal is not a
// call: creating a closure captures variables and evaluates none of its body.
func (g *guardResolver) effectFree(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident, *ast.BasicLit, *ast.FuncLit:
		return true
	case *ast.ParenExpr:
		return g.effectFree(e.X)
	case *ast.SelectorExpr:
		// A field, a method value, or a qualified identifier: each reads, and
		// none of them calls.
		return g.effectFree(e.X)
	case *ast.StarExpr:
		return g.effectFree(e.X)
	case *ast.IndexExpr:
		return g.effectFree(e.X) && g.effectFree(e.Index)
	case *ast.IndexListExpr:
		if !g.effectFree(e.X) {
			return false
		}
		for _, index := range e.Indices {
			if !g.effectFree(index) {
				return false
			}
		}
		return true
	case *ast.SliceExpr:
		return g.effectFree(e.X) &&
			g.effectFreeOrAbsent(e.Low) && g.effectFreeOrAbsent(e.High) && g.effectFreeOrAbsent(e.Max)
	case *ast.TypeAssertExpr:
		// A nil Type is the `x.(type)` of a type switch guard, which is not an
		// expression that has a value at all.
		return e.Type != nil && g.effectFree(e.X)
	case *ast.UnaryExpr:
		return e.Op != token.ARROW && g.effectFree(e.X)
	case *ast.BinaryExpr:
		return g.effectFree(e.X) && g.effectFree(e.Y)
	case *ast.CompositeLit:
		return g.compositeParts(e, g.effectFree)
	case *ast.CallExpr:
		return g.effectFreeCall(e)
	default:
		return false
	}
}

// effectFreeOrAbsent is [guardResolver.effectFree] for the optional parts of a
// slice expression, where a missing bound is no evaluation at all.
func (g *guardResolver) effectFreeOrAbsent(expr ast.Expr) bool {
	return expr == nil || g.effectFree(expr)
}

// effectFreeCall admits the two kinds of call that are not calls: a conversion,
// which computes a value of another type from one it is handed, and a builtin
// from [effectFreeBuiltins].
func (g *guardResolver) effectFreeCall(call *ast.CallExpr) bool {
	if !g.isConversion(call) {
		name, ok := g.builtinName(call)
		if !ok || !effectFreeBuiltins[name] {
			return false
		}
	}
	return g.argumentsAre(call, g.effectFree)
}

// panicFree reports whether evaluating an expression cannot panic: grammar P
// above, which is a subset of E.
//
// Refused throughout, and each for a run-time panic the compiler cannot rule
// out: a dereference and an index (nil, out of range), a type assertion in its
// single-value form, a selector that indirects through a pointer or reaches
// into an interface, a method value (evaluating one dereferences the receiver
// of a value-receiver method), division and shifts by a non-constant, a
// comparison that may compare interface values, and `make`.
func (g *guardResolver) panicFree(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident, *ast.BasicLit, *ast.FuncLit:
		return true
	case *ast.ParenExpr:
		return g.panicFree(e.X)
	case *ast.SelectorExpr:
		return g.panicFreeSelector(e)
	case *ast.UnaryExpr:
		switch e.Op {
		case token.ADD, token.SUB, token.NOT, token.XOR, token.AND:
			return g.panicFree(e.X)
		default:
			return false
		}
	case *ast.BinaryExpr:
		return g.panicFreeBinary(e)
	case *ast.CompositeLit:
		return g.hashableKeys(e) && g.compositeParts(e, g.panicFree)
	case *ast.CallExpr:
		return g.panicFreeCall(e)
	default:
		return false
	}
}

// panicFreeSelector admits the two selections that read a value already in
// hand: a qualified identifier, whose base is a package rather than a value,
// and a field of a struct value.
//
// A field reached through a pointer is a dereference and is refused; so is one
// reached through an embedded pointer, which [types.Selection.Indirect] is what
// reports. A method value is refused whatever it is bound to: `x.M` where M has
// a value receiver and x is a nil pointer panics as it is evaluated, before
// anything calls it.
func (g *guardResolver) panicFreeSelector(sel *ast.SelectorExpr) bool {
	if g.info == nil {
		return false
	}
	selection, resolved := g.info.Selections[sel]
	if !resolved {
		// Not a selection: a qualified identifier, whose base names a package.
		// The checker records nothing in Selections for it, so the base is
		// asked what it is rather than assumed.
		base, isIdent := sel.X.(*ast.Ident)
		if !isIdent {
			return false
		}
		_, isPackage := g.info.Uses[base].(*types.PkgName)
		return isPackage
	}
	if selection.Kind() != types.FieldVal || selection.Indirect() {
		return false
	}
	if _, isInterface := underlyingOf(g.typeOf(sel.X)).(*types.Interface); isInterface {
		return false
	}
	return g.panicFree(sel.X)
}

// panicFreeBinary decides the operators.
//
// Three of them can fail and the rest cannot. Division and remainder panic on a
// zero divisor, so the divisor has to be a constant the compiler already
// evaluated and found non-zero. A shift by a negative count panics, and a
// constant count is one the compiler has already refused if it is negative.
// Equality is the subtle one: comparing two interface values whose dynamic type
// is not comparable panics, and a struct or array holding an interface carries
// the same hazard, so `==` and `!=` are admitted only for operands that are
// basic, pointer or channel underneath. The ordered comparisons are legal on
// basic types alone and need no such rule.
//
// Signed overflow is not a panic in Go — `math.MinInt / -1` is defined to wrap
// — so nothing here has to reason about it.
func (g *guardResolver) panicFreeBinary(expr *ast.BinaryExpr) bool {
	if !g.panicFree(expr.X) || !g.panicFree(expr.Y) {
		return false
	}
	switch expr.Op {
	case token.ADD, token.SUB, token.MUL,
		token.AND, token.OR, token.XOR, token.AND_NOT,
		token.LAND, token.LOR:
		return true
	case token.QUO, token.REM:
		divisor := g.constantValue(expr.Y)
		return divisor != nil && constant.Sign(divisor) != 0
	case token.SHL, token.SHR:
		return g.constantValue(expr.Y) != nil
	case token.EQL, token.NEQ:
		return comparesWithoutPanic(g.typeOf(expr.X)) && comparesWithoutPanic(g.typeOf(expr.Y))
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	default:
		return false
	}
}

// panicFreeCall is [guardResolver.effectFreeCall] narrowed to the calls that
// also cannot fail.
func (g *guardResolver) panicFreeCall(call *ast.CallExpr) bool {
	if g.isConversion(call) {
		return g.panicFreeConversion(call)
	}
	name, ok := g.builtinName(call)
	if !ok || !panicFreeBuiltins[name] {
		return false
	}
	return g.argumentsAre(call, g.panicFree)
}

// panicFreeConversion refuses the one conversion that panics: a slice converted
// to an array, or to a pointer to one, panics when the slice is shorter than
// the array. Every other conversion computes its result from bits it has.
func (g *guardResolver) panicFreeConversion(call *ast.CallExpr) bool {
	if len(call.Args) != 1 || !g.panicFree(call.Args[0]) {
		return false
	}
	if _, fromSlice := underlyingOf(g.typeOf(call.Args[0])).(*types.Slice); !fromSlice {
		return true
	}
	if g.info == nil {
		return false
	}
	target, known := g.info.Types[ast.Unparen(call.Fun)]
	if !known {
		return false
	}
	switch underlyingOf(target.Type).(type) {
	case *types.Array, *types.Pointer:
		return false
	default:
		return true
	}
}

// compositeParts reports whether every expression a composite literal evaluates
// satisfies a grammar.
//
// The literal's type is not one of them — it is a type, and an array length
// written into it is a constant — and neither is a struct literal's key, which
// is the name of a field. That name is an identifier, so both grammars accept
// it where they stand, and there is no need to ask the checker which kind of
// literal this is. A map literal's keys are ordinary expressions and are
// scanned like its values.
func (g *guardResolver) compositeParts(lit *ast.CompositeLit, admits func(ast.Expr) bool) bool {
	for _, elt := range lit.Elts {
		if kv, isPair := elt.(*ast.KeyValueExpr); isPair {
			if !admits(kv.Key) || !admits(kv.Value) {
				return false
			}
			continue
		}
		if !admits(elt) {
			return false
		}
	}
	return true
}

// hashableKeys refuses a map literal whose key type can hold a value that
// cannot be hashed.
//
// Building the map hashes every key, and hashing a slice, a map or a function
// held in an interface is a run-time panic — the same hazard the equality rule
// above refuses, arriving through a different door. `map[any]int{k: 1}` is
// therefore refused whatever k is; a map keyed by a basic, pointer or channel
// type cannot hold one. Everything that is not a map literal passes.
func (g *guardResolver) hashableKeys(lit *ast.CompositeLit) bool {
	m, isMap := underlyingOf(g.typeOf(lit)).(*types.Map)
	if !isMap {
		return true
	}
	return comparesWithoutPanic(m.Key())
}

// argumentsAre reports whether every argument of a call satisfies a grammar,
// passing over the ones that are types: `new(T)` and `make([]T, n)` name a type
// in argument position, and a type is not evaluated.
func (g *guardResolver) argumentsAre(call *ast.CallExpr, admits func(ast.Expr) bool) bool {
	for _, arg := range call.Args {
		if g.isTypeExpr(arg) {
			continue
		}
		if !admits(arg) {
			return false
		}
	}
	return true
}

// isConversion reports whether a call is a type conversion rather than a call
// of a function.
func (g *guardResolver) isConversion(call *ast.CallExpr) bool {
	return g.isTypeExpr(ast.Unparen(call.Fun))
}

// builtinName names the predeclared function a call calls, or reports false.
//
// The callee has to be spelled as a bare identifier. `(len)(s)` is legal Go and
// is just as effect-free, and it is refused here anyway: the question this
// answers is about a small set of exact shapes, an indirect spelling costs one
// unprobed mutant, and the alternative is a rule that has to reason about how
// far the parentheses go. A `len` the package shadowed with a function of its
// own is not a [types.Builtin] and is refused by the same lookup, which is why
// the name is taken from the object rather than from the source.
func (g *guardResolver) builtinName(call *ast.CallExpr) (string, bool) {
	if g.info == nil {
		return "", false
	}
	ident, isIdent := call.Fun.(*ast.Ident)
	if !isIdent {
		return "", false
	}
	builtin, isBuiltin := g.info.Uses[ident].(*types.Builtin)
	if !isBuiltin || builtin.Parent() != types.Universe {
		// A builtin of package unsafe has no universe parent, and every one of
		// them is out: they read or compute over memory the type system is
		// deliberately not describing.
		return "", false
	}
	return builtin.Name(), true
}

// isTypeExpr reports whether an expression denotes a type rather than a value.
func (g *guardResolver) isTypeExpr(expr ast.Expr) bool {
	if g.info == nil || expr == nil {
		return false
	}
	tv, known := g.info.Types[expr]
	return known && tv.IsType()
}

// typeOf returns the type the checker recorded for an expression, or nil.
func (g *guardResolver) typeOf(expr ast.Expr) types.Type {
	if g.info == nil || expr == nil {
		return nil
	}
	tv, known := g.info.Types[expr]
	if !known || !tv.IsValue() {
		return nil
	}
	return tv.Type
}

// constantValue returns the value the checker folded an expression to, or nil
// when it is not a constant.
func (g *guardResolver) constantValue(expr ast.Expr) constant.Value {
	if g.info == nil || expr == nil {
		return nil
	}
	tv, known := g.info.Types[expr]
	if !known {
		return nil
	}
	return tv.Value
}

// comparesWithoutPanic reports whether `==` over a type is decided by the bits
// of the values rather than by a dynamic type that may not be comparable.
//
// Basic, pointer and channel types are the answer, and struct and array types
// are deliberately not: they compare field by field, and one interface-typed
// field anywhere inside makes the comparison able to panic. Refusing them whole
// costs the few structs that hold no interface.
func comparesWithoutPanic(t types.Type) bool {
	switch underlyingOf(t).(type) {
	case *types.Basic, *types.Pointer, *types.Chan:
		return true
	default:
		return false
	}
}

// floatingResult reports whether a result type is one whose values `!=` does
// not separate.
//
// IEEE 754 says `-0.0 == 0`, so a `return-zero-numeric` mutant at a float
// result holding negative zero is recorded as *not* infected — the answer that
// skips the test — while `math.Signbit` and `1/x` both tell the two apart, so a
// test really can kill it. Complex results carry the same hazard in each of
// their two parts. NaN needs no rule and gets none: `NaN != 0` is true, so such
// a site reports infected, which is only ever the safe answer.
func floatingResult(t types.Type) bool {
	return basicInfo(t)&(types.IsFloat|types.IsComplex) != 0
}

// underlyingOf is the underlying type, or nil for a nil type, so that an
// expression the checker recorded nothing for falls through every type switch
// above rather than panicking.
func underlyingOf(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	return t.Underlying()
}
