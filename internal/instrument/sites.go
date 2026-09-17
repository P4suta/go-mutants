// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/interval"
	"github.com/P4suta/go-mutants/internal/mutation"
)

type site struct {
	form      discover.GuardForm
	span      mutation.Span
	declare   []discover.DeclType
	siteType  string
	undeclare []Splice
}

type siteIndex struct {
	stmts map[mutation.Span]ast.Stmt
	exprs map[mutation.Span]ast.Expr
	src   []byte
	tok   *token.File
}

func parseSnapshotFile(srcPath string, src []byte) (*ast.File, *token.File, error) {
	file, tok, err := parseGo(srcPath, src)
	if err != nil {
		return nil, nil, &Error{
			Code:    CodeUnparsable,
			Message: "cannot parse " + strconv.Quote(srcPath) + " in the snapshot",
			Err:     err,
		}
	}
	return file, tok, nil
}

func checkParses(srcPath string, out []byte) error {
	if _, _, err := parseGo(srcPath, out); err != nil {
		return &Error{
			Code:    CodeUnparsable,
			Message: "internal error: the instrumented form of " + strconv.Quote(srcPath) + " does not parse",
			Err:     err,
		}
	}
	return nil
}

func parseGo(srcPath string, src []byte) (*ast.File, *token.File, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, srcPath, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}
	tok := fset.File(file.Package)
	if tok == nil {
		return nil, nil, errors.New("parsed file has no position information")
	}
	return file, tok, nil
}

func newSiteIndex(tok *token.File, file *ast.File, src []byte) *siteIndex {
	x := &siteIndex{
		stmts: make(map[mutation.Span]ast.Stmt),
		exprs: make(map[mutation.Span]ast.Expr),
		src:   src,
		tok:   tok,
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case ast.Stmt:
			if span := x.span(n); !x.hasStmt(span) {
				x.stmts[span] = n
			}
		case ast.Expr:
			if span := x.span(n); !x.hasExpr(span) {
				x.exprs[span] = n
			}
		}
		return true
	})
	return x
}

func (x *siteIndex) hasStmt(span mutation.Span) bool {
	_, ok := x.stmts[span]
	return ok
}

func (x *siteIndex) hasExpr(span mutation.Span) bool {
	_, ok := x.exprs[span]
	return ok
}

func (x *siteIndex) offset(pos token.Pos) uint32 { return uint32(x.tok.Offset(pos)) }

func (x *siteIndex) span(node ast.Node) mutation.Span {
	return mutation.Span{StartByte: x.offset(node.Pos()), EndByte: x.offset(node.End())}
}

func (x *siteIndex) siteFor(m mutation.Mutant, guard discover.Guard, srcPath string) (site, error) {
	span := guard.SiteSpan
	if !span.Contains(m.Span) {
		return site{}, &Error{
			Code: CodeSiteConflict,
			Message: fmt.Sprintf("%s: mutant %s at %s is not inside the site %s its guard hint names",
				srcPath, m.DisplayID, m.Span, span),
		}
	}
	switch guard.Form {
	case discover.GuardFormC:
		if !x.hasExpr(span) {
			return site{}, x.notFound(m, srcPath, span, "no expression covers these bytes")
		}
		return site{form: discover.GuardFormC, span: span}, nil

	case discover.GuardFormE:
		if !x.hasExpr(span) {
			return site{}, x.notFound(m, srcPath, span, "no expression covers these bytes")
		}
		if guard.SiteType == "" {
			return site{}, x.unsupported(m, srcPath, span,
				"a Form E site carries no type for its closure to return")
		}
		return site{form: discover.GuardFormE, span: span, siteType: guard.SiteType}, nil

	case discover.GuardFormCPrime:
		if !x.hasExpr(span) {
			return site{}, x.notFound(m, srcPath, span, "no expression covers these bytes")
		}
		if guard.SiteType == "" {
			return site{}, x.unsupported(m, srcPath, span,
				"a Form C' site carries no type to convert its selector back to")
		}
		return site{form: discover.GuardFormCPrime, span: span, siteType: guard.SiteType}, nil

	case discover.GuardFormS:
		stmt, ok := x.stmts[span]
		if !ok {
			return site{}, x.notFound(m, srcPath, span, "no statement covers these bytes")
		}
		if !wrappableStatement(stmt) {
			return site{}, x.unsupported(m, srcPath, span,
				fmt.Sprintf("a %T is not one of the statements Form S wraps", stmt))
		}
		return site{form: discover.GuardFormS, span: span}, nil

	case discover.GuardFormF:
		stmt, ok := x.stmts[span]
		if !ok {
			return site{}, x.notFound(m, srcPath, span, "no statement covers these bytes")
		}
		if !closurableStatement(stmt) {
			return site{}, x.unsupported(m, srcPath, span,
				fmt.Sprintf("a %T is not one of the statements Form F moves into a closure", stmt))
		}
		return site{form: discover.GuardFormF, span: span}, nil

	case discover.GuardFormD:
		stmt, ok := x.stmts[span]
		if !ok {
			return site{}, x.notFound(m, srcPath, span, "no statement covers these bytes")
		}
		undeclare, err := x.undeclare(stmt, span, m, srcPath)
		if err != nil {
			return site{}, err
		}
		return site{
			form:      discover.GuardFormD,
			span:      span,
			declare:   guard.DeclTypes,
			undeclare: undeclare,
		}, nil

	default:
		return site{}, x.unsupported(m, srcPath, span,
			"guard form "+strconv.Quote(string(guard.Form))+" is not one this version emits")
	}
}

func wrappableStatement(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt, *ast.ReturnStmt, *ast.IncDecStmt, *ast.SendStmt, *ast.DeferStmt, *ast.GoStmt:
		return true
	case *ast.AssignStmt:
		return s.Tok != token.DEFINE
	case *ast.BranchStmt:
		return s.Tok != token.FALLTHROUGH
	default:
		return false
	}
}

func closurableStatement(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt, *ast.SendStmt, *ast.IncDecStmt:
		return true
	case *ast.AssignStmt:
		return s.Tok != token.DEFINE
	default:
		return false
	}
}

func (x *siteIndex) undeclare(stmt ast.Stmt, span mutation.Span, m mutation.Mutant, srcPath string) ([]Splice, error) {
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		if s.Tok != token.DEFINE {
			return nil, x.unsupported(m, srcPath, span,
				"a Form D site has to declare something, and this assignment does not")
		}
		cut, err := x.rewriteToken(s.TokPos, token.DEFINE.String(), "=", span, m, srcPath)
		if err != nil {
			return nil, err
		}
		return []Splice{cut}, nil

	case *ast.DeclStmt:
		gen, ok := s.Decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			return nil, x.unsupported(m, srcPath, span,
				"only a `var` declaration is a Form D site; a const or type declaration is not")
		}
		return x.undeclareVar(gen, span, m, srcPath)

	default:
		return nil, x.unsupported(m, srcPath, span,
			fmt.Sprintf("a %T is not a declaration Form D can rewrite", stmt))
	}
}

func (x *siteIndex) undeclareVar(gen *ast.GenDecl, span mutation.Span, m mutation.Mutant, srcPath string) ([]Splice, error) {
	cuts := make([]Splice, 0, 3+2*len(gen.Specs))
	keyword, err := x.rewriteToken(gen.TokPos, token.VAR.String(), "", span, m, srcPath)
	if err != nil {
		return nil, err
	}
	cuts = append(cuts, keyword)

	if gen.Lparen.IsValid() {
		open, err := x.rewriteToken(gen.Lparen, token.LPAREN.String(), "", span, m, srcPath)
		if err != nil {
			return nil, err
		}
		closing, err := x.rewriteToken(gen.Rparen, token.RPAREN.String(), "", span, m, srcPath)
		if err != nil {
			return nil, err
		}
		cuts = append(cuts, open, closing)
	}

	for _, spec := range gen.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			return nil, x.unsupported(m, srcPath, span,
				fmt.Sprintf("a %T is not a value specification", spec))
		}
		if len(value.Values) == 0 {
			cuts = append(cuts, x.cut(value.Pos(), value.End(), span))
			continue
		}
		if value.Type != nil {
			cuts = append(cuts, x.cut(value.Type.Pos(), value.Type.End(), span))
		}
	}
	return cuts, nil
}

func (x *siteIndex) cut(from, to token.Pos, base mutation.Span) Splice {
	span := mutation.Span{StartByte: x.offset(from), EndByte: x.offset(to)}
	return Splice{
		Span:     relativeTo(span, base.StartByte),
		Original: x.src[span.StartByte:span.EndByte],
	}
}

func (x *siteIndex) rewriteToken(
	pos token.Pos,
	tok, replacement string,
	base mutation.Span,
	m mutation.Mutant,
	srcPath string,
) (Splice, error) {
	span := mutation.Span{StartByte: x.offset(pos), EndByte: x.offset(pos) + uint32(len(tok))}
	if uint64(span.EndByte) > uint64(len(x.src)) || !bytes.Equal(x.src[span.StartByte:span.EndByte], []byte(tok)) {
		return Splice{}, x.notFound(m, srcPath, base, "the declaration has no "+strconv.Quote(tok)+" where the file says it does")
	}
	return Splice{
		Span:        relativeTo(span, base.StartByte),
		Original:    x.src[span.StartByte:span.EndByte],
		Replacement: []byte(replacement),
	}, nil
}

func (x *siteIndex) notFound(m mutation.Mutant, srcPath string, span mutation.Span, detail string) error {
	return &Error{
		Code: CodeSiteNotFound,
		Message: fmt.Sprintf("%s: mutant %s (%s) cannot be instrumented: its guard hint names the site %s and %s",
			x.position(srcPath, m.Span), m.DisplayID, m.Rule, span, detail),
	}
}

func (x *siteIndex) unsupported(m mutation.Mutant, srcPath string, span mutation.Span, detail string) error {
	return &Error{
		Code: CodeUnsupportedGuard,
		Message: fmt.Sprintf("%s: mutant %s (%s) cannot be instrumented: its guard hint names the site %s, where %s",
			x.position(srcPath, m.Span), m.DisplayID, m.Rule, span, detail),
	}
}

func (x *siteIndex) position(srcPath string, span mutation.Span) string {
	if uint64(span.StartByte) > uint64(x.tok.Size()) {
		return srcPath + " " + span.String()
	}
	pos := x.tok.PositionFor(x.tok.Pos(int(span.StartByte)), false)
	return fmt.Sprintf("%s:%d:%d", srcPath, pos.Line, pos.Column)
}

func buildSites(
	index *siteIndex,
	srcPath string,
	mutants []mutation.Mutant,
	hints Hints,
) (interval.Forest[mutation.Mutant], map[mutation.Span]site, []discover.Completion, error) {
	items := make([]interval.Item[mutation.Mutant], 0, len(mutants))
	sites := make(map[mutation.Span]site, len(mutants))
	var completions []discover.Completion
	fail := func(err error) (
		interval.Forest[mutation.Mutant], map[mutation.Span]site, []discover.Completion, error,
	) {
		return interval.Forest[mutation.Mutant]{}, nil, nil, err
	}
	for _, m := range mutants {
		guard, err := hints.guardFor(m, srcPath)
		if err != nil {
			return fail(err)
		}
		resolved, err := index.siteFor(m, guard, srcPath)
		if err != nil {
			return fail(err)
		}
		if previous, seen := sites[resolved.span]; seen {
			if err := agree(previous, resolved, m, srcPath); err != nil {
				return fail(err)
			}
		}
		sites[resolved.span] = resolved
		completions = discover.MergeCompletions(completions, guard.Imports)
		items = append(items, interval.Item[mutation.Mutant]{Span: resolved.span, Payload: m})
	}
	forest, err := placeSites(srcPath, items)
	if err != nil {
		return fail(err)
	}
	return forest, sites, completions, nil
}

func agree(previous, current site, m mutation.Mutant, srcPath string) error {
	if previous.form == current.form && slices.Equal(previous.declare, current.declare) {
		return nil
	}
	return &Error{
		Code: CodeSiteConflict,
		Message: fmt.Sprintf(
			"internal error: %s: the site %s is Form %s for mutant %s and Form %s for another mutant of the same bytes",
			srcPath, current.span, current.form, m.DisplayID, previous.form),
	}
}

func renderSites(
	forest interval.Forest[mutation.Mutant],
	src []byte,
	compose func(*siteNode, map[*siteNode][]byte) ([]byte, error),
) ([]Splice, int, error) {
	rendered := make(map[*siteNode][]byte)
	var failure error
	forest.InnerFirst(func(node *siteNode) {
		if failure != nil {
			return
		}
		text, err := compose(node, rendered)
		if err != nil {
			failure = err
			return
		}
		rendered[node] = text
	})
	if failure != nil {
		return nil, 0, failure
	}

	roots := forest.Roots()
	splices := make([]Splice, 0, len(roots))
	for _, root := range roots {
		splices = append(splices, Splice{
			Span:        root.Span,
			Original:    src[root.Span.StartByte:root.Span.EndByte],
			Replacement: rendered[root],
			Origin:      "the rewrite site at " + root.Span.String(),
		})
	}
	return splices, len(rendered), nil
}

func placeSites(srcPath string, items []interval.Item[mutation.Mutant]) (interval.Forest[mutation.Mutant], error) {
	forest, conflicts := interval.Build(items)
	if len(conflicts) > 0 {
		c := conflicts[0]
		return interval.Forest[mutation.Mutant]{}, &Error{
			Code: CodeSiteConflict,
			Message: fmt.Sprintf("internal error: %s: the rewrite site %s could not be placed (%s); %d of %d sites in this file were rejected",
				srcPath, c.Item.Span, c.Reason, len(conflicts), len(items)),
		}
	}
	return forest, nil
}

func relativeTo(span mutation.Span, base uint32) mutation.Span {
	return mutation.Span{StartByte: span.StartByte - base, EndByte: span.EndByte - base}
}
