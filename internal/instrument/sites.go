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
	"strconv"

	"github.com/P4suta/go-mutants/internal/interval"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// comparisonOps are the operators whose [ast.BinaryExpr] is bool-valued, and
// so the operators whose enclosing expression Form C may wrap.
//
// The set is spelled out here rather than derived from the rule table because
// it answers a different question. internal/discover knows which operator each
// rule rewrites; this package has to know which expressions are bool-valued,
// and a future rule that rewrites "+" into "-" must not be wrapped in a boolean
// guard just because it reached this file.
var comparisonOps = map[token.Token]bool{
	token.EQL: true,
	token.NEQ: true,
	token.LSS: true,
	token.LEQ: true,
	token.GTR: true,
	token.GEQ: true,
}

// booleanLiterals are the two identifiers the boolean-literal family rewrites.
//
// Checking the name here is not redundant with the catalogue having said so.
// Without type information this package cannot tell the predeclared constant
// from a field of the same name — Go lets you declare one — and what it can
// tell is that a candidate claiming to rewrite a boolean literal at a span
// holding neither "true" nor "false" describes a different file than the one on
// disk. That is a refusal, not a guess.
var booleanLiterals = map[string]bool{"true": true, "false": true}

// A siteIndex answers "which expression encloses this edit?" for one parsed
// file.
//
// It is built by one walk over the syntax tree and then queried once per
// candidate, so a file with a hundred mutants is still one parse and one walk.
// Both maps are keyed by byte offset rather than by [token.Pos], because a
// candidate arrives from the catalogue holding offsets and nothing else.
type siteIndex struct {
	// binary maps the offset of an operator token to the binary expression it
	// belongs to. Operator positions are unique within a file, so the key
	// identifies exactly one node.
	binary map[uint32]*ast.BinaryExpr
	// idents maps an identifier's whole span to the identifier.
	idents map[mutation.Span]*ast.Ident
	tok    *token.File
}

// parseSnapshotFile parses pristine snapshot bytes, keeping every position
// exact.
//
// Positions have to be exact because every span in the catalogue is a byte
// offset into these same bytes: the parse is how this package finds the
// expression enclosing an edit, and an approximate position would find the
// wrong one. It is also why the file is re-parsed here instead of reusing
// whatever discovery held — that tree belongs to another package's loader, and
// a shared one would tie instrumentation to a go/packages load it does not
// otherwise need.
//
// No type information is computed, and none is needed: see the package
// documentation on named boolean types for what this package deliberately
// leaves to the validation phase.
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

// newSiteIndex walks a parsed file once and records everything a candidate
// could be anchored to.
//
// The identifier at the end of a selector is deliberately not recorded. `x.true`
// is a field or method named "true", not the predeclared constant, and it is the
// one shape where a boolean-literal span could land on an identifier that is not
// a boolean literal at all. Leaving it out turns that into a [CodeSiteNotFound]
// error instead of a guard wrapped around a field name.
func newSiteIndex(tok *token.File, file *ast.File) *siteIndex {
	x := &siteIndex{
		binary: make(map[uint32]*ast.BinaryExpr),
		idents: make(map[mutation.Span]*ast.Ident),
		tok:    tok,
	}
	// A selector's own identifier is marked on the way past its parent, which
	// ast.Inspect always visits first, rather than by pruning the walk there:
	// the expression a selector is taken of is ordinary code that can hold
	// sites of its own, and pruning would lose them.
	selectors := make(map[*ast.Ident]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.BinaryExpr:
			x.binary[x.offset(n.OpPos)] = n
		case *ast.SelectorExpr:
			selectors[n.Sel] = true
		case *ast.Ident:
			if !selectors[n] {
				x.recordIdent(n)
			}
		}
		return true
	})
	return x
}

// recordIdent files one identifier under its own span.
func (x *siteIndex) recordIdent(ident *ast.Ident) {
	x.idents[mutation.Span{StartByte: x.offset(ident.Pos()), EndByte: x.offset(ident.End())}] = ident
}

// offset is the byte offset of a position in the file being indexed.
//
// [token.File.Offset] panics on a position outside the file, and every caller
// here passes one taken from the syntax tree just parsed from that file, which
// cannot be. Nothing derived from the catalogue reaches it: a candidate's span
// is used as a map key or compared, never converted back into a position — the
// one place that does convert one, [siteIndex.position], checks it against the
// file's size first, because that one really is fed catalogue data.
func (x *siteIndex) offset(pos token.Pos) uint32 { return uint32(x.tok.Offset(pos)) }

// span is the byte range of a node.
func (x *siteIndex) span(node ast.Node) mutation.Span {
	return mutation.Span{StartByte: x.offset(node.Pos()), EndByte: x.offset(node.End())}
}

// siteFor returns the rewrite site of one catalogued mutant: the innermost
// enclosing expression that is bool-valued and safe to wrap in a Form C guard.
//
// For a comparison the site is the whole binary expression, because the edit
// itself is one operator token — bytes that are not an expression and cannot be
// guarded on their own. For a boolean literal the identifier already is the
// expression, so the site is the candidate's own span.
//
// Everything the candidate claims is checked against the parsed file before a
// site is returned. A candidate that says it replaces "==" at an offset where
// the file holds a "+" is a catalogue that no longer describes this tree, and
// the honest answer is an error rather than a boolean guard around an integer.
func (x *siteIndex) siteFor(m mutation.Mutant, srcPath string) (mutation.Span, error) {
	switch m.Rule.Family {
	case mutation.FamilyComparison:
		expr, ok := x.binary[m.Span.StartByte]
		if !ok || !comparisonOps[expr.Op] || expr.Op.String() != m.Original {
			return mutation.Span{}, x.notFound(m, srcPath, "no comparison operator "+strconv.Quote(m.Original)+" starts here")
		}
		return x.span(expr), nil
	case mutation.FamilyBooleanLiteral:
		ident, ok := x.idents[m.Span]
		if !ok || ident.Name != m.Original || !booleanLiterals[ident.Name] {
			return mutation.Span{}, x.notFound(m, srcPath, "no boolean literal "+strconv.Quote(m.Original)+" covers these bytes")
		}
		return m.Span, nil
	default:
		return mutation.Span{}, &Error{
			Code: CodeUnsupportedFamily,
			Message: fmt.Sprintf("%s: mutant %s belongs to the %q family, which needs a guard form this version does not emit",
				x.position(srcPath, m.Span), m.DisplayID, m.Rule.Family),
		}
	}
}

// notFound builds the "the file is not what the catalogue says" error.
func (x *siteIndex) notFound(m mutation.Mutant, srcPath, detail string) error {
	return &Error{
		Code: CodeSiteNotFound,
		Message: fmt.Sprintf("%s: mutant %s (%s) cannot be instrumented: %s",
			x.position(srcPath, m.Span), m.DisplayID, m.Rule, detail),
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

// A guardRenderer turns one file's rewrite sites into Form C guards.
type guardRenderer struct {
	// path is the module-relative path, for diagnostics only.
	path string
	// src is the pristine file, the coordinate system every span in the forest
	// is expressed in.
	src []byte
	// alias is the local name the runtime package is imported under in this
	// file, and the guards written into it have to spell whatever that turned
	// out to be. It varies from file to file because "__gm" may already be
	// taken — by something this file spells, or by something the package block
	// binds anywhere in the package — in which case [aliasFor] bumps it.
	alias string
}

// A siteNode is one node of the rewrite forest for a file.
type siteNode = interval.Node[mutation.Mutant]

// render composes every site of one file, children before parents, and returns
// the splices to apply to the pristine bytes.
//
// The composition is bottom-up in parent-relative coordinates: a site's
// original text is its own pristine bytes with each child's finished guard
// spliced in, and only the outermost sites are ever spliced against the file
// itself. [interval.Forest.InnerFirst] supplies the order that makes this
// possible; the [OffsetMap] each nested [Apply] returns is deliberately unused,
// because composing in a child's parent-relative coordinates is the same
// arithmetic done by construction rather than by lookup, and it never leaves
// the file's own coordinate system to begin with.
// The second return value is the number of guards written: one per site,
// nested sites included, which is what a file's guard count means. Several
// mutants of one expression are alternatives inside a single guard.
func (r *guardRenderer) render(forest interval.Forest[mutation.Mutant]) ([]Splice, int, error) {
	rendered := make(map[*siteNode][]byte)
	var failure error
	forest.InnerFirst(func(node *siteNode) {
		if failure != nil {
			return
		}
		text, err := r.site(node, rendered)
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
			Original:    r.original(root.Span),
			Replacement: rendered[root],
		})
	}
	return splices, len(rendered), nil
}

// site renders one node: its children are folded into its original text, and
// the guard is wrapped around the result.
func (r *guardRenderer) site(node *siteNode, rendered map[*siteNode][]byte) ([]byte, error) {
	base := r.original(node.Span)

	splices := make([]Splice, 0, len(node.Children))
	for _, child := range node.Children {
		splices = append(splices, Splice{
			Span:        relativeTo(child.Span, node.Span.StartByte),
			Original:    r.original(child.Span),
			Replacement: rendered[child],
		})
	}
	if !LinePreserving(splices) {
		return nil, r.lineDrift("folding nested guards into the site at " + node.Span.String())
	}
	orig, _, err := Apply(base, splices)
	if err != nil {
		return nil, err
	}
	return r.guard(node, orig)
}

// guard renders the Form C selector for one site.
//
// The shape, for alternatives i1..in with mutated renderings m1..mn and the
// site's current text ORIG, is
//
//	(A.M[i1] && (m1) || … || !(A.M[i1] || … || A.M[in]) && (ORIG))
//
// where A is this file's alias for the runtime package. Both branches are
// ordinary expressions in the site's own type context, so the compiler settles
// typing, evaluation order, and short-circuiting; exactly one of them is ever
// evaluated, and with every flag false that one is ORIG, byte for byte the
// source the user wrote.
//
// Every byte written before ORIG is on ORIG's first line and every byte written
// after it is on ORIG's last line: the prefix holds no line break, and each mk
// is flattened onto one line by [Flatten]. That is what keeps a rewritten
// multi-line condition line-preserving.
func (r *guardRenderer) guard(node *siteNode, orig []byte) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('(')
	for _, m := range node.Alternatives {
		mutated, err := r.mutated(node.Span, m)
		if err != nil {
			return nil, err
		}
		b.WriteString(r.flag(m))
		b.WriteString(" && (")
		b.Write(mutated)
		b.WriteString(") || ")
	}
	b.WriteString("!(")
	for i, m := range node.Alternatives {
		if i > 0 {
			b.WriteString(" || ")
		}
		b.WriteString(r.flag(m))
	}
	b.WriteString(") && (")
	b.Write(orig)
	b.WriteString("))")

	if got, want := CountLines(b.Bytes()), CountLines(orig); got != want {
		return nil, r.lineDrift(fmt.Sprintf("the guard at %s spans %d lines, its site spans %d", node.Span, got+1, want+1))
	}
	return b.Bytes(), nil
}

// flag renders one mutant's activation lookup, "A.M[7]".
func (r *guardRenderer) flag(m mutation.Mutant) string {
	return r.alias + ".M[" + strconv.FormatUint(uint64(m.Index), 10) + "]"
}

// mutated renders one alternative: the site as it reads with that single
// candidate's edit applied, folded onto one line.
//
// It is rendered from the pristine bytes and never from the site's current
// text. A mutant is one edit to the program the user wrote, so the copy that
// runs when its flag is set must not carry the guards of the sites nested
// inside it — those would make it a different mutant, and one whose identity
// nothing in the catalogue describes.
func (r *guardRenderer) mutated(site mutation.Span, m mutation.Mutant) ([]byte, error) {
	if !site.Contains(m.Span) {
		return nil, &Error{
			Code: CodeSiteConflict,
			Message: fmt.Sprintf("%s: mutant %s at %s is not inside its own site %s",
				r.path, m.DisplayID, m.Span, site),
		}
	}
	patched, _, err := Apply(r.original(site), []Splice{{
		Span:        relativeTo(m.Span, site.StartByte),
		Original:    []byte(m.Original),
		Replacement: []byte(m.Replacement),
	}})
	if err != nil {
		return nil, err
	}
	return Flatten(patched)
}

// original returns the pristine bytes a span covers. The span came out of the
// forest, which was built from spans this package computed against these very
// bytes, so it fits by construction.
func (r *guardRenderer) original(span mutation.Span) []byte {
	return r.src[span.StartByte:span.EndByte]
}

// lineDrift builds the line-preservation failure.
func (r *guardRenderer) lineDrift(detail string) error {
	return &Error{
		Code:    CodeLineDrift,
		Message: "internal error: instrumenting " + strconv.Quote(r.path) + " would move a line: " + detail,
	}
}

// relativeTo re-expresses a span in coordinates that start at base.
func relativeTo(span mutation.Span, base uint32) mutation.Span {
	return mutation.Span{StartByte: span.StartByte - base, EndByte: span.EndByte - base}
}

// buildSites arranges one file's mutants into the forest of rewrite sites they
// occupy.
//
// Identical site spans become alternatives of one node — six comparison rules
// rewriting one operator are one guard with six branches, not six guards — and
// nested sites become children. Partial overlap cannot happen here: two
// expressions of one syntax tree either nest or are disjoint, so a conflict is
// a site span this package computed wrong, and it is reported as the internal
// error it is.
func buildSites(index *siteIndex, srcPath string, mutants []mutation.Mutant) (interval.Forest[mutation.Mutant], error) {
	items := make([]interval.Item[mutation.Mutant], 0, len(mutants))
	for _, m := range mutants {
		span, err := index.siteFor(m, srcPath)
		if err != nil {
			return interval.Forest[mutation.Mutant]{}, err
		}
		items = append(items, interval.Item[mutation.Mutant]{Span: span, Payload: m})
	}
	return placeSites(srcPath, items)
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
