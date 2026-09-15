// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"strconv"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// The verdicts a termination proof can reach.
const (
	// TerminationBounded says the mutant's loop still has a decreasing measure,
	// so it terminates wherever the original did.
	TerminationBounded = "bounded"
	// TerminationUnbounded says the edit removes the measure: there is an input
	// for which the mutant's loop does not terminate.
	TerminationUnbounded = "unbounded"
)

// A TerminationProof is what this phase can say, before anything is executed,
// about whether one mutant's loop still stops.
//
// # Why a proof rather than a measurement
//
// A mutant that never returns is reported as a timeout, which the score counts
// as a detection, so the verdict is already honest. What is not honest is how
// it is *reached*: the run waits out the per-mutant budget, and then waits it
// out again, because a timeout is measured twice before it is believed. On a
// scope whose budget is derived from a slow baseline that is minutes of worker
// time for one mutant, and the only way anybody learns which mutant it was is
// to watch the clock.
//
// But whether a loop's bound survives an edit is not a fact about the machine.
// It is a fact about the syntax and the types, decidable for the shapes Go
// programs are actually written in, and this is the phase that has both. So it
// is decided here, once, and published beside the mutant -- and a run that
// knows a mutant cannot return knows it before it starts one.
//
// # What it is not
//
// It is not a decision to skip anything. A mutant proved unbounded is
// catalogued, instrumented and measured like any other: the proof says what its
// timeout will mean, not whether to have one. Nothing about a verdict changes.
//
// # The shape it recognises, and the one it refuses
//
// One shape: a three-clause `for` whose post statement moves an induction
// variable by a constant step and whose condition compares that variable
// against something the loop does not change. That is the counted loop, and it
// is most of the loops in most Go programs.
//
// Everything else is refused, silently and without a skip: a `range`, a `for`
// with no condition, a condition over two moving variables, a body that assigns
// the bound. A refusal is the absence of a proof and never a claim that the
// loop is fine -- exactly as [BranchProof] is absent rather than negative.
type TerminationProof struct {
	// Verdict is [TerminationBounded] or [TerminationUnbounded].
	Verdict string
	// Reason is one line naming the edit's effect on the measure, for a reader
	// who wants to know why before believing it.
	Reason string
	// LoopLine and LoopColumn address the `for` keyword.
	LoopLine   int
	LoopColumn int
}

// An inductionLoop is a counted `for` this phase understood.
type inductionLoop struct {
	stmt *ast.ForStmt
	// variable is the induction variable's name.
	variable string
	// step is how far one iteration moves it, signed. Never zero: a zero step
	// is not an induction and the loop is refused.
	step int
	// comparison is the condition's operator, with the variable on the left.
	// The condition is normalised so that it always is.
	comparison token.Token
}

// progresses reports whether the loop's measure decreases: the variable moves
// towards the bound the comparison names.
//
// `i < n` with a positive step approaches n from below and stops; `i > 0` with
// a negative step approaches 0 from above and stops. The two crossed pairs --
// `i < n` counting down, `i > 0` counting up -- run away from their bound, and
// are exactly what a negated condition or a flipped step produces.
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

// terminationProof returns what this phase can prove about one candidate's
// loop, or nil when it can prove nothing.
//
// Every refusal is silent, for [BranchProof]'s reason: a proof is an
// optimisation a consumer may use, and recording a skip for every loop this
// phase declined to reason about would bury the skips that mean "go-mutants
// declined to mutate this".
func (s *fileScan) terminationProof(rule mutation.Rule, anchor ast.Node) *TerminationProof {
	if s.guard == nil {
		return nil
	}
	loop, ok := s.enclosingInductionLoop(anchor)
	if !ok || !loop.progresses() {
		// A loop whose measure does not decrease before the edit is one the
		// original program relies on something else to stop. Nothing here can
		// say what an edit does to that.
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

// applyToLoop works out what one edit does to the measure.
//
// The rules it understands are the ones that can touch a measure at all: the
// comparison in the condition, the step in the post statement, and the deletion
// of the post statement. A rule that edits neither leaves the loop as it was,
// which is a proof of `bounded` rather than a refusal -- an arithmetic mutant
// in the body does not stop a counted loop counting.
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
			// The one edit here whose answer does not come from the measure at
			// all. A condition settled false is a loop that runs zero times, so
			// it stops for every input, and the measure it leaves behind is
			// beside the point. The loop is returned unchanged because the
			// caller reads `bounded` off a measure that still decreases, and
			// that reading happens to be right -- but the reason says what is
			// actually true, which is stronger.
			return mutated, "the condition is settled false, so the loop runs zero times", true
		default:
			// eq-to-neq and its neighbours turn a counted comparison into one
			// that is true of a disjoint set rather than a nested one, and
			// nothing here can say what that does to the measure.
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
		// The edit is somewhere else in the loop -- its body, its init. The
		// measure is untouched, and a loop whose measure is untouched still
		// stops.
		return mutated, "the edit is outside the loop's condition and step, which still bound it", true
	}
}

// editsNode reports whether the anchor is inside the given node.
func (s *fileScan) editsNode(anchor ast.Node, within ast.Node) bool {
	if within == nil || anchor == nil {
		return false
	}
	return anchor.Pos() >= within.Pos() && anchor.End() <= within.End()
}

// negatedComparison is what `!` makes of one comparison.
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

// movedComparison is the operator one boundary-moving rule produces.
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

// enclosingInductionLoop walks outward to the nearest `for` and reads it as a
// counted loop, or refuses.
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

// readInductionLoop recognises `for …; v OP bound; v STEP` and nothing else.
func readInductionLoop(loop *ast.ForStmt) (inductionLoop, bool) {
	if loop.Cond == nil || loop.Post == nil {
		// `for {}` and `for cond {}` have no step this phase can reason about.
		return inductionLoop{}, false
	}
	variable, step := readStep(loop.Post)
	if step == 0 {
		return inductionLoop{}, false
	}
	comparison := readComparison(loop.Cond, variable)
	if !isOrdering(comparison) {
		return inductionLoop{}, false
	}
	if assignsWithin(loop.Body, variable) {
		// A body that moves the variable itself is a loop whose measure is not
		// the post statement's, and nothing here can say what it is.
		return inductionLoop{}, false
	}
	if boundMoves(loop) {
		return inductionLoop{}, false
	}
	return inductionLoop{stmt: loop, variable: variable, step: step, comparison: comparison}, true
}

// readStep reads `v++`, `v--`, `v += k` and `v -= k` for a constant k, and
// answers a step of zero for every other post statement.
//
// Zero is the refusal rather than a flag beside it. A step of zero moves the
// variable nowhere, so a loop carrying one has no measure this phase can
// follow and the caller refuses it on that ground alone; a second way to say
// "this is not a step" would be a boundary no statement could put on the wrong
// side of. `i += 0` is written the same way and means the same thing.
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
		// Base zero, which is Go's own rule for an integer literal: `0x2`,
		// `0b10` and `1_000` are steps a person writes, and reading them in
		// base ten alone would leave the loops they bound unproved for a
		// reason nobody could see.
		//
		// A literal the parser accepted and this will not is one that does not
		// fit in an int -- a step of more than nine quintillion is a program
		// the compiler refuses, and this phase reads syntax before anything has
		// type-checked it.
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

// readComparison reads `v OP bound` or `bound OP v`, normalising so the
// variable is on the left, and answers [token.ILLEGAL] for every other
// condition.
//
// ILLEGAL is the refusal rather than a flag beside it, for [readStep]'s reason:
// the caller has to ask [isOrdering] of the answer anyway -- a condition that
// compares the variable with `==` is read here and is still not a measure -- and
// isOrdering answers no for ILLEGAL like any other token that does not order.
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

// isOrdering reports whether an operator orders its operands.
func isOrdering(op token.Token) bool {
	switch op {
	case token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	default:
		return false
	}
}

// mirrored is the operator that means the same with the operands swapped.
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

// identName is one expression read as a bare identifier.
func identName(expr ast.Expr) (string, bool) {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return "", false
	}
	return ident.Name, true
}

// mentions reports whether a name appears anywhere in an expression.
func mentions(expr ast.Expr, name string) bool {
	for node := range ast.Preorder(expr) {
		if ident, ok := node.(*ast.Ident); ok && ident.Name == name {
			return true
		}
	}
	return false
}

// assignsWithin reports whether a block assigns to the named variable.
//
// A nested function literal is walked too: a closure that moves the variable
// moves it, and whether it is called is not something this phase decides.
func assignsWithin(block *ast.BlockStmt, name string) bool {
	if block == nil {
		return false
	}
	for node := range ast.Preorder(block) {
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

// boundMoves reports whether the loop's condition compares against something
// its own body assigns.
//
// `for i := 0; i < n; i++ { n = f() }` is a loop whose bound is not a bound,
// and nothing here can say whether an edit to it terminates.
func boundMoves(loop *ast.ForStmt) bool {
	binary, ok := loop.Cond.(*ast.BinaryExpr)
	if !ok {
		return true
	}
	for _, side := range []ast.Expr{binary.X, binary.Y} {
		for node := range ast.Preorder(side) {
			ident, isIdent := node.(*ast.Ident)
			if isIdent && assignsWithin(loop.Body, ident.Name) {
				return true
			}
		}
	}
	return false
}
