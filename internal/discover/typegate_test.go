// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/types"
	"testing"
)

// The type predicates every rule's applicability is decided by, tested as the
// functions they are.
//
// Driving them through a scan asserts which candidates a fixture produced,
// which is the right test for the walk and the wrong one for these: a predicate
// that answered "nillable" for a type parameter would be caught by a fixture
// that happens to hold one, and silently right about every type nobody wrote a
// fixture for. Here the type is built and handed in, so the refusals are stated
// for the shapes that matter rather than for the shapes the corpus contains.

// typeParam is a type parameter constrained to an interface, which is what a
// generic function's `T` is by the time go/types has recorded it.
func typeParam(t *testing.T, constraint types.Type) types.Type {
	t.Helper()
	name := types.NewTypeName(0, nil, "T", nil)
	return types.NewTypeParam(name, constraint)
}

// TestIsEmptiableIsSlicesAndMapsAndNothingElse pins the line the neutral-value
// family is drawn at.
//
// A type is in when it has two distinct neutral values the standard library
// treats differently and `len()` cannot separate, which is slices and maps. A
// string has no nil; an array or a struct has `T{}` as its zero value rather
// than as a second neutral; a channel's non-nil empty blocks rather than being
// empty; a pointer's `new(T)` would mask a nil dereference instead of exposing
// one; a function's would need its whole signature rendered.
func TestIsEmptiableIsSlicesAndMapsAndNothingElse(t *testing.T) {
	t.Parallel()

	str := types.Typ[types.String]
	named := types.NewNamed(types.NewTypeName(0, nil, "Lines", nil), types.NewSlice(str), nil)

	for _, test := range []struct {
		name          string
		typ           types.Type
		slice, mapped bool
	}{
		{name: "a slice", typ: types.NewSlice(str), slice: true},
		{name: "a map", typ: types.NewMap(str, str), mapped: true},
		{name: "a named slice", typ: named, slice: true},
		{name: "a string", typ: str},
		{name: "an int", typ: types.Typ[types.Int]},
		{name: "a pointer", typ: types.NewPointer(str)},
		{name: "a channel", typ: types.NewChan(types.SendRecv, str)},
		{name: "an array", typ: types.NewArray(str, 3)},
		{name: "a struct", typ: types.NewStruct(nil, nil)},
		{name: "an interface", typ: types.NewInterfaceType(nil, nil)},
		{name: "a signature", typ: types.NewSignatureType(nil, nil, nil, nil, nil, false)},
		// The type parameter is refused before its underlying type is
		// consulted, because a parameter's underlying type is its constraint:
		// a `T` constrained to `~[]byte` would otherwise be offered `[]T{}`
		// for a function that may be returning an int.
		{name: "a type parameter constrained to a slice", typ: typeParam(t, types.NewInterfaceType(nil, nil))},
		{name: "nothing at all", typ: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			slice, mapped := isEmptiable(test.typ)
			if slice != test.slice || mapped != test.mapped {
				t.Errorf("isEmptiable = %v, %v, want %v, %v", slice, mapped, test.slice, test.mapped)
			}
		})
	}
}

// TestIsNillableRefusesATypeParameterBeforeItsUnderlyingType is the refusal
// that is the whole reason the predicate is not a one-line type switch.
//
// A type parameter's underlying type is its constraint, which is an interface
// — so a `T` constrained to `~int | ~string` would answer "nillable" and
// `return nil` would be spliced into a function returning an int. What the
// constraint is made of does not help: a parameter constrained to `*T | []T`
// still cannot be handed a plain `nil`, because `nil` needs a single type.
func TestIsNillableRefusesATypeParameterBeforeItsUnderlyingType(t *testing.T) {
	t.Parallel()

	str := types.Typ[types.String]
	for _, test := range []struct {
		name string
		typ  types.Type
		want bool
	}{
		{name: "a pointer", typ: types.NewPointer(str), want: true},
		{name: "a slice", typ: types.NewSlice(str), want: true},
		{name: "a map", typ: types.NewMap(str, str), want: true},
		{name: "a channel", typ: types.NewChan(types.SendRecv, str), want: true},
		{name: "a signature", typ: types.NewSignatureType(nil, nil, nil, nil, nil, false), want: true},
		{name: "an interface", typ: types.NewInterfaceType(nil, nil), want: true},
		{name: "a named pointer", typ: types.NewNamed(
			types.NewTypeName(0, nil, "P", nil), types.NewPointer(str), nil), want: true},
		{name: "a string", typ: str},
		{name: "an int", typ: types.Typ[types.Int]},
		{name: "an array", typ: types.NewArray(str, 3)},
		{name: "a struct", typ: types.NewStruct(nil, nil)},
		{name: "a type parameter", typ: typeParam(t, types.NewInterfaceType(nil, nil))},
		{name: "nothing at all", typ: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := isNillable(test.typ); got != test.want {
				t.Errorf("isNillable = %v, want %v", got, test.want)
			}
		})
	}
}

// TestImplementsErrorRefusesTheTypesThatAreNotTypes is the gate
// `nil-error-branch` asks its question through.
//
// Untyped nil and the invalid type both reach here from an expression the
// checker could not place, and types.Implements answers "yes" for the first of
// them -- every interface is satisfied by nil. A comparison against an
// unplaceable expression is not an `err != nil` to rewrite.
func TestImplementsErrorRefusesTheTypesThatAreNotTypes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		typ  types.Type
		want bool
	}{
		{name: "the error interface itself", typ: errorType, want: true},
		{name: "a pointer to a type with an Error method", typ: errorImplementor(t), want: true},
		{name: "an int", typ: types.Typ[types.Int]},
		{name: "a string", typ: types.Typ[types.String]},
		{name: "untyped nil", typ: types.Typ[types.UntypedNil]},
		{name: "the invalid type", typ: types.Typ[types.Invalid]},
		{name: "nothing at all", typ: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := implementsError(test.typ); got != test.want {
				t.Errorf("implementsError = %v, want %v", got, test.want)
			}
		})
	}

	// And the sharper question beside it: exactly `error`, which is what
	// separates error-swallowing from return-replacement.
	if !isExactlyError(errorType) {
		t.Error("isExactlyError refuses the error interface itself")
	}
	if isExactlyError(errorImplementor(t)) {
		t.Error("isExactlyError accepts a concrete type that merely implements error")
	}
	if isExactlyError(nil) {
		t.Error("isExactlyError accepts nothing at all")
	}
}

// errorImplementor is a named pointer type with an Error method, which is what
// a `return &myErr{}` from a function returning error has.
func errorImplementor(t *testing.T) types.Type {
	t.Helper()

	pkg := types.NewPackage("example.com/m", "m")
	named := types.NewNamed(types.NewTypeName(0, pkg, "myErr", nil), types.NewStruct(nil, nil), nil)
	sig := types.NewSignatureType(
		types.NewVar(0, pkg, "e", types.NewPointer(named)), nil, nil, nil,
		types.NewTuple(types.NewVar(0, pkg, "", types.Typ[types.String])), false)
	named.AddMethod(types.NewFunc(0, pkg, "Error", sig))
	return types.NewPointer(named)
}

// TestTheUniverseNamesAreTheOnesNobodyShadowed is the rule three predicates
// share: a package that declares its own `nil`, `true` or `panic` is entitled
// to have it left alone.
//
// `true` is not a keyword in Go and neither is `nil`, so the name alone proves
// nothing. What proves it is the object the checker resolved the identifier to
// and the scope that object belongs to.
func TestTheUniverseNamesAreTheOnesNobodyShadowed(t *testing.T) {
	t.Parallel()

	t.Run("the predeclared ones are recognised", func(t *testing.T) {
		t.Parallel()

		got := scanSource(t, `package pkg

// Fail returns an error, or nil.
func Fail(bad bool) error {
	if bad {
		return errNew()
	}
	return nil
}

func errNew() error { return nil }

// Always is a condition written with the predeclared constant.
func Always() bool { return true }

// Must ends the program with the predeclared builtin.
func Must(ok bool) {
	if !ok {
		panic("no")
	}
}
`)
		// The nil literal is what makes the `return nil` above a no-op edit
		// for return-err-to-nil rather than a candidate.
		if got.has("return-err-to-nil", "nil", "nil") {
			t.Error("a `return nil` produced a return-err-to-nil candidate")
		}
		if !got.has("true-to-false", "true", "false") {
			t.Errorf("scan found %v, want the predeclared true swapped", got.rules())
		}
		// The predeclared panic is not deleted: removing a terminating panic
		// leaves a path that reaches the closing brace without returning.
		for _, c := range got.candidates {
			if c.Rule.Name == "delete-call-statement" && c.Original == `panic("no")` {
				t.Error("the predeclared panic was offered for deletion")
			}
		}
	})

	t.Run("a package's own are left alone", func(t *testing.T) {
		t.Parallel()

		got := scanSource(t, `package pkg

// true, nil and panic, all three declared here.
const true_ = 1

type shadow struct{}

// Must is a function of this package's own, named panic.
func panic(message string) {}

// Ready is a condition on a value this package declared.
func Ready(ok bool) {
	if !ok {
		panic("no")
	}
}
`)
		// A panic this package declared is an ordinary call, and an ordinary
		// call statement is deleted like any other.
		if !got.has("delete-call-statement", `panic("no")`, "") {
			t.Errorf("scan found %v, want a shadowed panic deleted like any other call", got.rules())
		}
	})
}

// TestAScanWithoutTheCheckersRecordRefusesEveryTypeQuestion is the one rule
// four predicates share, and the reason each of them spells it out.
//
// A scan always has go/types' record in hand, so this is a path the walk does
// not take. It is pinned anyway because of which way it has to fail: every one
// of these predicates is asked "may this be mutated", and a predicate that
// answered yes with no record would turn an absent checker into a licence. The
// name of an identifier proves nothing on its own — `nil`, `true` and `panic`
// are ordinary names a package may declare for itself — and without the record
// there is nothing else to ask.
func TestAScanWithoutTheCheckersRecordRefusesEveryTypeQuestion(t *testing.T) {
	t.Parallel()

	blind := &fileScan{}
	if got := blind.typeOf(ast.NewIdent("x")); got != nil {
		t.Errorf("typeOf with no record = %v, want nil", got)
	}
	if blind.isNilLiteral(ast.NewIdent("nil")) {
		t.Error("isNilLiteral with no record accepted an identifier spelled nil")
	}
	if blind.isUniverseConst(ast.NewIdent("true")) {
		t.Error("isUniverseConst with no record accepted an identifier spelled true")
	}
	if blind.isBuiltinCall(&ast.CallExpr{Fun: ast.NewIdent("panic")}, "panic") {
		t.Error("isBuiltinCall with no record accepted a call spelled panic")
	}
}

// TestThePredeclaredNamesAreAskedOfTheCheckerAndNotOfTheSpelling is the rule
// three predicates share, from the side a fixture of ordinary Go cannot reach.
//
// `nil`, `true` and `panic` are ordinary identifiers that a package may declare
// for itself, and each predicate resolves the identifier and asks which scope
// the object belongs to. A shadowed one is what proves the resolution is load
// bearing: with the name alone as evidence, every one of these would say yes.
func TestThePredeclaredNamesAreAskedOfTheCheckerAndNotOfTheSpelling(t *testing.T) {
	t.Parallel()

	t.Run("a nil the package declared is not the predeclared one", func(t *testing.T) {
		t.Parallel()

		p := probeSource(t, "var nil = 1", "nil")
		if (&fileScan{info: p.info}).isNilLiteral(p.expr) {
			t.Error("isNilLiteral accepted a package's own nil")
		}
	})

	t.Run("the predeclared nil is", func(t *testing.T) {
		t.Parallel()

		p := probeSource(t, "var e error", "e == nil")
		binary, isBinary := p.expr.(*ast.BinaryExpr)
		if !isBinary {
			t.Fatalf("the fixture yielded %T, want a comparison", p.expr)
		}
		if !(&fileScan{info: p.info}).isNilLiteral(binary.Y) {
			t.Error("isNilLiteral refused the predeclared nil")
		}
	})

	t.Run("a call is of the builtin the caller named and of no other", func(t *testing.T) {
		t.Parallel()

		p := probeSource(t, "var xs []int", "len(xs)")
		call, isCall := p.expr.(*ast.CallExpr)
		if !isCall {
			t.Fatalf("the fixture yielded %T, want a call", p.expr)
		}
		s := &fileScan{info: p.info}
		if !s.isBuiltinCall(call, "len") {
			t.Error("isBuiltinCall refused the predeclared len")
		}
		if s.isBuiltinCall(call, "panic") {
			t.Error("isBuiltinCall answered for a builtin this call does not call")
		}
	})

}

// TestATypeIsNotAValueTheCheckerRecordedForAnExpression is the second half of
// [fileScan.typeOf], and the half no walk over ordinary source reaches.
//
// The checker records an entry for a type expression as well as for a value
// one -- `int64` in `int64(n)` has a [types.TypeAndValue] of its own -- and the
// two are told apart by [types.TypeAndValue.IsValue] rather than by the
// presence of the entry. A predicate that read the type out of either would
// hand a rule the type of a conversion's target as though it were the type of a
// value, which is a different type in every conversion that does anything.
func TestATypeIsNotAValueTheCheckerRecordedForAnExpression(t *testing.T) {
	t.Parallel()

	p := probeSource(t, "var n int", "int64(n)")
	call, isCall := p.expr.(*ast.CallExpr)
	if !isCall {
		t.Fatalf("the fixture yielded %T, want a conversion", p.expr)
	}
	s := &fileScan{info: p.info}
	if got := s.typeOf(call.Fun); got != nil {
		t.Errorf("typeOf(a type expression) = %v, want nil", got)
	}
	if got := s.typeOf(call); got == nil {
		t.Error("typeOf(a conversion) = nil, want the type it converts to")
	}
}
