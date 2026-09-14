// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/types"
)

// The type gates.
//
// Every family below the comparison family is decided by what the operands
// *are*, not by how they are spelled. `a + b` is an integer-arithmetic
// candidate, a float-arithmetic candidate, or neither, and the difference is
// not visible in the bytes: string concatenation is excluded here because the
// operands are strings, never because a `+` happened to sit beside a quote.
//
// Named types are included throughout, because the underlying type is what
// decides whether an operator applies: `type Celsius float64` adds and
// subtracts exactly like a float64, and refusing to mutate it would mean
// refusing to mutate the domain types a well-typed program is made of.
//
// The one place this reasoning does not reach is a type parameter. `a + b`
// where a and b are of a constrained type parameter has no single underlying
// type — the constraint may admit both integers and strings — so
// [basicInfo] reports nothing for it and every arithmetic gate declines.

// basicInfo returns the [types.BasicInfo] flags of a type's underlying basic
// type, or zero when the type is not basic underneath. A nil type reports zero,
// so an expression the type checker recorded nothing for gates out rather than
// panicking.
func basicInfo(t types.Type) types.BasicInfo {
	if t == nil {
		return 0
	}
	basic, ok := t.Underlying().(*types.Basic)
	if !ok {
		return 0
	}
	return basic.Info()
}

// isInteger reports whether a type is an integer underneath, untyped integer
// and rune constants included.
func isInteger(t types.Type) bool { return basicInfo(t)&types.IsInteger != 0 }

// isFloat reports whether a type is a floating-point type underneath.
//
// Complex is deliberately not floating-point here even though it is numeric:
// v1 does not mutate complex arithmetic, and this is the gate that says so.
func isFloat(t types.Type) bool { return basicInfo(t)&types.IsFloat != 0 }

// isNumeric reports whether a type is any numeric type underneath, complex
// included — `return 0` is a valid zero for a complex result as much as for an
// int one, which is the only question the return-replacement family asks.
func isNumeric(t types.Type) bool { return basicInfo(t)&types.IsNumeric != 0 }

// isStringy reports whether a type is a string underneath.
func isStringy(t types.Type) bool { return basicInfo(t)&types.IsString != 0 }

// isBoolClassed reports whether a type is a boolean underneath, `type Flag
// bool` included. It is the gate for the condition-negation family, because `!`
// applies to any boolean type — unlike a Form C guard, which needs the universe
// bool exactly. See [Guard].
func isBoolClassed(t types.Type) bool { return basicInfo(t)&types.IsBoolean != 0 }

// isUniverseBool reports whether a type is exactly the predeclared `bool`, or
// an untyped boolean that has not been given a named type by its context.
//
// This is the strictest of the boolean questions and the one Form C rests on.
// The type checker records the type an expression finally settled on, so a
// comparison written into a `type Flag bool` context is recorded as that named
// type and is refused here, while the same comparison in an `if` condition
// stays untyped bool and is accepted.
func isUniverseBool(t types.Type) bool {
	return t == types.Typ[types.Bool] || t == types.Typ[types.UntypedBool]
}

// isNillable reports whether a type's underlying form is one `nil` can be
// written for: a pointer, slice, map, channel, function, or interface.
//
// Arrays and structs are absent because they have no nil, and `unsafe.Pointer`
// is absent because it is a basic type — it does accept nil, but naming it
// needs an import this phase will not add.
//
// A type parameter is refused before its underlying type is ever looked at, and
// the refusal is the whole reason this is not a one-line type switch. A type
// parameter's underlying type is its constraint, which is an interface — so
// `T` constrained to `~int | ~string` would answer "nillable" and `return nil`
// would be spliced into a function returning an int. What the constraint is
// made of does not help either: a parameter constrained to `*T | []T` still
// cannot be handed a plain `nil`, because `nil` needs a single type to be.
// isEmptiable reports whether a type has a neutral value that is not nil, and
// which of the two it is.
//
// Slices and maps, and nothing else. The line is: a type is in when it has two
// distinct neutral values that the standard library treats differently and that
// `len()` cannot separate. `x == nil` is true of one and false of the other;
// `encoding/json` writes `null` for one and `[]` or `{}` for the other; and
// `len(x) == 0` is true of both, which is the assertion a suite routinely makes
// and which therefore cannot tell them apart.
//
// A string has no nil, so `""` is its only neutral and return-empty-string
// already offers it. An array or a struct has `T{}`, which is its *zero* value
// rather than a second neutral. A channel's `make(chan T)` is not neutral at
// all: a nil channel makes a select case unselectable and an unbuffered one
// blocks, so the dominant outcome would be a hang. A pointer's `new(T)` would
// allocate and would *mask* a nil dereference rather than expose one. A
// function's would need the whole signature rendered.
//
// The type parameter is refused before the underlying type is consulted, for
// isNillable's reason: a parameter's underlying type is its constraint, and an
// unwary reading would offer a slice literal for a function returning an int.
func isEmptiable(t types.Type) (slice, mapped bool) {
	if t == nil {
		return false, false
	}
	if _, isParam := types.Unalias(t).(*types.TypeParam); isParam {
		return false, false
	}
	switch t.Underlying().(type) {
	case *types.Slice:
		return true, false
	case *types.Map:
		return false, true
	default:
		return false, false
	}
}

func isNillable(t types.Type) bool {
	if t == nil {
		return false
	}
	if _, isParam := types.Unalias(t).(*types.TypeParam); isParam {
		return false
	}
	switch t.Underlying().(type) {
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan, *types.Signature, *types.Interface:
		return true
	default:
		return false
	}
}

// errorType is the predeclared `error` interface, looked up once.
var errorType = types.Universe.Lookup("error").Type()

// errorInterface is that same interface, in the form [types.Implements] wants.
var errorInterface = errorType.Underlying().(*types.Interface)

// isExactlyError reports whether a type *is* the predeclared error interface,
// rather than merely satisfying it.
//
// The error-swallowing family owns error-typed values and the
// return-replacement family owns every other nillable one, and this is the line
// between them. It is drawn at identity rather than at satisfaction on purpose:
// `return &myErr{}` in a function returning `error` is a concrete value being
// returned, and rewriting it to `nil` is the same edit `return-nil` makes to
// any other nillable result, while `return err` is the specific failure mode
// `return-err-to-nil` exists to catch.
func isExactlyError(t types.Type) bool {
	return t != nil && types.Identical(t, errorType)
}

// implementsError reports whether a type satisfies the error interface, which
// is what `err != nil` has to be asking about for `nil-error-branch` to apply.
func implementsError(t types.Type) bool {
	if t == nil || t == types.Typ[types.UntypedNil] || t == types.Typ[types.Invalid] {
		return false
	}
	return types.Implements(t, errorInterface)
}

// typeOf returns the type the checker recorded for an expression, or nil.
func (s *fileScan) typeOf(expr ast.Expr) types.Type {
	if s.info == nil || expr == nil {
		return nil
	}
	tv, ok := s.info.Types[expr]
	if !ok || !tv.IsValue() {
		return nil
	}
	return tv.Type
}

// isNilLiteral reports whether an expression is the predeclared `nil`, and not
// something of that name a package declared for itself.
func (s *fileScan) isNilLiteral(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok || ident.Name != "nil" || s.info == nil {
		return false
	}
	obj := s.info.Uses[ident]
	nilObj, ok := obj.(*types.Nil)
	return ok && nilObj.Parent() == types.Universe
}

// isUniverseConst reports whether an identifier is the predeclared constant of
// its own name, rather than something shadowing it. `true` is not a keyword in
// Go, and a package that declares its own is entitled to have it left alone.
func (s *fileScan) isUniverseConst(ident *ast.Ident) bool {
	if s.info == nil {
		return false
	}
	konst, ok := s.info.Uses[ident].(*types.Const)
	return ok && konst.Parent() == types.Universe
}

// isBuiltinCall reports whether a call is to the predeclared builtin of the
// given name. `panic` shadowed by a function of the user's own is an ordinary
// call and is deleted like any other.
//
// The parentheses are stripped first, because the language does not care about
// them and neither does the property the caller is asking about:
// `(panic)("no")` is a call to the same builtin and terminates its function
// exactly as `panic("no")` does. go/types strips them at that very decision —
// it unparenthesizes an expression statement before asking whether the call it
// holds is the `panic` that ends the function — so an exclusion that stopped at
// the parenthesis would let through the one shape it exists to keep out.
func (s *fileScan) isBuiltinCall(call *ast.CallExpr, name string) bool {
	ident, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok || ident.Name != name || s.info == nil {
		return false
	}
	builtin, ok := s.info.Uses[ident].(*types.Builtin)
	return ok && builtin.Parent() == types.Universe
}
