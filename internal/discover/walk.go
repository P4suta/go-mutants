// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"cmp"
	"errors"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"math"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"

	"golang.org/x/tools/go/packages"

	"github.com/P4suta/go-mutants/internal/mutation"
)

var generatedMarker = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

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

func (d *discovery) file(loaded *loadResult, pkg *packages.Package, file *ast.File) error {
	tokFile := loaded.fset.File(file.Package)
	if tokFile == nil {
		return nil
	}
	abs := tokFile.Name()
	if isTestFile(abs) {
		return nil
	}
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
	return d.scanParsed(rel, packagePath(pkg), src, tokFile, file, pkg.TypesInfo, pkg.Types,
		d.packageImports(loaded, pkg))
}

func (d *discovery) scanParsed(
	rel, pkgPath string,
	src []byte,
	tokFile *token.File,
	file *ast.File,
	info *types.Info,
	pkgTypes *types.Package,
	siblings map[string]string,
) error {
	if uint64(len(src)) > math.MaxUint32 {
		return &Error{
			Code:    CodeFileUnreadable,
			Message: strconv.Quote(rel) + " is larger than 4 GiB, which mutant spans cannot address",
		}
	}
	digest := mutation.Digest(src)
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
		guard:        newGuardResolver(file, info, pkgTypes, tokFile, siblings),
	}
	return scan.walk(file)
}

func (d *discovery) selection(rel string) (SkipReason, bool) {
	subject := rel
	if d.prefix != "" {
		subject = path.Join(d.prefix, rel)
	}
	if len(d.include) > 0 {
		included := false
		for _, pattern := range d.include {
			if pattern.Match(subject) {
				included = true
				break
			}
		}
		if !included {
			return SkipExcluded, true
		}
	}
	for _, pattern := range d.exclude {
		if pattern.Match(subject) {
			return SkipExcluded, true
		}
	}
	return "", false
}

func isGenerated(file *ast.File) bool {
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if comment.Pos() > file.Package {
				return false
			}
			if generatedMarker.MatchString(comment.Text) {
				return true
			}
		}
	}
	return false
}

type suppression struct {
	start  token.Pos
	end    token.Pos
	reason SkipReason
}

func (s suppression) width() int { return int(s.end - s.start) }

func collectSuppressions(file *ast.File, info *types.Info) []suppression {
	var out []suppression
	add := func(from, to token.Pos, reason SkipReason) {
		out = append(out, suppression{start: from, end: to, reason: reason})
	}

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
		case *ast.FuncDecl:
			if n.Type != nil && n.Type.TypeParams != nil {
				add(n.Type.TypeParams.Pos(), n.Type.TypeParams.End(), SkipTypeParam)
			}
		case *ast.TypeSpec:
			if n.TypeParams != nil {
				add(n.TypeParams.Pos(), n.TypeParams.End(), SkipTypeParam)
			}
		case *ast.IndexExpr:
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

	sortSuppressions(out)
	return out
}

func sortSuppressions(out []suppression) {
	slices.SortFunc(out, func(x, y suppression) int {
		return cmp.Or(
			cmp.Compare(x.start, y.start),
			cmp.Compare(y.end, x.end),
			cmp.Compare(reasonRank[x.reason], reasonRank[y.reason]),
		)
	})
}

func isTypeExpr(info *types.Info, expr ast.Expr) bool {
	if info == nil || expr == nil {
		return false
	}
	tv, ok := info.Types[expr]
	return ok && tv.IsType()
}

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

func (s *fileScan) unaryExpr(n *ast.UnaryExpr) error {
	rule, ok := s.matchers.rule(ruleRemoveNegation)
	if !ok || n.Op != token.NOT || !isBoolClassed(s.typeOf(n.X)) {
		return nil
	}
	operand, ok := s.text(n.X)
	if !ok {
		return s.nodeReachesPastTheEnd(n.X)
	}
	return s.emitNode(rule, n, operand)
}

func (s *fileScan) booleanLiteral(n *ast.Ident) error {
	matcher, ok := s.matchers.boolean[n.Name]
	if !ok || !s.isUniverseConst(n) {
		return nil
	}
	return s.emit(matcher.rule, n, n.Pos(), n.Name, matcher.replacement)
}

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
		return nil
	}
	return s.emitNode(rule, n, n.Tok.String())
}

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
	return true
}

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

func (s *fileScan) settleCondition(cond ast.Expr, name, replacement string) error {
	rule, ok := s.matchers.rule(name)
	if !ok || cond == nil || !isBoolClassed(s.typeOf(cond)) {
		return nil
	}
	original, ok := s.text(cond)
	if !ok || original == replacement || s.spellsTheSameConstant(cond, replacement) {
		return nil
	}
	return s.emitNode(rule, cond, replacement)
}

func (s *fileScan) returnStmt(n *ast.ReturnStmt) error {
	results := s.enclosingResults(n)
	if len(n.Results) == 0 || results == nil || results.Len() != len(n.Results) {
		return nil
	}
	site := s.probeSite(n, results)
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

func (s *fileScan) probeSite(n *ast.ReturnStmt, results *types.Tuple) *ProbeSite {
	if s.guard == nil {
		return nil
	}
	return s.guard.probeSite(n, results)
}

func (s *fileScan) probesResult(value ast.Expr, declared types.Type) bool {
	if s.guard == nil {
		return false
	}
	return s.guard.probesResult(value, declared)
}

func (s *fileScan) returnValue(value ast.Expr, declared types.Type, site *ProbeSite) error {
	if isExactlyError(s.typeOf(value)) {
		return s.replaceReturn(value, ruleReturnErrToNil, "nil", site, nil)
	}
	switch {
	case isNumeric(declared):
		return s.replaceReturn(value, ruleReturnZeroNumeric, "0", site, nil)
	case isStringy(declared):
		return s.replaceReturn(value, ruleReturnEmptyString, `""`, site, nil)
	case isBoolClassed(declared):
		if err := s.replaceReturn(value, ruleReturnTrue, "true", site, nil); err != nil {
			return err
		}
		return s.replaceReturn(value, ruleReturnFalse, "false", site, nil)
	case isNillable(declared):
		if err := s.replaceReturn(value, ruleReturnNil, "nil", site, nil); err != nil {
			return err
		}
		return s.replaceEmptyNeutral(value, declared, site)
	default:
		return nil
	}
}

func (s *fileScan) replaceReturn(
	value ast.Expr, name, replacement string, site *ProbeSite, needs []Completion,
) error {
	rule, ok := s.matchers.rule(name)
	if !ok {
		return nil
	}
	original, ok := s.text(value)
	if !ok {
		return s.nodeReachesPastTheEnd(value)
	}
	if original == replacement || s.spellsTheSameConstant(value, replacement) {
		return nil
	}
	return s.emitProbed(rule, value, replacement, site, needs)
}

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

func (s *fileScan) assignStmt(n *ast.AssignStmt) error {
	if matcher, ok := s.matchers.assignOp[n.Tok]; ok && len(n.Lhs) == 1 {
		if target := s.typeOf(n.Lhs[0]); isInteger(target) || isFloat(target) {
			if err := s.emit(matcher.rule, n, n.TokPos, matcher.original, matcher.replacement); err != nil {
				return err
			}
		}
	}
	if rule, ok := s.matchers.rule(ruleDeleteAssignment); ok && n.Tok == token.ASSIGN {
		return s.emitNode(rule, n, "")
	}
	return nil
}

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

func (s *fileScan) swap(table map[token.Token]tokenMatcher, anchor ast.Node, op token.Token, pos token.Pos) error {
	matcher, ok := table[op]
	if !ok {
		return nil
	}
	return s.emit(matcher.rule, anchor, pos, matcher.original, matcher.replacement)
}

func (s *fileScan) text(node ast.Node) (string, bool) {
	start := s.tokFile.Offset(node.Pos())
	end := s.tokFile.Offset(node.End())
	if end > len(s.src) {
		return "", false
	}
	return string(s.src[start:end]), true
}

func (s *fileScan) emitNode(rule mutation.Rule, node ast.Node, replacement string) error {
	return s.emitProbed(rule, node, replacement, nil, nil)
}

func (s *fileScan) emitProbed(
	rule mutation.Rule, node ast.Node, replacement string, site *ProbeSite, needs []Completion,
) error {
	original, ok := s.text(node)
	if !ok {
		return s.nodeReachesPastTheEnd(node)
	}
	return s.emitAt(rule, node, node.Pos(), original, replacement, site, needs)
}

func (s *fileScan) emit(rule mutation.Rule, anchor ast.Node, pos token.Pos, original, replacement string) error {
	return s.emitAt(rule, anchor, pos, original, replacement, nil, nil)
}

func (s *fileScan) emitAt(
	rule mutation.Rule,
	anchor ast.Node,
	pos token.Pos,
	original, replacement string,
	site *ProbeSite,
	needs []Completion,
) error {
	if reason, ok := s.suppressed(pos); ok {
		s.recordAt(s.rel, reason, rule.Name, pos)
		return nil
	}
	offset := s.tokFile.Offset(pos)
	end := offset + len(original)
	if end > len(s.src) {
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
	guard.Imports = MergeCompletions(guard.Imports, needs)
	if site != nil {
		guard.Probe = site
	}
	if guard.Probe == nil && rule.Family == mutation.FamilyStatementDeletion {
		guard.Probe = s.guard.reachProbe(guard)
	}
	if guard.Probe != nil && guard.Probe.Form != ProbeFormReach &&
		s.guard.introducesPanic(anchor, replacement) {
		guard.Probe = nil
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

func (s *fileScan) replaceEmptyNeutral(value ast.Expr, declared types.Type, site *ProbeSite) error {
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
	spelled, needs, ok := s.guard.typeString(declared)
	if !ok {
		s.recordAt(s.rel, SkipUnnameableDeclType, rule, value.Pos())
		return nil
	}
	return s.replaceReturn(value, rule, spelled+"{}", nil, needs)
}

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
			if !s.spellsTheSameConstant(arg, "0") {
				return false
			}
		}
		return true
	default:
		return false
	}
}

var constantReplacements = map[string]constant.Value{
	"0":     constant.MakeInt64(0),
	`""`:    constant.MakeString(""),
	"true":  constant.MakeBool(true),
	"false": constant.MakeBool(false),
}

func (s *fileScan) spellsTheSameConstant(expr ast.Expr, replacement string) bool {
	want, ok := constantReplacements[replacement]
	if !ok || s.info == nil || expr == nil {
		return false
	}
	got := s.info.Types[expr].Value
	if got == nil || !comparableConstants(got, want) {
		return false
	}
	return constant.Compare(got, token.EQL, want)
}

func comparableConstants(a, b constant.Value) bool {
	class := func(v constant.Value) constant.Kind {
		switch v.Kind() {
		case constant.Int, constant.Float, constant.Complex:
			return constant.Complex
		default:
			return v.Kind()
		}
	}
	return class(a) == class(b) && a.Kind() != constant.Unknown
}

func (s *fileScan) recordAt(rel string, reason SkipReason, rule string, pos token.Pos) {
	position := s.tokFile.PositionFor(pos, false)
	s.recordSite(rel, reason, rule, position.Line, position.Column)
}

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

func (s *fileScan) nodeReachesPastTheEnd(node ast.Node) error {
	position := s.tokFile.PositionFor(node.Pos(), false)
	return &Error{
		Code: CodeSpanMismatch,
		Message: "internal error: " + s.rel + ":" + strconv.Itoa(position.Line) + ":" +
			strconv.Itoa(position.Column) + " starts a node that reaches past the end of the file",
	}
}

func (s *fileScan) spanMismatch(pos token.Pos, original, detail string) error {
	position := s.tokFile.PositionFor(pos, false)
	return &Error{
		Code: CodeSpanMismatch,
		Message: "internal error: " + s.rel + ":" + strconv.Itoa(position.Line) + ":" +
			strconv.Itoa(position.Column) + " should hold " + strconv.Quote(original) +
			" but " + detail,
	}
}

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

func wider(x, y suppression) bool {
	return cmp.Or(
		cmp.Compare(y.width(), x.width()),
		cmp.Compare(x.start, y.start),
		cmp.Compare(reasonRank[x.reason], reasonRank[y.reason]),
	) < 0
}
