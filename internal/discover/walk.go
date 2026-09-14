// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"errors"
	"go/ast"
	"go/constant"
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
			d.recordFile(ref.rel, reason)
			continue
		}
		d.recordFile(ref.rel, SkipCgo)
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
		d.recordFile(rel, reason)
		return nil
	}
	if isGenerated(file) {
		d.recordFile(rel, SkipGenerated)
		return nil
	}
	if d.matchers.empty() {
		return nil
	}

	src, err := os.ReadFile(abs)
	if err != nil {
		return &Error{Code: CodeFileUnreadable, Message: "cannot read " + strconv.Quote(rel), Err: err}
	}
	return d.scanParsed(rel, packagePath(pkg), src, tokFile, file, pkg.TypesInfo, pkg.Types)
}

// scanParsed runs the mutation walk over one file that has already been parsed
// and type-checked, emitting its candidates and skips into d.
//
// It is the part of discovery that needs no package loader: every input is
// passed in rather than read from a [packages.Package], so a caller that has
// built an *ast.File and a *types.Info another way — go/parser and go/types over
// a synthetic source, for instance — can exercise the whole walk without a
// toolchain. [discovery.file] is the loader-fed caller, and it is the only
// difference between a real discovery and a test's: both reach the walk through
// here, so the two cannot come to disagree about what the walk does.
//
// tokFile is the [token.File] file's positions resolve against, info its type
// information, and pkgTypes the package it was checked in — what the guard
// resolver needs to name the type an edit would produce.
func (d *discovery) scanParsed(
	rel, pkgPath string,
	src []byte,
	tokFile *token.File,
	file *ast.File,
	info *types.Info,
	pkgTypes *types.Package,
) error {
	if uint64(len(src)) > math.MaxUint32 {
		return &Error{
			Code:    CodeFileUnreadable,
			Message: strconv.Quote(rel) + " is larger than 4 GiB, which mutant spans cannot address",
		}
	}
	digest := mutation.Digest(src)
	// Recorded for every file whose bytes were read, and not only for the ones
	// that go on to produce a candidate: [Result.SourceDigests] is what
	// discovery saw, and a caller checking that the tree held still underneath
	// this pass needs the files it found nothing in most of all.
	d.digests[rel] = digest
	scan := &fileScan{
		discovery:    d,
		rel:          rel,
		pkgPath:      pkgPath,
		src:          src,
		digest:       digest,
		tokFile:      tokFile,
		info:         info,
		suppressions: collectSuppressions(file, info),
		guard:        newGuardResolver(file, info, pkgTypes, tokFile),
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

// addCaseLabels suppresses the label list of every clause of one switch body.
//
// The labels and not the bodies: everything under a `case` is ordinary code and
// is mutated like any other.
func addCaseLabels(add func(token.Pos, token.Pos, SkipReason), body *ast.BlockStmt) {
	if body == nil {
		return
	}
	for _, statement := range body.List {
		clause, ok := statement.(*ast.CaseClause)
		if !ok || len(clause.List) == 0 {
			continue
		}
		add(clause.List[0].Pos(), clause.List[len(clause.List)-1].End(), SkipCaseLabel)
	}
}

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
		case *ast.SwitchStmt:
			// A *tagged* switch compares each label against the tag, and a
			// guard cannot stand where a label does: Form C needs the
			// expression to be exactly `bool`, which a label of `switch x` is
			// not, and Form S needs a statement, which a label is not either.
			//
			// A *tagless* switch is the opposite case and is the reason this
			// arm asks. `switch { case a > b: }` has an implicit tag of the
			// typed constant `true`, so every label in it is exactly `bool` --
			// which is precisely what a Form C site is. Suppressing those was
			// a blanket rather than a verdict: the labels were never offered to
			// the guard chooser at all, so nothing ever decided they could not
			// be expressed.
			if n.Tag != nil {
				addCaseLabels(add, n.Body)
			}
		case *ast.TypeSwitchStmt:
			// A type switch's labels hold types, not values, which is the same
			// fact SkipTypeParam records elsewhere. No rule in the registry
			// rewrites a type, and a "mutated" type is a different program
			// rather than a mutant of this one.
			addCaseLabels(add, n.Body)
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
			failure = errors.Join(
				s.negateCondition(n.Cond, ruleNegateCondition),
				s.settleCondition(n.Cond, ruleConditionToTrue, "true"),
				s.settleCondition(n.Cond, ruleConditionToFalse, "false"),
			)
		case *ast.ForStmt:
			failure = errors.Join(
				s.negateCondition(n.Cond, ruleNegateLoopCondition),
				s.settleCondition(n.Cond, ruleLoopConditionToFalse, "false"),
			)
		case *ast.ReturnStmt:
			failure = s.returnStmt(n)
		case *ast.AssignStmt:
			failure = s.assignStmt(n)
		case *ast.IncDecStmt:
			failure = s.incDecStmt(n)
		case *ast.BranchStmt:
			failure = s.branchStmt(n)
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

// branchStmt is the labeled-branch family and the one refusal beside it.
//
// `break L` and `continue L` become `break` and `continue`. That is a real
// question about a program: a labelled branch says "leave *that* construct",
// and dropping the label says "leave the nearest one", which is a different
// program wherever the two differ -- and wherever they do not, this refuses.
//
// The refusal is structural rather than statistical. If the label names the
// innermost construct the bare form would bind to, the two statements are the
// same program, token for token, and there is nothing to measure. What makes
// that worth computing is that the answer differs between the two rules: a
// `switch` inside a labelled `for` is breakable and not continuable, so
// `break L` there is a real mutant -- the bare form leaves the switch -- while
// `continue L` at the same position is equivalent.
//
// The unused-label trap disarms itself. An unused label does not compile, so
// removing a label's only reference would break the tree; Form S keeps the
// original bytes in its `else` arm, so `L` goes on being referenced whether or
// not the mutant is active.
//
// `goto` is recorded as [SkipLabelOrGoto] rather than mutated, and
// `fallthrough` is neither: see that constant for the first, and
// [FormSStatement] for why the second is not even a site.
func (s *fileScan) branchStmt(n *ast.BranchStmt) error {
	switch n.Tok {
	case token.GOTO:
		s.recordAt(s.rel, SkipLabelOrGoto, "", n.Pos())
		return nil
	case token.BREAK, token.CONTINUE:
	default:
		return nil
	}
	if n.Label == nil {
		// A bare branch has no label to drop.
		return nil
	}
	name := ruleDropBreakLabel
	if n.Tok == token.CONTINUE {
		name = ruleDropContinueLabel
	}
	rule, ok := s.matchers.rule(name)
	if !ok {
		return nil
	}
	if s.labelNamesTheNearestTarget(n) {
		// The mutation and the source are the same program. Silent, for
		// [fileScan.replaceReturn]'s reason.
		return nil
	}
	return s.emitNode(rule, n, n.Tok.String())
}

// labelNamesTheNearestTarget reports whether dropping the label would leave the
// branch bound to the same construct it is bound to now.
//
// The walk is outward from the branch to the first construct its *bare* form
// would bind to -- a `for` or `range` for `continue`, and those plus `switch`,
// a type switch and `select` for `break` -- and the question is whether that
// construct is the one the label labels. The comparison is by object rather
// than by name: a label may be shadowed in an inner function literal, and two
// labels spelled the same are two labels.
//
// A branch this phase cannot resolve answers true, which refuses the candidate.
// That is the fail-closed direction: an unresolvable label is one this code
// does not understand, and emitting a mutant on the strength of not
// understanding it is how an equivalent mutant becomes a survivor somebody has
// to argue about.
func (s *fileScan) labelNamesTheNearestTarget(n *ast.BranchStmt) bool {
	if s.guard == nil || s.info == nil {
		return true
	}
	target, ok := s.info.Uses[n.Label].(*types.Label)
	if !ok {
		return true
	}
	for node := ast.Node(n); node != nil; node = s.guard.parent[node] {
		if !s.bindsBareBranch(node, n.Tok) {
			continue
		}
		labelled, ok := s.guard.parent[node].(*ast.LabeledStmt)
		if !ok {
			return false
		}
		return s.info.Defs[labelled.Label] == target
	}
	// No enclosing construct at all, which the type checker would already have
	// refused. Fail closed.
	return true
}

// bindsBareBranch reports whether a node is a construct an unlabelled branch of
// the given kind binds to.
//
// The two sets differ by exactly the three constructs that are breakable and
// not continuable, and that difference is the whole reason this family has two
// rules rather than one.
func (s *fileScan) bindsBareBranch(node ast.Node, tok token.Token) bool {
	switch node.(type) {
	case *ast.ForStmt, *ast.RangeStmt:
		return true
	case *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
		return tok == token.BREAK
	default:
		return false
	}
}

// settleCondition replaces a whole condition with a constant.
//
// The catalogue could not say this before. `negate-condition` writes `!(C)`,
// which is a different condition rather than a settled one; `true-to-false`
// fires only where the condition *is* a literal; `nil-error-branch` is the one
// special case of "this branch stops firing", written for `err != nil` alone.
// A guard that always fires and a guard that never does are the two questions a
// reader asks about a branch, and neither had a rule.
//
// No new guard form is needed: this is the anchor [fileScan.negateCondition]
// already uses, and `true` is an untyped constant that Form C writes into the
// same selector any other boolean expression goes into.
//
// The type gate is the one difference from negation, and it is the guard's
// rather than this rule's. `!` applies to any boolean type, so a condition of a
// named boolean type is negatable; Form C requires a site of *exactly* the
// universe `bool`, so such a condition is refused as [SkipUnnameableDeclType]
// by [fileScan.emitAt] -- the same answer the negation at that site already
// gets, from the same place, rather than a silence this rule invented.
//
// A `for` with no condition and a `range` clause both arrive here with a nil
// Cond and are passed over: there is nothing to settle, and inventing a
// condition would be a different edit than this rule describes.
//
// There is deliberately no `loop-condition-to-true`. It would turn every
// counted loop in a tree into one that never ends, each costing a whole
// per-mutant timeout -- twice, since a timeout is measured again before it is
// believed -- to teach a reader nothing the source does not already say. False
// is the safe direction: the loop runs zero times.
func (s *fileScan) settleCondition(cond ast.Expr, name, replacement string) error {
	rule, ok := s.matchers.rule(name)
	if !ok || cond == nil || !isBoolClassed(s.typeOf(cond)) {
		return nil
	}
	original, ok := s.text(cond)
	if !ok || original == replacement || s.isConstantBool(cond, replacement == "true") {
		// A condition already spelled as its own replacement is not a place
		// go-mutants declined to mutate; it is a place where the mutation and
		// the source are the same program. [fileScan.replaceReturn] makes the
		// same refusal for the same reason.
		//
		// The constant check is the same refusal one level down, and it is the
		// one that earns its keep: `const limit = 3 > 2` used in an `if` is
		// spelled `limit` and *is* `true`, so settling it true writes different
		// bytes for the same program. go/types has already folded it, so this
		// costs a map lookup and removes a mutant that could never die. Only
		// the matching direction is refused -- settling a constantly-true
		// condition *false* is a branch that stops firing, which is a real and
		// useful mutant.
		return nil
	}
	return s.emitNode(rule, cond, replacement)
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
	// One site for the whole statement, computed before any candidate: the
	// probe rewrite replaces the statement rather than the value, so every
	// candidate in it describes the same rewrite and differs only in which
	// result it replaces. A nil site is a statement this file cannot spell a
	// probe for, or one the probe cannot stand in for, and every candidate in it
	// goes out unprobed.
	//
	// The per-result conditions are asked after it, and each of them refuses one
	// result while leaving the others probed.
	site := s.returnSite(n, results)
	for i, value := range n.Results {
		declared := results.At(i).Type()
		hint := site.at(i)
		if hint != nil && !s.probesResult(value, declared) {
			hint = nil
		}
		if err := s.returnValue(value, declared, hint); err != nil {
			return err
		}
	}
	return nil
}

// returnSite computes the probe hint of one `return`, or nil when this phase
// holds no resolver to compute it with.
func (s *fileScan) returnSite(n *ast.ReturnStmt, results *types.Tuple) *ReturnSite {
	if s.guard == nil {
		return nil
	}
	return s.guard.returnSite(n, results)
}

// probesResult reports whether one result of a `return` may carry the site
// computed for that statement, or false when this phase holds no resolver to
// ask.
func (s *fileScan) probesResult(value ast.Expr, declared types.Type) bool {
	if s.guard == nil {
		return false
	}
	return s.guard.probesResult(value, declared)
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
func (s *fileScan) returnValue(value ast.Expr, declared types.Type, site *ReturnSite) error {
	if isExactlyError(s.typeOf(value)) {
		return s.replaceReturn(value, ruleReturnErrToNil, "nil", site)
	}
	switch {
	case isNumeric(declared):
		return s.replaceReturn(value, ruleReturnZeroNumeric, "0", site)
	case isStringy(declared):
		return s.replaceReturn(value, ruleReturnEmptyString, `""`, site)
	case isBoolClassed(declared):
		if err := s.replaceReturn(value, ruleReturnTrue, "true", site); err != nil {
			return err
		}
		return s.replaceReturn(value, ruleReturnFalse, "false", site)
	case isNillable(declared):
		// Not an error-typed value: that was settled above.
		if err := s.replaceReturn(value, ruleReturnNil, "nil", site); err != nil {
			return err
		}
		return s.replaceEmptyNeutral(value, declared, site)
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
func (s *fileScan) replaceReturn(value ast.Expr, name, replacement string, site *ReturnSite) error {
	rule, ok := s.matchers.rule(name)
	if !ok {
		return nil
	}
	original, ok := s.text(value)
	if !ok || original == replacement {
		return nil
	}
	return s.emitProbed(rule, value, replacement, site)
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
	return s.emitProbed(rule, node, replacement, nil)
}

// emitProbed is [fileScan.emitNode] for a candidate that also carries a probe
// hint. The hint is a fact about the rewrite site of a *different* tree, so it
// travels beside the candidate rather than changing anything about it.
func (s *fileScan) emitProbed(rule mutation.Rule, node ast.Node, replacement string, site *ReturnSite) error {
	original, ok := s.text(node)
	if !ok {
		position := s.tokFile.PositionFor(node.Pos(), false)
		return &Error{
			Code: CodeSpanMismatch,
			Message: "internal error: " + s.rel + ":" + strconv.Itoa(position.Line) + ":" +
				strconv.Itoa(position.Column) + " starts a node that reaches past the end of the file",
		}
	}
	return s.emitAt(rule, node, node.Pos(), original, replacement, site)
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
//
// The branch proof is resolved from the same anchor and removes nothing: it is
// present when this phase could prove the edit only narrows an `if` or `for`
// condition, and nil otherwise. See [BranchProof].
func (s *fileScan) emit(rule mutation.Rule, anchor ast.Node, pos token.Pos, original, replacement string) error {
	return s.emitAt(rule, anchor, pos, original, replacement, nil)
}

// emitAt is [fileScan.emit] with the probe hint the return-value family carries.
// Every other family passes nil, because the probe forms of their sites are not
// written yet and a hint nothing can rewrite would be worse than none.
func (s *fileScan) emitAt(
	rule mutation.Rule,
	anchor ast.Node,
	pos token.Pos,
	original, replacement string,
	site *ReturnSite,
) error {
	if reason, ok := s.suppressed(pos); ok {
		s.recordAt(s.rel, reason, rule.Name, pos)
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
		s.recordAt(s.rel, SkipUnnameableDeclType, rule.Name, pos)
		return nil
	}
	// The probe hint is attached after the guard and never instead of it: a
	// site the probe tree cannot express is still a site the mutant tree does,
	// so a nil hint is not a skip and removes no candidate.
	guard.Return = site

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
		Candidate:   candidate,
		Line:        position.Line,
		Column:      position.Column,
		Package:     s.pkgPath,
		Guard:       guard,
		Branch:      s.branchProof(rule, anchor),
		Termination: s.terminationProof(rule, anchor),
	})
	return nil
}

// replaceEmptyNeutral offers the neutral value that is not nil: `[]T{}` for a
// slice result and `map[K]V{}` for a map one.
//
// `len(x) == 0` is true of both nil and empty, and it is the assertion a suite
// routinely makes -- so a function that returns nil where it meant to return an
// empty slice passes every `len` check, and `encoding/json` writes `null` where
// the caller expected `[]`. That is the difference this rule is about, and
// `return-nil` beside it cannot express it.
//
// The type has to be *spelled*, which is what makes this a rule rather than a
// constant: `[]T{}` needs T rendered against the file's own imports.
// [guardResolver.typeString] is the machinery -- the same one Form D's
// declarations go through, so a type this file cannot name is refused here
// exactly as it is there.
//
// Two refusals, both silent for [fileScan.replaceReturn]'s reason:
//
//   - a result already spelled as its own replacement. `return []T{}` and
//     `return make([]T, 0)` are the program the mutant would be, and
//     replaceReturn's own check only catches the first spelling.
//   - a result returned beside a non-nil error. `if err != nil { return nil,
//     err }` is the commonest `return nil` for a slice in Go, and a caller that
//     sees an error does not look at the other results -- so the mutant is
//     equivalent by universal convention. That is an argument from convention
//     rather than a proof, and docs/operators.md says so where the gate is
//     documented. `return xs, nil` -- the success path, where the rule is worth
//     the most -- is not gated.
func (s *fileScan) replaceEmptyNeutral(value ast.Expr, declared types.Type, site *ReturnSite) error {
	slice, mapped := isEmptiable(declared)
	if !slice && !mapped {
		return nil
	}
	if s.returnsBesideAnError(value) || s.alreadyEmpty(value) {
		return nil
	}
	if s.guard == nil {
		return nil
	}
	rule := ruleReturnEmptySlice
	if mapped {
		rule = ruleReturnEmptyMap
	}
	spelled, ok := s.guard.typeString(declared)
	if !ok {
		// The same fact Form D records when it cannot spell a declared type:
		// go-mutants knows what it would like to write here and cannot say it
		// in Go. A dot import and an unsafe.Pointer element are the two ways
		// to reach this.
		s.recordAt(s.rel, SkipUnnameableDeclType, rule, value.Pos())
		return nil
	}
	// No probe hint. A slice is not comparable, so `r0 != []T{}` is not legal
	// Go and the return form's `!=` cannot be written for it; the `return-nil`
	// beside this one keeps its own, because `r0 != nil` is legal for both.
	return s.replaceReturn(value, rule, spelled+"{}", nil)
}

// returnsBesideAnError reports whether the statement this value belongs to also
// returns a non-nil error.
func (s *fileScan) returnsBesideAnError(value ast.Expr) bool {
	if s.guard == nil {
		return false
	}
	stmt, ok := s.guard.parent[ast.Node(value)].(*ast.ReturnStmt)
	if !ok {
		return false
	}
	for _, result := range stmt.Results {
		if result == value {
			continue
		}
		if isExactlyError(s.typeOf(result)) && !s.isNilLiteral(result) {
			return true
		}
	}
	return false
}

// alreadyEmpty reports whether a result is already the empty value this rule
// would write: a composite literal with no elements, or a `make` with a zero
// length and no capacity.
func (s *fileScan) alreadyEmpty(value ast.Expr) bool {
	switch expr := value.(type) {
	case *ast.CompositeLit:
		return len(expr.Elts) == 0
	case *ast.CallExpr:
		ident, ok := expr.Fun.(*ast.Ident)
		if !ok || ident.Name != "make" || len(expr.Args) < 2 {
			return false
		}
		for _, arg := range expr.Args[1:] {
			if !s.isZeroConstant(arg) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// isConstantBool reports whether an expression is a constant of the given
// boolean value, as go/types folded it.
func (s *fileScan) isConstantBool(expr ast.Expr, want bool) bool {
	if s.info == nil {
		return false
	}
	value := s.info.Types[expr].Value
	if value == nil || value.Kind() != constant.Bool {
		return false
	}
	return constant.BoolVal(value) == want
}

// isZeroConstant reports whether an expression is the constant zero.
func (s *fileScan) isZeroConstant(expr ast.Expr) bool {
	if s.info == nil {
		return false
	}
	value := s.info.Types[expr].Value
	if value == nil {
		return false
	}
	n, ok := constant.Int64Val(value)
	return ok && n == 0
}

// recordAt records one suppression at the position the edit would have sat at,
// naming the rule whose edit it was.
//
// The coordinates cost one [token.File.PositionFor] call, made where the
// [token.Pos] is already in hand, and they are what turns "four const-decl
// sites in this file" into four places a reader can go. The position is
// unadjusted, exactly as the one [emitAt] stamps on a candidate is: both name a
// byte in the snapshot's own copy of the file, and a `//line` directive that
// relocated a skip while leaving the mutants beside it alone would make the two
// halves of one listing disagree about where they are.
func (s *fileScan) recordAt(rel string, reason SkipReason, rule string, pos token.Pos) {
	position := s.tokFile.PositionFor(pos, false)
	s.recordSite(rel, reason, rule, position.Line, position.Column)
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
