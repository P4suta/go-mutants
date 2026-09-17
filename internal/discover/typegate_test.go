// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/types"
	"testing"
)

func typeParam(t *testing.T, constraint types.Type) types.Type {
	t.Helper()
	name := types.NewTypeName(0, nil, "T", nil)
	return types.NewTypeParam(name, constraint)
}

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
		if got.has("return-err-to-nil", "nil", "nil") {
			t.Error("a `return nil` produced a return-err-to-nil candidate")
		}
		if !got.has("true-to-false", "true", "false") {
			t.Errorf("scan found %v, want the predeclared true swapped", got.rules())
		}
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
		if !got.has("delete-call-statement", `panic("no")`, "") {
			t.Errorf("scan found %v, want a shadowed panic deleted like any other call", got.rules())
		}
	})
}

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
