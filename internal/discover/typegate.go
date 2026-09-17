// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/types"
)

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

func isInteger(t types.Type) bool { return basicInfo(t)&types.IsInteger != 0 }

func isFloat(t types.Type) bool { return basicInfo(t)&types.IsFloat != 0 }

func isNumeric(t types.Type) bool { return basicInfo(t)&types.IsNumeric != 0 }

func isStringy(t types.Type) bool { return basicInfo(t)&types.IsString != 0 }

func isBoolClassed(t types.Type) bool { return basicInfo(t)&types.IsBoolean != 0 }

func isUniverseBool(t types.Type) bool {
	return t == types.Typ[types.Bool] || t == types.Typ[types.UntypedBool]
}

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

var errorType = types.Universe.Lookup("error").Type()

var errorInterface = errorType.Underlying().(*types.Interface)

func isExactlyError(t types.Type) bool {
	return t != nil && types.Identical(t, errorType)
}

func implementsError(t types.Type) bool {
	return t != nil && types.Implements(t, errorInterface)
}

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

func (s *fileScan) isNilLiteral(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok || ident.Name != "nil" || s.info == nil {
		return false
	}
	obj := s.info.Uses[ident]
	nilObj, ok := obj.(*types.Nil)
	return ok && nilObj.Parent() == types.Universe
}

func (s *fileScan) isUniverseConst(ident *ast.Ident) bool {
	if s.info == nil {
		return false
	}
	konst, ok := s.info.Uses[ident].(*types.Const)
	return ok && konst.Parent() == types.Universe
}

func (s *fileScan) isBuiltinCall(call *ast.CallExpr, name string) bool {
	ident, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok || ident.Name != name || s.info == nil {
		return false
	}
	builtin, ok := s.info.Uses[ident].(*types.Builtin)
	return ok && builtin.Parent() == types.Universe
}
