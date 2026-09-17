// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
)

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

func (g *guardResolver) effectFree(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident, *ast.BasicLit, *ast.FuncLit:
		return true
	case *ast.ParenExpr:
		return g.effectFree(e.X)
	case *ast.SelectorExpr:
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

func (g *guardResolver) effectFreeOrAbsent(expr ast.Expr) bool {
	return expr == nil || g.effectFree(expr)
}

func (g *guardResolver) effectFreeCall(call *ast.CallExpr) bool {
	if !g.isConversion(call) && !effectFreeBuiltins[g.builtinName(call)] {
		return false
	}
	return g.argumentsAre(call, g.effectFree)
}

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

func (g *guardResolver) panicFreeSelector(sel *ast.SelectorExpr) bool {
	if g.info == nil {
		return false
	}
	selection, resolved := g.info.Selections[sel]
	if !resolved {
		base, _ := sel.X.(*ast.Ident)
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

func (g *guardResolver) panicFreeCall(call *ast.CallExpr) bool {
	if g.isConversion(call) {
		return g.panicFreeConversion(call)
	}
	if !panicFreeBuiltins[g.builtinName(call)] {
		return false
	}
	return g.argumentsAre(call, g.panicFree)
}

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

func (g *guardResolver) hashableKeys(lit *ast.CompositeLit) bool {
	m, isMap := underlyingOf(g.typeOf(lit)).(*types.Map)
	if !isMap {
		return true
	}
	return comparesWithoutPanic(m.Key())
}

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

func (g *guardResolver) isConversion(call *ast.CallExpr) bool {
	return g.isTypeExpr(ast.Unparen(call.Fun))
}

func (g *guardResolver) builtinName(call *ast.CallExpr) string {
	if g.info == nil {
		return ""
	}
	ident, isIdent := call.Fun.(*ast.Ident)
	if !isIdent {
		return ""
	}
	builtin, isBuiltin := g.info.Uses[ident].(*types.Builtin)
	if !isBuiltin || builtin.Parent() != types.Universe {
		return ""
	}
	return builtin.Name()
}

func (g *guardResolver) isTypeExpr(expr ast.Expr) bool {
	if g.info == nil || expr == nil {
		return false
	}
	tv, known := g.info.Types[expr]
	return known && tv.IsType()
}

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

func (g *guardResolver) introducesPanic(anchor ast.Node, replacement string) bool {
	if replacement != "/" && replacement != "%" {
		return false
	}
	binary, ok := anchor.(*ast.BinaryExpr)
	if !ok {
		return true
	}
	if floatingResult(g.typeOf(binary)) {
		return false
	}
	divisor := g.constantValue(binary.Y)
	return divisor == nil || constant.Sign(divisor) == 0
}

func comparesWithoutPanic(t types.Type) bool {
	switch underlyingOf(t).(type) {
	case *types.Basic, *types.Pointer, *types.Chan:
		return true
	default:
		return false
	}
}

func floatingResult(t types.Type) bool {
	return basicInfo(t)&(types.IsFloat|types.IsComplex) != 0
}

func underlyingOf(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	return t.Underlying()
}

func (g *guardResolver) inertContext(expr ast.Expr) bool {
	for node := ast.Node(expr); node != nil; node = g.parent[node] {
		stmt, ok := node.(ast.Stmt)
		if !ok {
			continue
		}
		for _, operand := range statementOperands(stmt) {
			if operand != nil && !g.effectFree(operand) {
				return false
			}
		}
		return true
	}
	return false
}

func statementOperands(stmt ast.Stmt) []ast.Expr {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		return []ast.Expr{s.X}
	case *ast.AssignStmt:
		return append(append([]ast.Expr{}, s.Lhs...), s.Rhs...)
	case *ast.ReturnStmt:
		return s.Results
	case *ast.IncDecStmt:
		return []ast.Expr{s.X}
	case *ast.SendStmt:
		return []ast.Expr{s.Chan, s.Value}
	case *ast.GoStmt:
		return []ast.Expr{s.Call}
	case *ast.DeferStmt:
		return []ast.Expr{s.Call}
	case *ast.IfStmt:
		return []ast.Expr{s.Cond}
	case *ast.ForStmt:
		return []ast.Expr{s.Cond}
	case *ast.RangeStmt:
		return []ast.Expr{s.Key, s.Value, s.X}
	case *ast.SwitchStmt:
		return []ast.Expr{s.Tag}
	case *ast.TypeSwitchStmt:
		return nil
	case *ast.CaseClause:
		return s.List
	case *ast.SelectStmt, *ast.CommClause:
		return nil
	case *ast.BlockStmt, *ast.DeclStmt, *ast.LabeledStmt, *ast.BranchStmt,
		*ast.EmptyStmt, *ast.BadStmt:
		return nil
	default:
		return nil
	}
}
