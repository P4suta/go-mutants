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

// A site is one rewrite site: the bytes a guard replaces, the form it takes,
// and everything that form needs beyond those bytes.
//
// It is what a [discover.Guard] becomes once it has been checked against the
// file on disk. The hint says which shape to use and over which bytes; this
// says which node those bytes turned out to be and, for a declaration, exactly
// which splices turn it into the plain assignment a guard branch can hold.
type site struct {
	// form is the rewrite shape.
	form discover.GuardForm
	// span is the byte range the guard replaces, in file coordinates.
	span mutation.Span
	// declare are the names a Form D site hoists out in front of its guard,
	// with the type each is declared as. Empty for the other two forms, and for
	// a declaration whose every name is the blank identifier.
	declare []discover.DeclType
	// undeclare are the splices that turn a Form D site's own bytes into plain
	// assignments — the `:=` downgraded to `=`, the `var` keyword, the
	// parentheses and the declared types cut out — in site-relative
	// coordinates against the pristine source. Empty for the other two forms.
	//
	// They are splices rather than a rendered string so that the original bytes
	// survive: everything the declaration held that is not one of those tokens,
	// line breaks included, stays exactly where the user wrote it, and [Apply]
	// proves each cut covers the bytes it claims before anything is edited.
	undeclare []Splice
}

// A siteIndex answers "which node does this hint name?" for one parsed file.
//
// It is built by one walk over the syntax tree and then queried once per
// candidate, so a file with a hundred mutants is still one parse and one walk.
// Both maps are keyed by byte span rather than by [token.Pos], because a hint
// arrives from discovery holding byte offsets and nothing else.
//
// Statements and expressions are indexed apart because they collide: an
// expression statement covers exactly the bytes of the call inside it, and a
// Form S hint over `f()` means the statement while a Form C hint over the same
// bytes would mean the expression. Where two nodes of one kind share a span the
// outer one is kept — [ast.Inspect] visits it first — since it is the one an
// enclosing rewrite would have to replace.
type siteIndex struct {
	stmts map[mutation.Span]ast.Stmt
	exprs map[mutation.Span]ast.Expr
	// src is the pristine file, so that a cut can carry the bytes it removes.
	src []byte
	tok *token.File
}

// parseSnapshotFile parses pristine snapshot bytes, keeping every position
// exact.
//
// Positions have to be exact because every span in the catalogue and every span
// in a hint is a byte offset into these same bytes: the parse is how this
// package finds the node a hint names, and an approximate position would find
// the wrong one. It is also why the file is re-parsed here instead of reusing
// whatever discovery held — that tree belongs to another package's loader, and
// a shared one would tie instrumentation to a go/packages load it does not
// otherwise need.
//
// No type information is computed, and none is needed: the questions that need
// it were answered by discovery and travel in the hint. See [Hints].
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

// checkParses is the postcondition side of the same parse: instrumented output
// that go/parser rejects is a bug in this package, and one that would otherwise
// surface as a build failure in a generated tree the user never asked to read.
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

// parseGo parses one file's bytes and returns the parser's own error.
func parseGo(srcPath string, src []byte) (*ast.File, *token.File, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, srcPath, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}
	tok := fset.File(file.Package)
	if tok == nil {
		// Unreachable: ParseFile added the file to the set it was handed.
		return nil, nil, errors.New("parsed file has no position information")
	}
	return file, tok, nil
}

// newSiteIndex walks a parsed file once and records every node a hint could
// name.
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

// offset is the byte offset of a position in the file being indexed.
//
// [token.File.Offset] panics on a position outside the file, and every caller
// here passes one taken from the syntax tree just parsed from that file, which
// cannot be. Nothing derived from the catalogue reaches it: a hint's span is
// used as a map key or compared, never converted back into a position — the one
// place that does convert one, [siteIndex.position], checks it against the
// file's size first, because that one really is fed catalogue data.
func (x *siteIndex) offset(pos token.Pos) uint32 { return uint32(x.tok.Offset(pos)) }

// span is the byte range of a node.
func (x *siteIndex) span(node ast.Node) mutation.Span {
	return mutation.Span{StartByte: x.offset(node.Pos()), EndByte: x.offset(node.End())}
}

// siteFor turns one mutant's hint into the site the renderer works from,
// checking everything the hint claims against the file that is really there.
//
// Three things are checked and none of them is ceremony. The site has to
// contain the edit, or the rewrite would splice a mutation into bytes that do
// not hold it. The site has to be a node of the kind its form needs, or the
// hint describes a different file than the one on disk. And a Form D site has
// to be a declaration this form can express, because turning one into an
// assignment is a byte edit over its own tokens rather than a re-rendering.
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

// wrappableStatement reports whether a statement may be buried in a block.
//
// The list is exactly the one [discover.Guard] documents for Form S, and it is
// short for one reason: every statement here declares nothing, so wrapping it
// in `if … { … } else { … }` changes no scope and the code after it goes on
// compiling. A `:=` and a `var` do declare, which is what Form D exists for; an
// `if`, a `for` or a block is never a hint this package should see, and a hint
// naming one means discovery and this package have drifted apart.
//
// `defer` and `go` are in the list and are wrapped whole, statement and all,
// rather than having their call rewritten in place. Both are function-scoped
// rather than block-scoped: a `defer` inside the guard's block still runs when
// the enclosing *function* returns, and a `go` still starts its goroutine, so
// the block the guard adds changes nothing about when either fires.
func wrappableStatement(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ExprStmt, *ast.ReturnStmt, *ast.IncDecStmt, *ast.SendStmt, *ast.DeferStmt, *ast.GoStmt:
		return true
	case *ast.AssignStmt:
		return s.Tok != token.DEFINE
	default:
		return false
	}
}

// undeclare computes the cuts that turn one declaring statement into plain
// assignments, in coordinates relative to the site.
//
// Two shapes reach here, and each is handled by removing the tokens that make
// it a declaration and nothing else:
//
//   - `x, y := f()` loses one byte: the `:` of its `:=`. Everything else — the
//     names, the spacing, the whole right-hand side, every line break in it —
//     is the user's own bytes, still in place.
//   - `var x T = f()` loses the `var` keyword and the type, and a parenthesized
//     `var ( … )` block loses its parentheses too, which leaves the specs
//     inside it as a list of assignments separated by the line breaks that were
//     already there. A spec with no initialiser has nothing to assign and is
//     cut whole; the name it declared is still declared, by the `var` the guard
//     writes in front of itself from the hint's [discover.DeclType] list.
//
// Every cut has to be free of line breaks for the rewrite to stay
// line-preserving. Two of them are as long as the source says — a spec with no
// initialiser, and a spelled-out type — and discovery is what keeps those on
// one line: a `var` it cannot undeclare this way is refused there, so no hint
// naming one ever arrives, and the candidate is a recorded skip rather than a
// failed run. That refusal belongs to the phase that can decline a candidate;
// this one can only fail a whole file.
//
// The check is still made, by the caller rather than here: the splices go
// through [LinePreserving] together with the nested guards, so a cut that does
// hold a line break is reported as [CodeLineDrift] against the site instead of
// being silently swallowed. Reaching it means discovery and this package have
// drifted apart, which is what an internal error is for.
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

// undeclareVar is [siteIndex.undeclare] for a `var` declaration.
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
		// A spec with no initialiser is not an assignment and cannot become
		// one, so the whole of it goes; the guard declares its names anyway.
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

// cut is the splice that removes the bytes between two positions, expressed
// relative to base. The bytes it carries are read from the file, so the splice
// describes what is really there and [Apply] has something to check the site
// against.
func (x *siteIndex) cut(from, to token.Pos, base mutation.Span) Splice {
	span := mutation.Span{StartByte: x.offset(from), EndByte: x.offset(to)}
	return Splice{
		Span:     relativeTo(span, base.StartByte),
		Original: x.src[span.StartByte:span.EndByte],
	}
}

// rewriteToken is [siteIndex.cut] for one fixed token, checked against the
// bytes at its position.
//
// The check is what turns a hint that no longer describes the file into a
// refusal rather than an edit. Every position here comes from the parsed file
// and so cannot miss, which is precisely why the one way it could — a future
// caller passing a position from somewhere else — is worth a line of code.
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

// notFound builds the "the file is not what the hint says" error.
func (x *siteIndex) notFound(m mutation.Mutant, srcPath string, span mutation.Span, detail string) error {
	return &Error{
		Code: CodeSiteNotFound,
		Message: fmt.Sprintf("%s: mutant %s (%s) cannot be instrumented: its guard hint names the site %s and %s",
			x.position(srcPath, m.Span), m.DisplayID, m.Rule, span, detail),
	}
}

// unsupported builds the "this hint names a shape no guard form covers" error.
func (x *siteIndex) unsupported(m mutation.Mutant, srcPath string, span mutation.Span, detail string) error {
	return &Error{
		Code: CodeUnsupportedGuard,
		Message: fmt.Sprintf("%s: mutant %s (%s) cannot be instrumented: its guard hint names the site %s, where %s",
			x.position(srcPath, m.Span), m.DisplayID, m.Rule, span, detail),
	}
}

// position renders a span's start as the file:line:column a user would look
// for. Out-of-range offsets fall back to the byte range, since a diagnostic
// must not panic on the very drift it is reporting.
func (x *siteIndex) position(srcPath string, span mutation.Span) string {
	if uint64(span.StartByte) > uint64(x.tok.Size()) {
		return srcPath + " " + span.String()
	}
	pos := x.tok.PositionFor(x.tok.Pos(int(span.StartByte)), false)
	return fmt.Sprintf("%s:%d:%d", srcPath, pos.Line, pos.Column)
}

// buildSites arranges one file's mutants into the forest of rewrite sites they
// occupy, and the site each of those nodes is.
//
// Identical site spans become alternatives of one node — six comparison rules
// rewriting one operator are one guard with six branches, and an arithmetic
// swap and a statement deletion on one statement are one guard with two, family
// notwithstanding — and nested sites become children. Partial overlap cannot
// happen: two nodes of one syntax tree either nest or are disjoint, so a
// conflict means a hint's site span does not describe a node at all, and it is
// reported as the internal error it is.
func buildSites(
	index *siteIndex,
	srcPath string,
	mutants []mutation.Mutant,
	hints Hints,
) (interval.Forest[mutation.Mutant], map[mutation.Span]site, error) {
	items := make([]interval.Item[mutation.Mutant], 0, len(mutants))
	sites := make(map[mutation.Span]site, len(mutants))
	for _, m := range mutants {
		guard, err := hints.guardFor(m, srcPath)
		if err != nil {
			return interval.Forest[mutation.Mutant]{}, nil, err
		}
		resolved, err := index.siteFor(m, guard, srcPath)
		if err != nil {
			return interval.Forest[mutation.Mutant]{}, nil, err
		}
		if previous, seen := sites[resolved.span]; seen {
			if err := agree(previous, resolved, m, srcPath); err != nil {
				return interval.Forest[mutation.Mutant]{}, nil, err
			}
		}
		sites[resolved.span] = resolved
		items = append(items, interval.Item[mutation.Mutant]{Span: resolved.span, Payload: m})
	}
	forest, err := placeSites(srcPath, items)
	if err != nil {
		return interval.Forest[mutation.Mutant]{}, nil, err
	}
	return forest, sites, nil
}

// agree refuses two hints that name one site and disagree about what it is.
//
// The form and the declared names are properties of the site rather than of the
// mutant, so two candidates in one statement must produce the same answer.
// Rendering one guard from two contradictory hints would mean silently picking
// whichever arrived first, which is the kind of order dependence the whole
// phase is built to keep out.
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

// placeSites is the forest placement itself, split out from the site
// computation so that the invariant it enforces can be exercised directly.
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

// relativeTo re-expresses a span in coordinates that start at base.
func relativeTo(span mutation.Span, base uint32) mutation.Span {
	return mutation.Span{StartByte: span.StartByte - base, EndByte: span.EndByte - base}
}
