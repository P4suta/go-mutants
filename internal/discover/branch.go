// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// The branch proof.
//
// Some edits can only make a condition *less* often true. `<=` becomes `<`,
// `>=` becomes `>`, `||` becomes `&&`, and an `err != nil` test becomes
// `false`: in each of them the mutated condition C′ implies the original C, so
// wherever C was false, C′ is false too. When such an edit sits in the
// condition of an `if` or a `for`, that implication is worth stating as data,
// because it lets a consumer discharge whole tests without running them:
//
//	If no statement of the gated body executed during a test, then C was false
//	every time it was evaluated. C′ ⟹ C, so C′ was false there as well, the
//	branch taken was identical on every evaluation, and — the condition itself
//	having no effects — the mutant and the original ran the same program. The
//	test cannot have observed the mutant, so it need not be executed against it.
//
// [BranchProof] is that statement: the span of the body, in the coordinates
// `go test -coverprofile` reports statement blocks in, so that a consumer
// holding per-test coverage can answer "did this body run" without asking
// go-mutants anything further.
//
// # Why each condition below exists
//
// The walk from the edit to the statement accepts parentheses and the two
// short-circuit connectives and nothing else, because monotonicity is what is
// being carried outward and only those preserve it. `A && B` and `A || B` are
// monotone in both operands, so narrowing an operand narrows the whole; a `!`
// inverts the implication, and a boolean `==` or `!=` destroys it — "less often
// true" becomes "differently true", which proves nothing about either branch.
//
// The *whole* condition has to be inert — no effects, no possible panic,
// guaranteed to terminate — and not merely the part the edit touches, because
// the mutant may evaluate fewer sub-expressions than the original did.
// `X != nil` becomes `false`, which evaluates nothing at all; `A || B` becomes
// `A && B`, which stops evaluating B when A is false; and once an operand
// short-circuits, every operand after it stops being evaluated too. An effect
// or a panic in a sub-expression the mutant skips is an observable difference
// even when both programs take the same branch, which would make the proof
// false. Inertness is decided by an allowlist rather than a denylist, because
// the honest default for a construct nobody has thought about is "no proof".
//
// The body has to hold at least one statement. `cmd/cover` records an empty
// body as a block of zero statements whose coordinates differ between releases
// (Go 1.26.6 starts it one past the `{` and ends it past the `}`, Go 1.27.0
// makes it empty), so "no statement of the body ran" is not a question with one
// answer across profiles — and a branch that does nothing is one no test can
// observe either way, so refusing the proof costs nothing.
//
// A file with a `//line` directive over either brace is refused outright.
// `cmd/cover` attributes a block to the file name the directive gives, so the
// span would be measured in this file's numbering and compared against blocks
// recorded under another's.
//
// # Why the braces, and not the first statement
//
// The span runs from the body's opening brace to its closing brace, inclusive,
// and it has to, because `cmd/cover` does not record a body block the same way
// in every release. For
//
//	if a <= b {
//		return 1
//	}
//
// written on lines 4 to 6, Go 1.26.6 records the body block as `4.12,6.3` — it
// starts at the `{` and ends one past the `}` — while Go 1.27.0 records
// `5.3,6.1`, starting at the first statement. Both were measured on the
// toolchains this repository pins and installs.
//
// `[Lbrace, Rbrace]` is the one span that works under both. It contains the
// body's first recorded block start either way, and no block belonging to code
// outside the body starts inside it under either convention: the `if` or `for`
// header block *ends* at the `{`, an `else` block's recorded start is after the
// `else` keyword, and the block following the whole statement starts after the
// `}`. That is exactly what the consumer's check needs — an instrumented block
// starts inside the span, and no covered block does, therefore the body never
// ran — and it is why the span is the braces rather than the statements between
// them.

// BranchDecreasing is the one direction this phase can prove: the mutated
// condition implies the original one on every evaluation.
const BranchDecreasing = "decreasing"

// A BranchProof is the body an `if` or a `for` gates, attached to a candidate
// whose edit can only narrow that statement's condition.
//
// BodyStart is the body's opening brace and BodyEnd its closing brace, as
// 1-based lines and 1-based byte columns of the pristine file — the same
// coordinate system [Located.Line] and [Located.Column] use, and the one
// `go test -coverprofile` reports statement blocks in.
//
// The contract a consumer may rely on is about the span alone: a test during
// which no statement of that body executed cannot distinguish the mutant from
// the original program. Direction names the lemma the span was derived from and
// is diagnostic. A consumer must never need to read it, so that a later lemma
// can attach a proof of its own without any consumer changing.
type BranchProof struct {
	// Direction is the lemma. Today it is always [BranchDecreasing].
	Direction string
	// BodyStartLine and BodyStartColumn address the opening brace.
	BodyStartLine   int
	BodyStartColumn int
	// BodyEndLine and BodyEndColumn address the closing brace, inclusive.
	BodyEndLine   int
	BodyEndColumn int
}

// decreasingRules is the single source of truth for which edits narrow a
// condition, keyed by the rule name the canonical registry holds.
//
// It is deliberately short and deliberately not derived from the operator
// tables: "this edit implies the original" is a claim about what the mutation
// *means*, and every entry here has been reasoned about one at a time.
// `lt-to-le`, `gt-to-ge` and `and-to-or` widen instead and are absent;
// `true-to-false` narrows a literal but is not tied to any condition's shape;
// `eq-to-neq` and `neq-to-eq` move a condition in neither direction, because
// the two comparisons are true of disjoint sets of inputs rather than nested
// ones.
var decreasingRules = map[string]string{
	"le-to-lt":         BranchDecreasing,
	"ge-to-gt":         BranchDecreasing,
	"or-to-and":        BranchDecreasing,
	ruleNilErrorBranch: BranchDecreasing,
}

// inertBuiltins are the predeclared functions a condition may call. None of
// them can panic, block, allocate observably, or read anything the program
// could tell apart from one evaluation to the next.
var inertBuiltins = map[string]bool{
	"len":     true,
	"cap":     true,
	"min":     true,
	"max":     true,
	"real":    true,
	"imag":    true,
	"complex": true,
}

// branchProof returns the proof one candidate carries, or nil when this phase
// cannot prove anything about it.
//
// Every refusal is silent. A proof is an optimisation a consumer may use, so
// its absence is not a decision a user would look up: recording a skip for
// every condition that calls a function would bury the skips that mean
// "go-mutants declined to mutate this" under ones that mean "go-mutants
// declined to reason about this".
func (s *fileScan) branchProof(rule mutation.Rule, anchor ast.Node) *BranchProof {
	direction, decreasing := decreasingRules[rule.Name]
	if !decreasing || s.info == nil || s.guard == nil {
		return nil
	}
	cond, body, ok := s.gatedBody(anchor)
	if !ok || len(body.List) == 0 || !s.inert(cond) {
		return nil
	}
	start, ok := s.undirectedPosition(body.Lbrace)
	if !ok {
		return nil
	}
	end, ok := s.undirectedPosition(body.Rbrace)
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

// gatedBody walks outward from the edit to the statement whose condition holds
// it, and returns that whole condition together with the body it gates.
//
// The intermediate parents are the ones that preserve the implication:
// parentheses, which change nothing, and the two short-circuit connectives,
// which are monotone in both operands. Everything else ends the walk without a
// proof — a `!`, a comparison, an assignment, a `return`, a call argument, a
// case clause, or the boundary of a function literal, whose body is a statement
// and so is never an expression parent.
//
// An `if`'s init statement is not part of its condition and is never inspected:
// it runs before the condition is evaluated, and the mutant runs it too.
func (s *fileScan) gatedBody(anchor ast.Node) (ast.Expr, *ast.BlockStmt, bool) {
	for node := anchor; node != nil; {
		switch parent := s.guard.parent[node].(type) {
		case *ast.ParenExpr:
			node = parent
		case *ast.BinaryExpr:
			if parent.Op != token.LAND && parent.Op != token.LOR {
				return nil, nil, false
			}
			node = parent
		case *ast.IfStmt:
			if parent.Cond != node || parent.Body == nil {
				return nil, nil, false
			}
			return parent.Cond, parent.Body, true
		case *ast.ForStmt:
			if parent.Cond != node || parent.Body == nil {
				return nil, nil, false
			}
			return parent.Cond, parent.Body, true
		default:
			// Including the nil parent of the file itself, which is how the
			// walk terminates when the edit is not under an `if` or a `for` at
			// all.
			return nil, nil, false
		}
	}
	return nil, nil, false
}

// undirectedPosition is the position of one brace, and the refusal of a file
// whose `//line` directives would make that position name somewhere else.
//
// The two spellings are compared whole. [token.Position] carries the filename,
// the line, and the column, and a directive may move any of them; the offset is
// the same in both readings, so comparing the struct is comparing exactly the
// three fields that matter.
func (s *fileScan) undirectedPosition(pos token.Pos) (token.Position, bool) {
	raw := s.tokFile.PositionFor(pos, false)
	if s.tokFile.PositionFor(pos, true) != raw {
		return token.Position{}, false
	}
	return raw, true
}

// inert reports whether evaluating an expression is guaranteed to have no
// effect, to raise no panic, and to terminate.
//
// It is an allowlist over the syntax, so an expression kind nobody has thought
// about — an index, a slice, a type assertion, a composite literal, a function
// literal — is refused by falling off the end rather than by being remembered.
func (s *fileScan) inert(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident, *ast.BasicLit:
		// Reading a name or a literal. Both are values already.
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
			// `<-` receives from a channel, which blocks, and `&` takes an
			// address, which may move a value to the heap.
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

// inertSelector reports whether a selection reads a value without dereferencing
// anything.
//
// Two shapes are accepted and they are told apart by the type checker rather
// than by the syntax, because `a.b` is written the same way either way. A
// qualified identifier — `pkg.Const`, `pkg.Var` — has no entry in
// [types.Info.Selections] at all and reads a name in another package. A field
// selection has one, and is accepted only when no pointer is dereferenced
// anywhere along the path, embedded promotions included: `p.f` through a
// pointer panics when the pointer is nil, and a mutant that stops evaluating it
// would be observably different from an original that crashed. A method value
// is refused too: it allocates a closure over its receiver.
func (s *fileScan) inertSelector(e *ast.SelectorExpr) bool {
	selection, ok := s.info.Selections[e]
	if !ok {
		ident, isIdent := e.X.(*ast.Ident)
		if !isIdent {
			return false
		}
		_, isPackage := s.info.Uses[ident].(*types.PkgName)
		return isPackage
	}
	if selection.Kind() != types.FieldVal || selection.Indirect() {
		return false
	}
	return s.inert(e.X)
}

// inertBinary reports whether a binary operation can be evaluated without a
// panic.
//
// Three operators need more than their spelling. A division or a remainder
// panics on a zero divisor, so it is admitted only when the divisor is a
// constant — the compiler already refuses a constant zero, which makes the
// check total. A shift is admitted only when the count is a constant, because a
// negative count panics and only a constant one can be ruled out here. And an
// equality panics when it compares interface values whose dynamic types are not
// comparable, which is what [safelyComparable] is about.
func (s *fileScan) inertBinary(e *ast.BinaryExpr) bool {
	switch e.Op {
	case token.LAND, token.LOR,
		token.ADD, token.SUB, token.MUL,
		token.AND, token.OR, token.XOR, token.AND_NOT,
		token.LSS, token.LEQ, token.GTR, token.GEQ:
		// Total on every operand type the compiler admits them for. Integer
		// overflow wraps rather than trapping, and an ordering comparison is
		// only ever written between ordered types.
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

// isConstant reports whether the type checker folded an expression to a
// constant value.
func (s *fileScan) isConstant(expr ast.Expr) bool {
	tv, ok := s.info.Types[expr]
	return ok && tv.Value != nil
}

// safelyComparable reports whether an equality can be evaluated without a
// panic.
//
// A comparison against the predeclared `nil` is always safe: it reads the
// interface's type word, or the pointer, and never the dynamic value.
// Otherwise both operands have to be of a type Go can compare without
// consulting a dynamic type at all.
func (s *fileScan) safelyComparable(e *ast.BinaryExpr) bool {
	if s.isNilLiteral(e.X) || s.isNilLiteral(e.Y) {
		return true
	}
	return comparableWithoutPanic(s.typeOf(e.X)) && comparableWithoutPanic(s.typeOf(e.Y))
}

// comparableWithoutPanic reports whether two values of a type can be compared
// with no possibility of a run-time panic.
//
// [types.Comparable] is the wrong question here, because it answers "does the
// language allow `==`", and the language allows it between interfaces whose
// dynamic types may turn out to be slices. That comparison compiles and panics.
// So the answer is built from the kinds that carry no dynamic type: a basic
// type, a pointer, a channel, and a struct or an array made recursively of
// those. An interface is refused, and so is a type parameter, whose type set
// may admit an interface.
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

// inertCall reports whether a call expression evaluates to a value without
// running any of the program's own code.
//
// Two things wear a call's syntax and are not one. A conversion computes a
// value from a value, and the only conversions that can panic are the ones to
// an array or to a pointer to an array, which check the length of the slice
// they came from. A call of one of the [inertBuiltins] is arithmetic over
// arguments the compiler already knows the shape of. Every other call runs code
// this phase cannot see, so it is refused.
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

// isInertBuiltin reports whether a callee is one of the predeclared functions
// [inertBuiltins] names, and not something of that name the package declared
// for itself.
func (s *fileScan) isInertBuiltin(fun ast.Expr) bool {
	ident, ok := fun.(*ast.Ident)
	if !ok || !inertBuiltins[ident.Name] {
		return false
	}
	builtin, ok := s.info.Uses[ident].(*types.Builtin)
	return ok && builtin.Parent() == types.Universe
}

// inertConversion reports whether a conversion to a type can be evaluated
// without a panic. Only the slice-to-array conversions can fail at run time,
// and they are recognised by their target.
func inertConversion(target types.Type) bool {
	if mayBeArray(target) {
		return false
	}
	if pointer, ok := target.Underlying().(*types.Pointer); ok {
		return !mayBeArray(pointer.Elem())
	}
	return true
}

// mayBeArray reports whether a type is an array, or a type parameter whose type
// set this phase declines to enumerate and which may therefore hold one.
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
