// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"strconv"

	"github.com/P4suta/go-mutants/internal/mutation"
)

const (
	TerminationBounded   = "bounded"
	TerminationUnbounded = "unbounded"
)

type TerminationProof struct {
	Verdict    string
	Reason     string
	LoopLine   int
	LoopColumn int
}

type inductionLoop struct {
	stmt       *ast.ForStmt
	variable   string
	step       int
	comparison token.Token
}

func (l inductionLoop) progresses() bool {
	switch l.comparison {
	case token.LSS, token.LEQ:
		return l.step > 0
	case token.GTR, token.GEQ:
		return l.step < 0
	default:
		return false
	}
}

func (s *fileScan) terminationProof(rule mutation.Rule, anchor ast.Node) *TerminationProof {
	if s.guard == nil {
		return nil
	}
	loop, ok := s.enclosingInductionLoop(anchor)
	if !ok || !loop.progresses() {
		return nil
	}
	mutated, reason, ok := s.applyToLoop(loop, rule, anchor)
	if !ok {
		return nil
	}
	position := s.tokFile.PositionFor(loop.stmt.For, false)
	verdict := TerminationUnbounded
	if mutated.progresses() {
		verdict = TerminationBounded
	}
	return &TerminationProof{
		Verdict:    verdict,
		Reason:     reason,
		LoopLine:   position.Line,
		LoopColumn: position.Column,
	}
}

func (s *fileScan) applyToLoop(loop inductionLoop, rule mutation.Rule, anchor ast.Node) (inductionLoop, string, bool) {
	mutated := loop
	switch {
	case s.editsNode(anchor, loop.stmt.Cond):
		switch rule.Name {
		case "negate-loop-condition", "negate-condition":
			mutated.comparison = negatedComparison(loop.comparison)
			if mutated.comparison == token.ILLEGAL {
				return loop, "", false
			}
			return mutated, "the condition is negated, so the variable moves away from the bound rather than towards it", true
		case "lt-to-le", "le-to-lt", "gt-to-ge", "ge-to-gt":
			mutated.comparison = movedComparison(rule.Name)
			return mutated, "the comparison moves by one, which changes how many iterations run and not whether they end", true
		case ruleLoopConditionToFalse:
			return mutated, "the condition is settled false, so the loop runs zero times", true
		default:
			return loop, "", false
		}
	case loop.stmt.Post != nil && s.editsNode(anchor, loop.stmt.Post):
		switch rule.Name {
		case "incr-to-decr", "decr-to-incr", "add-assign-to-sub-assign", "sub-assign-to-add-assign",
			"add-to-sub", "sub-to-add":
			mutated.step = -loop.step
			return mutated, "the step is reversed, so the variable moves away from the bound", true
		case "delete-incdec", "delete-assignment":
			mutated.step = 0
			return mutated, "the step is deleted, so the variable never reaches the bound", true
		default:
			return loop, "", false
		}
	default:
		return mutated, "the edit is outside the loop's condition and step, which still bound it", true
	}
}

func (s *fileScan) editsNode(anchor ast.Node, within ast.Node) bool {
	if within == nil || anchor == nil {
		return false
	}
	return anchor.Pos() >= within.Pos() && anchor.End() <= within.End()
}

func negatedComparison(op token.Token) token.Token {
	switch op {
	case token.LSS:
		return token.GEQ
	case token.LEQ:
		return token.GTR
	case token.GTR:
		return token.LEQ
	case token.GEQ:
		return token.LSS
	default:
		return token.ILLEGAL
	}
}

func movedComparison(rule string) token.Token {
	switch rule {
	case "lt-to-le":
		return token.LEQ
	case "le-to-lt":
		return token.LSS
	case "gt-to-ge":
		return token.GEQ
	case "ge-to-gt":
		return token.GTR
	default:
		return token.ILLEGAL
	}
}

func (s *fileScan) enclosingInductionLoop(anchor ast.Node) (inductionLoop, bool) {
	for node := ast.Node(anchor); node != nil; node = s.guard.parent[node] {
		loop, ok := node.(*ast.ForStmt)
		if !ok {
			if _, isFunc := node.(*ast.FuncDecl); isFunc {
				return inductionLoop{}, false
			}
			if _, isLit := node.(*ast.FuncLit); isLit {
				return inductionLoop{}, false
			}
			continue
		}
		return readInductionLoop(loop)
	}
	return inductionLoop{}, false
}

func readInductionLoop(loop *ast.ForStmt) (inductionLoop, bool) {
	if loop.Cond == nil {
		return inductionLoop{}, false
	}
	variable, step := readStep(loop.Post)
	stepStmt := loop.Post
	if step == 0 {
		variable, step, stepStmt = readBodyStep(loop.Body)
		if step == 0 {
			return inductionLoop{}, false
		}
	}
	comparison := readComparison(loop.Cond, variable)
	if !isOrdering(comparison) {
		return inductionLoop{}, false
	}
	if assignsWithinExcept(loop.Body, variable, stepStmt) {
		return inductionLoop{}, false
	}
	if boundMoves(loop, stepStmt) {
		return inductionLoop{}, false
	}
	return inductionLoop{stmt: loop, variable: variable, step: step, comparison: comparison}, true
}

func readBodyStep(body *ast.BlockStmt) (variable string, step int, stmt ast.Stmt) {
	if body == nil {
		return "", 0, nil
	}
	for node := range ast.Preorder(body) {
		if branch, isBranch := node.(*ast.BranchStmt); isBranch && branch.Tok == token.CONTINUE {
			return "", 0, nil
		}
	}
	for _, candidate := range body.List {
		name, moved := readStep(candidate)
		if moved == 0 {
			continue
		}
		if step != 0 {
			return "", 0, nil
		}
		variable, step, stmt = name, moved, candidate
	}
	return variable, step, stmt
}

func readStep(post ast.Stmt) (variable string, step int) {
	switch statement := post.(type) {
	case *ast.IncDecStmt:
		name, named := identName(statement.X)
		if !named {
			return "", 0
		}
		if statement.Tok == token.INC {
			return name, 1
		}
		return name, -1
	case *ast.AssignStmt:
		if len(statement.Lhs) != 1 || len(statement.Rhs) != 1 {
			return "", 0
		}
		name, named := identName(statement.Lhs[0])
		if !named {
			return "", 0
		}
		literal, isLiteral := statement.Rhs[0].(*ast.BasicLit)
		if !isLiteral || literal.Kind != token.INT {
			return "", 0
		}
		value, err := strconv.ParseInt(literal.Value, 0, 0)
		if err != nil {
			return "", 0
		}
		switch statement.Tok {
		case token.ADD_ASSIGN:
			return name, int(value)
		case token.SUB_ASSIGN:
			return name, int(-value)
		default:
			return "", 0
		}
	default:
		return "", 0
	}
}

func readComparison(cond ast.Expr, variable string) token.Token {
	binary, isBinary := cond.(*ast.BinaryExpr)
	if !isBinary {
		return token.ILLEGAL
	}
	if name, named := identName(binary.X); named && name == variable {
		if mentions(binary.Y, variable) {
			return token.ILLEGAL
		}
		return binary.Op
	}
	if name, named := identName(binary.Y); named && name == variable {
		if mentions(binary.X, variable) {
			return token.ILLEGAL
		}
		return mirrored(binary.Op)
	}
	return token.ILLEGAL
}

func isOrdering(op token.Token) bool {
	switch op {
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	default:
		return false
	}
}

func mirrored(op token.Token) token.Token {
	switch op {
	case token.LSS:
		return token.GTR
	case token.LEQ:
		return token.GEQ
	case token.GTR:
		return token.LSS
	case token.GEQ:
		return token.LEQ
	default:
		return token.ILLEGAL
	}
}

func identName(expr ast.Expr) (string, bool) {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return "", false
	}
	return ident.Name, true
}

func mentions(expr ast.Expr, name string) bool {
	for node := range ast.Preorder(expr) {
		if ident, ok := node.(*ast.Ident); ok && ident.Name == name {
			return true
		}
	}
	return false
}

func assignsWithin(block *ast.BlockStmt, name string) bool {
	return assignsWithinExcept(block, name, nil)
}

func assignsWithinExcept(block *ast.BlockStmt, name string, except ast.Stmt) bool {
	if block == nil {
		return false
	}
	for node := range ast.Preorder(block) {
		if except != nil && node == ast.Node(except) {
			continue
		}
		switch statement := node.(type) {
		case *ast.IncDecStmt:
			if ident, ok := statement.X.(*ast.Ident); ok && ident.Name == name {
				return true
			}
		case *ast.AssignStmt:
			for _, target := range statement.Lhs {
				if ident, ok := target.(*ast.Ident); ok && ident.Name == name {
					return true
				}
			}
		}
	}
	return false
}

func boundMoves(loop *ast.ForStmt, step ast.Stmt) bool {
	binary, ok := loop.Cond.(*ast.BinaryExpr)
	if !ok {
		return true
	}
	for _, side := range []ast.Expr{binary.X, binary.Y} {
		for node := range ast.Preorder(side) {
			ident, isIdent := node.(*ast.Ident)
			if isIdent && assignsWithinExcept(loop.Body, ident.Name, step) {
				return true
			}
		}
	}
	return false
}
