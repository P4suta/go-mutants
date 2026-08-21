// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/token"
	"go/types"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"

	"golang.org/x/tools/go/packages"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// generatedMarker is the convention every Go code generator follows:
// https://go.dev/s/generatedcode. The line has to appear before the package
// clause, which is checked separately.
var generatedMarker = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// pkg walks one loaded package.
func (d *discovery) pkg(loaded *loadResult, pkg *packages.Package) error {
	if d.cgo.covers(pkg) {
		d.recordCgoPackage(pkg)
		return nil
	}
	for _, file := range pkg.Syntax {
		if err := d.file(loaded, pkg, file); err != nil {
			return err
		}
	}
	return nil
}

// recordCgoPackage skips every file of a package that imports "C", naming each
// one rather than the package: a skip is something a user looks up by path.
func (d *discovery) recordCgoPackage(pkg *packages.Package) {
	for _, ref := range moduleFiles(pkg, d.root) {
		if d.seen[ref.rel] {
			continue
		}
		d.seen[ref.rel] = true
		if reason, excluded := d.selection(ref.rel); excluded {
			d.record(ref.rel, reason, 1)
			continue
		}
		d.record(ref.rel, SkipCgo, 1)
	}
}

// file walks one syntax tree, or records why it did not.
func (d *discovery) file(loaded *loadResult, pkg *packages.Package, file *ast.File) error {
	tokFile := loaded.fset.File(file.Package)
	if tokFile == nil {
		return nil
	}
	abs := tokFile.Name()
	// A test file is never mutated and never recorded: that is structural,
	// not a decision about this particular file.
	if isTestFile(abs) {
		return nil
	}
	// Anything outside the module root is somebody else's source: a dependency,
	// the standard library, or the cgo preprocessor's output in the build
	// cache. None of it is ours to mutate, and none of it is worth a skip.
	rel, ok := relativePath(d.root, abs)
	if !ok {
		return nil
	}
	if d.seen[rel] {
		return nil
	}
	d.seen[rel] = true

	if reason, excluded := d.selection(rel); excluded {
		d.record(rel, reason, 1)
		return nil
	}
	if isGenerated(file) {
		d.record(rel, SkipGenerated, 1)
		return nil
	}
	if d.matchers.empty() {
		return nil
	}

	src, err := os.ReadFile(abs)
	if err != nil {
		return &Error{Code: CodeFileUnreadable, Message: "cannot read " + strconv.Quote(rel), Err: err}
	}
	if uint64(len(src)) > math.MaxUint32 {
		return &Error{
			Code:    CodeFileUnreadable,
			Message: strconv.Quote(rel) + " is larger than 4 GiB, which mutant spans cannot address",
		}
	}
	scan := &fileScan{
		discovery:    d,
		rel:          rel,
		pkgPath:      packagePath(pkg),
		src:          src,
		digest:       mutation.Digest(src),
		tokFile:      tokFile,
		info:         pkg.TypesInfo,
		suppressions: collectSuppressions(file, pkg.TypesInfo),
		guard:        newGuardResolver(file, pkg.TypesInfo, pkg.Types, tokFile),
	}
	return scan.walk(file)
}

// selection applies the include and exclude patterns to a module-relative
// path. Excludes are applied after includes, so an exclude always wins, and an
// empty include set includes everything.
func (d *discovery) selection(rel string) (SkipReason, bool) {
	if len(d.include) > 0 {
		included := false
		for _, pattern := range d.include {
			if pattern.Match(rel) {
				included = true
				break
			}
		}
		if !included {
			return SkipExcluded, true
		}
	}
	for _, pattern := range d.exclude {
		if pattern.Match(rel) {
			return SkipExcluded, true
		}
	}
	return "", false
}

// isGenerated reports whether a file claims to be generated.
//
// [ast.IsGenerated] answers the same question and answers it correctly, CRLF
// line endings included — go/scanner strips the carriage return from a
// //-comment before the literal is ever stored, so the marker's "DO NOT EDIT."
// suffix matches on a Windows checkout too. The check is spelled out here
// anyway because "generated" is a [SkipReason] this package reports and has to
// keep reporting the same way: both halves of the convention — the anchored
// marker line and the requirement that it precede the package clause — are
// written down in one place under this package's control, rather than tracking
// a standard-library helper whose exact semantics are free to shift.
func isGenerated(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() > file.Package {
			break
		}
		for _, comment := range group.List {
			if comment.Pos() > file.Package {
				break
			}
			if generatedMarker.MatchString(comment.Text) {
				return true
			}
		}
	}
	return false
}

// A suppression is one region of a file discovery refuses to descend into.
type suppression struct {
	start  token.Pos
	end    token.Pos
	reason SkipReason
}

// width is the region's size in bytes, which is how two nested regions are
// ordered.
func (s suppression) width() int { return int(s.end - s.start) }

// collectSuppressions collects every region of a file that cannot hold a mutable
// expression, together with the reason.
//
// Regions are collected rather than enforced during the emitting walk because
// two of them cover only part of a node — an array's length but not its
// element type, a case clause's labels but not its body — and a walk that had
// to remember which child slot it was in would be one `switch` away from
// silently mutating a case label.
func collectSuppressions(file *ast.File, info *types.Info) []suppression {
	var out []suppression
	add := func(from, to token.Pos, reason SkipReason) {
		if from.IsValid() && to.IsValid() && to > from {
			out = append(out, suppression{start: from, end: to, reason: reason})
		}
	}

	// Package-level variable initialisers are read from the declaration list
	// and not from the walk, because a `var` inside a function body is
	// ordinary code that the same syntax node would otherwise catch.
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Values) == 0 {
				continue
			}
			add(value.Values[0].Pos(), value.Values[len(value.Values)-1].End(), SkipPackageVarInit)
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.GenDecl:
			if n.Tok == token.CONST {
				add(n.Pos(), n.End(), SkipConstDecl)
			}
		case *ast.ArrayType:
			if n.Len != nil {
				add(n.Len.Pos(), n.Len.End(), SkipArrayLength)
			}
		case *ast.CaseClause:
			if len(n.List) > 0 {
				add(n.List[0].Pos(), n.List[len(n.List)-1].End(), SkipCaseLabel)
			}
		case *ast.CommClause:
			if n.Comm != nil {
				add(n.Comm.Pos(), n.Comm.End(), SkipCaseLabel)
			}
		case *ast.FuncDecl:
			if n.Type != nil && n.Type.TypeParams != nil {
				add(n.Type.TypeParams.Pos(), n.Type.TypeParams.End(), SkipTypeParam)
			}
		case *ast.TypeSpec:
			if n.TypeParams != nil {
				add(n.TypeParams.Pos(), n.TypeParams.End(), SkipTypeParam)
			}
		case *ast.IndexExpr:
			// A single explicit type argument. The same syntax is an ordinary
			// index expression when the index is a value, which is why this
			// asks the type checker instead of guessing: `m[true]` is runtime
			// code and stays mutable.
			if isTypeExpr(info, n.Index) {
				add(n.Index.Pos(), n.Index.End(), SkipTypeParam)
			}
		case *ast.IndexListExpr:
			for _, index := range n.Indices {
				if isTypeExpr(info, index) {
					add(index.Pos(), index.End(), SkipTypeParam)
				}
			}
		}
		return true
	})

	slices.SortFunc(out, func(x, y suppression) int {
		if x.start != y.start {
			return int(x.start - y.start)
		}
		if x.end != y.end {
			return int(y.end - x.end)
		}
		return reasonRank[x.reason] - reasonRank[y.reason]
	})
	return out
}

// isTypeExpr reports whether an expression denotes a type rather than a value.
func isTypeExpr(info *types.Info, expr ast.Expr) bool {
	if info == nil || expr == nil {
		return false
	}
	tv, ok := info.Types[expr]
	return ok && tv.IsType()
}

// A fileScan is the emitting walk over one file.
type fileScan struct {
	*discovery
	rel          string
	pkgPath      string
	src          []byte
	digest       string
	tokFile      *token.File
	info         *types.Info
	suppressions []suppression
	guard        *guardResolver
}

// walk emits every candidate in the file, or records why it did not.
//
// One switch over the syntax, one case per node kind a family can be anchored
// to. Several families share a node kind — a binary expression is where the
// comparison, connective, arithmetic, bitwise, and nil-error rules all look —
// and each of them asks the type checker its own question there, which is why
// the dispatch is by node and the discrimination is by type.
func (s *fileScan) walk(file *ast.File) error {
	var failure error
	ast.Inspect(file, func(node ast.Node) bool {
		if failure != nil || node == nil {
			return false
		}
		switch n := node.(type) {
		case *ast.BinaryExpr:
			failure = s.binaryExpr(n)
		case *ast.UnaryExpr:
			failure = s.unaryExpr(n)
		case *ast.Ident:
			failure = s.booleanLiteral(n)
		case *ast.IfStmt:
			failure = s.negateCondition(n.Cond, ruleNegateCondition)
		case *ast.ForStmt:
			failure = s.negateCondition(n.Cond, ruleNegateLoopCondition)
		case *ast.ReturnStmt:
			failure = s.returnStmt(n)
		case *ast.AssignStmt:
			failure = s.assignStmt(n)
		case *ast.IncDecStmt:
			failure = s.incDecStmt(n)
		case *ast.ExprStmt:
			failure = s.exprStmt(n)
		}
		return failure == nil
	})
	return failure
}

// binaryExpr emits every candidate anchored on one binary expression.
//
// The arithmetic families are the reason the operand types are read once here
// rather than inside each swap: `+` is an integer rule, a float rule, or
// neither, and string concatenation is excluded because its operands are
// strings — never because a quote was spotted near the operator.
func (s *fileScan) binaryExpr(n *ast.BinaryExpr) error {
	if err := s.swap(s.matchers.comparison, n, n.Op, n.OpPos); err != nil {
		return err
	}
	if err := s.swap(s.matchers.connective, n, n.Op, n.OpPos); err != nil {
		return err
	}
	left, right := s.typeOf(n.X), s.typeOf(n.Y)
	if isInteger(left) && isInteger(right) {
		if err := s.swap(s.matchers.integer, n, n.Op, n.OpPos); err != nil {
			return err
		}
	}
	if isFloat(left) && isFloat(right) {
		if err := s.swap(s.matchers.float, n, n.Op, n.OpPos); err != nil {
			return err
		}
	}
	if err := s.bitwise(n, left, right); err != nil {
		return err
	}
	return s.nilErrorBranch(n)
}

// bitwise emits the bitwise swap, if the operator is one and the operands allow
// it.
//
// A shift is gated on its left operand alone. The count is an operand of a
// different kind — it may be any integer type and is never what the rule
// rewrites — so requiring it to match the shifted value would refuse
// `x << shift` for no reason connected to the mutation.
func (s *fileScan) bitwise(n *ast.BinaryExpr, left, right types.Type) error {
	switch n.Op {
	case token.SHL, token.SHR:
		if !isInteger(left) {
			return nil
		}
	default:
		if !isInteger(left) || !isInteger(right) {
			return nil
		}
	}
	return s.swap(s.matchers.bitwise, n, n.Op, n.OpPos)
}

// nilErrorBranch emits the rule that makes an `if err != nil` branch stop
// firing.
//
// The whole comparison is replaced with `false` rather than the operator being
// swapped, because the point is a branch that never runs: `err == nil` would
// only move the failure to the other arm, which the comparison family already
// covers.
func (s *fileScan) nilErrorBranch(n *ast.BinaryExpr) error {
	rule, ok := s.matchers.rule(ruleNilErrorBranch)
	if !ok || n.Op != token.NEQ {
		return nil
	}
	var other ast.Expr
	switch {
	case s.isNilLiteral(n.Y):
		other = n.X
	case s.isNilLiteral(n.X):
		other = n.Y
	default:
		return nil
	}
	if !implementsError(s.typeOf(other)) {
		return nil
	}
	return s.emitNode(rule, n, "false")
}

// unaryExpr emits the negation-removal rule.
//
// The span is the whole unary expression and the replacement is the operand's
// own bytes, so the edit is exactly "delete the `!`" however much whitespace or
// commentary sits between the two. Removing an operator can never remove a line
// break, which is what keeps this line-preserving.
func (s *fileScan) unaryExpr(n *ast.UnaryExpr) error {
	rule, ok := s.matchers.rule(ruleRemoveNegation)
	if !ok || n.Op != token.NOT || !isBoolClassed(s.typeOf(n.X)) {
		return nil
	}
	operand, ok := s.text(n.X)
	if !ok {
		return nil
	}
	return s.emitNode(rule, n, operand)
}

// booleanLiteral emits the boolean-literal swap for a predeclared `true` or
// `false`.
func (s *fileScan) booleanLiteral(n *ast.Ident) error {
	matcher, ok := s.matchers.boolean[n.Name]
	if !ok || !s.isUniverseConst(n) {
		return nil
	}
	return s.emit(matcher.rule, n, n.Pos(), n.Name, matcher.replacement)
}

// negateCondition wraps an `if` or `for` condition in a negation.
//
// The gate is "boolean underneath" rather than "the universe bool", because `!`
// applies to any boolean type: a condition of a named boolean type is still
// negatable, even though the guard around it cannot be Form C. Wrapping the
// original bytes in `!(…)` rather than re-rendering the condition is what keeps
// comments, spacing, and line count intact.
func (s *fileScan) negateCondition(cond ast.Expr, name string) error {
	rule, ok := s.matchers.rule(name)
	if !ok || cond == nil || !isBoolClassed(s.typeOf(cond)) {
		return nil
	}
	original, ok := s.text(cond)
	if !ok {
		return nil
	}
	return s.emitNode(rule, cond, "!("+original+")")
}

// returnStmt emits the return-replacement and error-swallowing rules for every
// value of one `return`.
//
// A bare `return` in a function with named results is passed over in silence:
// there are no bytes to replace, so there is no candidate and nothing was
// decided against. A `return f()` whose single call fills several results is
// passed over too — the values and the declared results cannot be lined up
// one to one, and replacing the whole call would be a different edit than the
// one this family describes.
func (s *fileScan) returnStmt(n *ast.ReturnStmt) error {
	results := s.enclosingResults(n)
	if len(n.Results) == 0 || results == nil || results.Len() != len(n.Results) {
		return nil
	}
	for i, value := range n.Results {
		if err := s.returnValue(value, results.At(i).Type()); err != nil {
			return err
		}
	}
	return nil
}

// returnValue emits whichever replacement the declared result type admits.
//
// The nillable results are split between two families, and the split is stated
// on both sides so that neither can widen without the other narrowing: a value
// whose static type is exactly `error` belongs to error-swallowing, and every
// other nillable result belongs to return-replacement. `return &myErr{}` from a
// function returning `error` is therefore a `return-nil` candidate — the value
// is a concrete pointer, not an error interface value — while `return err` is
// the `return-err-to-nil` the family exists for.
func (s *fileScan) returnValue(value ast.Expr, declared types.Type) error {
	if isExactlyError(s.typeOf(value)) {
		return s.replaceReturn(value, ruleReturnErrToNil, "nil")
	}
	switch {
	case isNumeric(declared):
		return s.replaceReturn(value, ruleReturnZeroNumeric, "0")
	case isStringy(declared):
		return s.replaceReturn(value, ruleReturnEmptyString, `""`)
	case isBoolClassed(declared):
		if err := s.replaceReturn(value, ruleReturnTrue, "true"); err != nil {
			return err
		}
		return s.replaceReturn(value, ruleReturnFalse, "false")
	case isNillable(declared):
		// Not an error-typed value: that was settled above.
		return s.replaceReturn(value, ruleReturnNil, "nil")
	default:
		return nil
	}
}

// replaceReturn emits one return-value replacement, unless the value is already
// spelled exactly that way.
//
// The catalogue would refuse a replacement equal to its original anyway, and
// refusing it loudly there would turn every `return nil` in the tree into a
// failed run. It is not a skip either: `return 0` is not a place go-mutants
// declined to mutate, it is a place where the mutation and the source are the
// same program.
func (s *fileScan) replaceReturn(value ast.Expr, name, replacement string) error {
	rule, ok := s.matchers.rule(name)
	if !ok {
		return nil
	}
	original, ok := s.text(value)
	if !ok || original == replacement {
		return nil
	}
	return s.emitNode(rule, value, replacement)
}

// enclosingResults returns the declared results of the function a node sits in.
func (s *fileScan) enclosingResults(node ast.Node) *types.Tuple {
	if s.guard == nil || s.info == nil {
		return nil
	}
	for n := node; n != nil; n = s.guard.parent[n] {
		var signature types.Type
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if obj := s.info.Defs[fn.Name]; obj != nil {
				signature = obj.Type()
			}
		case *ast.FuncLit:
			if tv, ok := s.info.Types[fn]; ok {
				signature = tv.Type
			}
		default:
			continue
		}
		sig, ok := signature.(*types.Signature)
		if !ok {
			return nil
		}
		return sig.Results()
	}
	return nil
}

// assignStmt emits the compound-assignment swap and the assignment deletion.
func (s *fileScan) assignStmt(n *ast.AssignStmt) error {
	if matcher, ok := s.matchers.assignOp[n.Tok]; ok && len(n.Lhs) == 1 {
		if target := s.typeOf(n.Lhs[0]); isInteger(target) || isFloat(target) {
			if err := s.emit(matcher.rule, n, n.TokPos, matcher.original, matcher.replacement); err != nil {
				return err
			}
		}
	}
	// Only a plain `=` is deleted. A `:=` declares, and deleting a declaration
	// makes every later use of the name a compile error rather than a mutant.
	if rule, ok := s.matchers.rule(ruleDeleteAssignment); ok && n.Tok == token.ASSIGN {
		return s.emitNode(rule, n, "")
	}
	return nil
}

// incDecStmt emits the `++`/`--` swap and the statement deletion.
func (s *fileScan) incDecStmt(n *ast.IncDecStmt) error {
	if matcher, ok := s.matchers.incDec[n.Tok]; ok {
		if target := s.typeOf(n.X); isInteger(target) || isFloat(target) {
			if err := s.emit(matcher.rule, n, n.TokPos, matcher.original, matcher.replacement); err != nil {
				return err
			}
		}
	}
	if rule, ok := s.matchers.rule(ruleDeleteIncDec); ok {
		return s.emitNode(rule, n, "")
	}
	return nil
}

// exprStmt emits the call-statement deletion.
//
// A `panic(…)` statement is left alone, and so is the `(panic)(…)` the same
// call may be written as. Deleting one removes the only reason the function
// ends there, so every path that fell through it now reaches the closing brace
// without a return — a compile error, manufactured wholesale in exactly the
// defensive code where a deleted call would otherwise be an interesting mutant.
// A `panic` the package shadowed with a function of its own is an ordinary call
// and is deleted like any other.
func (s *fileScan) exprStmt(n *ast.ExprStmt) error {
	rule, ok := s.matchers.rule(ruleDeleteCallStatement)
	if !ok {
		return nil
	}
	call, ok := n.X.(*ast.CallExpr)
	if !ok || s.isBuiltinCall(call, "panic") {
		return nil
	}
	return s.emitNode(rule, n, "")
}

// swap emits one operator-token rewrite from a family table.
func (s *fileScan) swap(table map[token.Token]tokenMatcher, anchor ast.Node, op token.Token, pos token.Pos) error {
	matcher, ok := table[op]
	if !ok {
		return nil
	}
	return s.emit(matcher.rule, anchor, pos, matcher.original, matcher.replacement)
}

// text returns the pristine bytes of a node, as a string.
func (s *fileScan) text(node ast.Node) (string, bool) {
	start := s.tokFile.Offset(node.Pos())
	end := s.tokFile.Offset(node.End())
	if start < 0 || end < start || end > len(s.src) {
		return "", false
	}
	return string(s.src[start:end]), true
}

// emitNode records a candidate whose span is a whole node, with the node's own
// bytes as the original text.
//
// A node parsed from these bytes always lies inside them, so the failure below
// is unreachable; it is an error rather than a silent skip because the one way
// to reach it is a syntax tree and a file that have stopped describing each
// other, which is the condition [emit]'s span check exists to shout about.
func (s *fileScan) emitNode(rule mutation.Rule, node ast.Node, replacement string) error {
	original, ok := s.text(node)
	if !ok {
		position := s.tokFile.PositionFor(node.Pos(), false)
		return &Error{
			Code: CodeSpanMismatch,
			Message: "internal error: " + s.rel + ":" + strconv.Itoa(position.Line) + ":" +
				strconv.Itoa(position.Column) + " starts a node that reaches past the end of the file",
		}
	}
	return s.emit(rule, node, node.Pos(), original, replacement)
}

// emit records one candidate, or the reason it was suppressed.
//
// The span invariant is checked here and nowhere else: the bytes the span
// covers in the file on disk must be exactly the text the rule says it is
// replacing. Everything downstream — the identity, the instrumented splice,
// the diff a survivor is displayed as — trusts that, and a mismatch means the
// syntax tree and the file have drifted apart. Failing loudly is the only
// honest answer; splicing a replacement over the wrong bytes is not.
//
// The guard hint is resolved here too, and it is the second thing that can
// remove a candidate: an edit whose rewrite site none of the three guard forms
// can express is recorded as [SkipUnnameableDeclType] rather than catalogued
// for an instrumenter that would have to refuse it later. anchor is the node
// the edit belongs to — the binary expression an operator sits in, the
// statement a deletion removes, the value a return replaces — and it is where
// the search for that site starts.
func (s *fileScan) emit(rule mutation.Rule, anchor ast.Node, pos token.Pos, original, replacement string) error {
	if reason, ok := s.suppressed(pos); ok {
		s.record(s.rel, reason, 1)
		return nil
	}
	offset := s.tokFile.Offset(pos)
	end := offset + len(original)
	if offset < 0 || end > len(s.src) {
		return s.spanMismatch(pos, original, "the span reaches past the end of the file")
	}
	span, err := mutation.NewSpan(uint32(offset), uint32(end))
	if err != nil {
		return &Error{Code: CodeSpanMismatch, Message: "in " + s.rel, Err: err}
	}
	covered, err := span.Slice(s.src)
	if err != nil {
		return &Error{Code: CodeSpanMismatch, Message: "in " + s.rel, Err: err}
	}
	if string(covered) != original {
		return s.spanMismatch(pos, original, "the file holds "+strconv.Quote(string(covered)))
	}
	guard, ok := s.guardFor(anchor, span)
	if !ok {
		s.record(s.rel, SkipUnnameableDeclType, 1)
		return nil
	}

	candidate := mutation.Candidate{
		Path:         s.rel,
		Rule:         rule,
		Span:         span,
		Original:     original,
		Replacement:  replacement,
		SourceDigest: s.digest,
	}
	if err := candidate.Validate(); err != nil {
		return &Error{
			Code:    CodeInvalidCandidate,
			Message: "rule " + rule.String() + " proposed an invalid candidate in " + s.rel,
			Err:     err,
		}
	}
	position := s.tokFile.PositionFor(pos, false)
	s.candidates = append(s.candidates, Located{
		Candidate: candidate,
		Line:      position.Line,
		Column:    position.Column,
		Package:   s.pkgPath,
		Guard:     guard,
	})
	return nil
}

// guardFor resolves the rewrite site of one candidate, checking the one
// invariant the hint has to hold: the site contains the edit.
//
// A site that did not would be an instrumenter splicing a mutation into an
// expression that does not hold it, which is the failure the whole span
// discipline exists to prevent — so it is refused here rather than passed on.
func (s *fileScan) guardFor(anchor ast.Node, span mutation.Span) (Guard, bool) {
	if s.guard == nil || anchor == nil {
		return Guard{}, false
	}
	guard, ok := s.guard.guardFor(anchor)
	if !ok || !guard.SiteSpan.Contains(span) {
		return Guard{}, false
	}
	return guard, true
}

// spanMismatch builds the internal-invariant error, located the way a user
// would look for it even though only a maintainer should ever see it.
func (s *fileScan) spanMismatch(pos token.Pos, original, detail string) error {
	position := s.tokFile.PositionFor(pos, false)
	return &Error{
		Code: CodeSpanMismatch,
		Message: "internal error: " + s.rel + ":" + strconv.Itoa(position.Line) + ":" +
			strconv.Itoa(position.Column) + " should hold " + strconv.Quote(original) +
			" but " + detail,
	}
}

// suppressed reports the reason a position is off limits, if it is.
//
// The widest containing region wins. That is the region a walker would have
// refused to descend into, so it is the reason that remains true whatever the
// narrower construct inside it turns out to be: a boolean literal inside an
// array length inside a type parameter list is not off limits because array
// lengths are constant, it is off limits because none of a type parameter list
// is value code.
func (s *fileScan) suppressed(pos token.Pos) (SkipReason, bool) {
	best := -1
	for i := range s.suppressions {
		region := s.suppressions[i]
		if pos < region.start || pos >= region.end {
			continue
		}
		if best < 0 || wider(region, s.suppressions[best]) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	return s.suppressions[best].reason, true
}

// wider reports whether x is the outer region of two that both contain a
// position, with a frozen tie-break so that two regions covering exactly the
// same bytes always resolve the same way.
func wider(x, y suppression) bool {
	if x.width() != y.width() {
		return x.width() > y.width()
	}
	if x.start != y.start {
		return x.start < y.start
	}
	return reasonRank[x.reason] < reasonRank[y.reason]
}
