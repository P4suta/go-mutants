// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/P4suta/go-mutants/internal/mutation"
)

const BranchDecreasing = "decreasing"

type BranchProof struct {
	Direction       string
	BodyStartLine   int
	BodyStartColumn int
	BodyEndLine     int
	BodyEndColumn   int
}

var decreasingRules = map[string]string{
	"le-to-lt":               BranchDecreasing,
	"ge-to-gt":               BranchDecreasing,
	"or-to-and":              BranchDecreasing,
	ruleNilErrorBranch:       BranchDecreasing,
	ruleConditionToFalse:     BranchDecreasing,
	ruleLoopConditionToFalse: BranchDecreasing,
}

var inertBuiltins = map[string]bool{
	"len":     true,
	"cap":     true,
	"min":     true,
	"max":     true,
	"real":    true,
	"imag":    true,
	"complex": true,
}

func (s *fileScan) branchProof(rule mutation.Rule, anchor ast.Node) *BranchProof {
	direction, decreasing := decreasingRules[rule.Name]
	if !decreasing {
		return nil
	}
	cond, body := s.gatedBody(anchor)
	if !s.inert(cond) {
		return nil
	}
	start, ok := s.undirectedPosition(body.start)
	if !ok {
		return nil
	}
	end, ok := s.undirectedPosition(body.end)
	if !ok {
		return nil
	}
	return &BranchProof{
		Direction:       direction,
		BodyStartLine:   start.Line,
		BodyStartColumn: start.Column,
		BodyEndLine:     end.Line,
		BodyEndColumn:   end.Column,
	}
}

func (s *fileScan) gatedBody(anchor ast.Node) (ast.Expr, gatedSpan) {
	for node := anchor; node != nil; {
		switch parent := s.guard.parent[node].(type) {
		case *ast.ParenExpr:
			node = parent
		case *ast.BinaryExpr:
			if parent.Op != token.LAND && parent.Op != token.LOR {
				return nil, gatedSpan{}
			}
			node = parent
		case *ast.IfStmt:
			if len(parent.Body.List) == 0 {
				return nil, gatedSpan{}
			}
			return parent.Cond, blockSpan(parent.Body)
		case *ast.ForStmt:
			if len(parent.Body.List) == 0 {
				return nil, gatedSpan{}
			}
			return parent.Cond, blockSpan(parent.Body)
		case *ast.CaseClause:
			if !slices.Contains(parent.List, exprOf(node)) || len(parent.Body) == 0 {
				return nil, gatedSpan{}
			}
			return exprOf(node), clauseSpan(parent)
		default:
			return nil, gatedSpan{}
		}
	}
	return nil, gatedSpan{}
}

type gatedSpan struct {
	start token.Pos
	end   token.Pos
}

func blockSpan(block *ast.BlockStmt) gatedSpan {
	return gatedSpan{start: block.Lbrace, end: block.Rbrace}
}

func clauseSpan(clause *ast.CaseClause) gatedSpan {
	first := clause.Body[0]
	last := clause.Body[len(clause.Body)-1]
	return gatedSpan{start: first.Pos(), end: last.End() - 1}
}

func exprOf(node ast.Node) ast.Expr {
	expr, _ := node.(ast.Expr)
	return expr
}

func (s *fileScan) undirectedPosition(pos token.Pos) (token.Position, bool) {
	raw := s.tokFile.PositionFor(pos, false)
	if s.tokFile.PositionFor(pos, true) != raw {
		return token.Position{}, false
	}
	return raw, true
}

func (s *fileScan) inert(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident, *ast.BasicLit:
		return true
	case *ast.ParenExpr:
		return s.inert(e.X)
	case *ast.SelectorExpr:
		return s.inertSelector(e)
	case *ast.UnaryExpr:
		switch e.Op {
		case token.NOT, token.SUB, token.ADD, token.XOR:
			return s.inert(e.X)
		default:
			return false
		}
	case *ast.BinaryExpr:
		return s.inertBinary(e)
	case *ast.CallExpr:
		return s.inertCall(e)
	default:
		return false
	}
}

func (s *fileScan) inertSelector(e *ast.SelectorExpr) bool {
	selection, ok := s.info.Selections[e]
	if !ok {
		ident, _ := e.X.(*ast.Ident)
		_, isPackage := s.info.Uses[ident].(*types.PkgName)
		return isPackage
	}
	if selection.Kind() != types.FieldVal || selection.Indirect() {
		return false
	}
	return s.inert(e.X)
}

func (s *fileScan) inertBinary(e *ast.BinaryExpr) bool {
	switch e.Op {
	case token.LAND, token.LOR,
		token.ADD, token.SUB, token.MUL,
		token.AND, token.OR, token.XOR, token.AND_NOT,
		token.LSS, token.LEQ, token.GTR, token.GEQ:
	case token.QUO, token.REM, token.SHL, token.SHR:
		if !s.isConstant(e.Y) {
			return false
		}
	case token.EQL, token.NEQ:
		if !s.safelyComparable(e) {
			return false
		}
	default:
		return false
	}
	return s.inert(e.X) && s.inert(e.Y)
}

func (s *fileScan) isConstant(expr ast.Expr) bool {
	tv, ok := s.info.Types[expr]
	return ok && tv.Value != nil
}

func (s *fileScan) safelyComparable(e *ast.BinaryExpr) bool {
	if s.isNilLiteral(e.X) || s.isNilLiteral(e.Y) {
		return true
	}
	return comparableWithoutPanic(s.typeOf(e.X)) && comparableWithoutPanic(s.typeOf(e.Y))
}

func comparableWithoutPanic(t types.Type) bool {
	if t == nil {
		return false
	}
	if _, isParam := types.Unalias(t).(*types.TypeParam); isParam {
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return u.Kind() != types.UntypedNil && u.Kind() != types.Invalid
	case *types.Pointer, *types.Chan:
		return true
	case *types.Struct:
		for i := range u.NumFields() {
			if !comparableWithoutPanic(u.Field(i).Type()) {
				return false
			}
		}
		return true
	case *types.Array:
		return comparableWithoutPanic(u.Elem())
	default:
		return false
	}
}

func (s *fileScan) inertCall(e *ast.CallExpr) bool {
	fun := ast.Unparen(e.Fun)
	if tv, isType := s.info.Types[fun]; isType && tv.IsType() {
		if !inertConversion(tv.Type) {
			return false
		}
	} else if !s.isInertBuiltin(fun) {
		return false
	}
	for _, arg := range e.Args {
		if !s.inert(arg) {
			return false
		}
	}
	return true
}

func (s *fileScan) isInertBuiltin(fun ast.Expr) bool {
	ident, ok := fun.(*ast.Ident)
	if !ok || !inertBuiltins[ident.Name] {
		return false
	}
	builtin, ok := s.info.Uses[ident].(*types.Builtin)
	return ok && builtin.Parent() == types.Universe
}

func inertConversion(target types.Type) bool {
	if mayBeArray(target) {
		return false
	}
	if pointer, ok := target.Underlying().(*types.Pointer); ok {
		return !mayBeArray(pointer.Elem())
	}
	return true
}

func mayBeArray(t types.Type) bool {
	if t == nil {
		return true
	}
	if _, isParam := types.Unalias(t).(*types.TypeParam); isParam {
		return true
	}
	_, isArray := t.Underlying().(*types.Array)
	return isArray
}
