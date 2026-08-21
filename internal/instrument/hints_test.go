// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// This file is the fixtures' half of the hint contract: given a fixture and the
// candidates it catalogues, it produces the [discover.Guard] discovery would
// have produced for each of them.
//
// It is deliberately a re-statement of internal/discover's rule rather than a
// call into it. Discovery answers the question with a type checker and a
// go/packages load; a fixture here is one file with no module around it, so the
// answer is derived from the syntax and from the two things syntax cannot say —
// what a `:=` declares, and which bool-valued expressions are of a named
// boolean type rather than the universe `bool` — which the fixture states for
// itself in [hintOptions]. That keeps the golden files a statement about the
// rewrite the instrumenter performs for a given hint, which is the only thing
// this package decides.
//
// It deliberately does not restate discovery's Form D *refusals*, because a
// refusal produces no hint and so has nothing to say about a rewrite. A fixture
// here must therefore not carry a declaration discovery would decline — one
// whose declared type or whose initialiser-less spec is spelled across lines,
// or whose initialiser mentions a name it declares. This would hand the
// instrumenter a Form D hint discovery never emits, and the failure would
// surface as a line-drift error in a golden test rather than as the missing
// candidate it really is. internal/instrument's own integration suite covers
// those shapes against the real discovery pass.

// hintOptions are the answers a syntax tree does not hold, stated by the
// fixture that needs them.
type hintOptions struct {
	// declared gives the type each name a Form D site declares is declared as,
	// by name. A fixture with a `:=` site must name every variable it declares:
	// the type is what the guard writes in front of itself, and there is no
	// honest way to guess it from the syntax.
	declared map[string]string
	// namedBool lists the expressions whose type is a named boolean type rather
	// than the universe `bool`, spelled exactly as the fixture writes them. Such
	// an expression is not a Form C site — a selector produces a plain `bool`,
	// which is not assignable to a named boolean type — so discovery hands it to
	// the statement form instead, and this is how a fixture says so.
	namedBool []string
}

// hintsFor derives the hints of a whole catalogue, reading each catalogued file
// out of the snapshot it was catalogued from.
func hintsFor(t *testing.T, root string, catalog *mutation.Catalog, opts hintOptions) instrument.Hints {
	t.Helper()

	deriving := make(map[string]*hintDeriver)
	hints := make(instrument.Hints, catalog.Len())
	for _, m := range catalog.Mutants() {
		deriver, ok := deriving[m.Path]
		if !ok {
			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.Path)))
			if err != nil {
				t.Fatalf("reading the catalogued %s: %v", m.Path, err)
			}
			deriver = newHintDeriver(t, m.Path, src, opts)
			deriving[m.Path] = deriver
		}
		hints[m.ID] = deriver.guardFor(m.Span)
	}
	return hints
}

// hintsOfCandidates derives the hints of one file's candidates before they are
// catalogued, keyed by the id each of them hashes to.
//
// It is what lets several fixtures' hints be merged into the one index a
// whole-tree pass needs: a mutant id is unique across every file, so the union
// of the maps is the index of the union of the catalogues.
func hintsOfCandidates(
	t *testing.T,
	path string,
	src []byte,
	candidates []mutation.Candidate,
	opts hintOptions,
) instrument.Hints {
	t.Helper()

	deriver := newHintDeriver(t, path, src, opts)
	hints := make(instrument.Hints, len(candidates))
	for _, candidate := range candidates {
		id, err := candidate.ID()
		if err != nil {
			t.Fatalf("identifying the candidate at %s %s: %v", candidate.Path, candidate.Span, err)
		}
		hints[id] = deriver.guardFor(candidate.Span)
	}
	return hints
}

// hintsInSource derives the hints of one file's mutants without a snapshot on
// disk, for the tests that never write one.
func hintsInSource(t *testing.T, src []byte, catalog *mutation.Catalog, opts hintOptions) instrument.Hints {
	t.Helper()

	deriver := newHintDeriver(t, sampleFile, src, opts)
	hints := make(instrument.Hints, catalog.Len())
	for _, m := range catalog.Mutants() {
		hints[m.ID] = deriver.guardFor(m.Span)
	}
	return hints
}

// A hintDeriver answers the hint question for one parsed fixture.
type hintDeriver struct {
	t      *testing.T
	path   string
	src    []byte
	file   *ast.File
	tok    *token.File
	parent map[ast.Node]ast.Node
	opts   hintOptions
}

func newHintDeriver(t *testing.T, path string, src []byte, opts hintOptions) *hintDeriver {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	d := &hintDeriver{
		t:      t,
		path:   path,
		src:    src,
		file:   file,
		tok:    fset.File(file.Package),
		parent: make(map[ast.Node]ast.Node),
		opts:   opts,
	}
	var stack []ast.Node
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if len(stack) > 0 {
			d.parent[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	return d
}

// guardFor is the hint for one candidate's span, or a fatal error: a fixture
// that catalogues an edit no guard form covers is a fixture with a mistake in
// it, not a case worth carrying.
func (d *hintDeriver) guardFor(span mutation.Span) discover.Guard {
	d.t.Helper()

	anchor := d.anchor(span)
	if anchor == nil {
		d.t.Fatalf("%s: no node covers %s", d.path, span)
	}
	if guard, ok := d.formC(anchor); ok {
		return guard
	}
	if guard, ok := d.statementSite(anchor); ok {
		return guard
	}
	d.t.Fatalf("%s: no guard form covers the edit at %s (%q)", d.path, span, d.text(anchor))
	return discover.Guard{}
}

// anchor is the innermost node covering a candidate's span, which is the node
// discovery would have anchored the rule to: the binary expression an operator
// sits in, the identifier a literal is, the statement a deletion removes.
func (d *hintDeriver) anchor(span mutation.Span) ast.Node {
	var best ast.Node
	ast.Inspect(d.file, func(node ast.Node) bool {
		if node == nil {
			return false
		}
		if d.span(node).Contains(span) {
			best = node
		}
		return true
	})
	return best
}

// formC looks outward for the nearest bool-valued expression that may be
// wrapped, stopping at the first ancestor that is not an expression.
func (d *hintDeriver) formC(anchor ast.Node) (discover.Guard, bool) {
	for node := anchor; node != nil; node = d.parent[node] {
		expr, ok := node.(ast.Expr)
		if !ok {
			return discover.Guard{}, false
		}
		if !d.universeBool(expr) || !d.wrappablePosition(expr) {
			continue
		}
		return discover.Guard{Form: discover.GuardFormC, SiteSpan: d.span(expr)}, true
	}
	return discover.Guard{}, false
}

// universeBool reports whether an expression is one of the shapes that yields a
// bool, and is not one the fixture declared to be of a named boolean type.
func (d *hintDeriver) universeBool(expr ast.Expr) bool {
	for _, named := range d.opts.namedBool {
		if d.text(expr) == named {
			return false
		}
	}
	switch e := expr.(type) {
	case *ast.BinaryExpr:
		switch e.Op {
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ, token.LAND, token.LOR:
			return true
		}
		return false
	case *ast.UnaryExpr:
		return e.Op == token.NOT
	case *ast.ParenExpr:
		return d.universeBool(e.X)
	case *ast.Ident:
		return e.Name == "true" || e.Name == "false"
	default:
		return false
	}
}

// wrappablePosition is internal/discover's rule for where a parenthesized
// expression of the same type is legal, restated over the shapes the fixtures
// use.
func (d *hintDeriver) wrappablePosition(expr ast.Expr) bool {
	switch parent := d.parent[expr].(type) {
	case *ast.ExprStmt, *ast.DeferStmt, *ast.GoStmt:
		return false
	case *ast.SelectorExpr:
		return parent.Sel != expr
	case *ast.UnaryExpr:
		return parent.Op != token.AND
	case *ast.IncDecStmt:
		return parent.X != expr
	case *ast.AssignStmt:
		for _, lhs := range parent.Lhs {
			if lhs == expr {
				return false
			}
		}
		return true
	case *ast.RangeStmt:
		return parent.Key != expr && parent.Value != expr
	case *ast.KeyValueExpr:
		return parent.Key != expr
	default:
		return true
	}
}

// statementSite looks outward for the nearest statement and decides which
// statement form covers it, stopping at the enclosing function.
func (d *hintDeriver) statementSite(anchor ast.Node) (discover.Guard, bool) {
	for node := anchor; node != nil; node = d.parent[node] {
		switch n := node.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			return discover.Guard{}, false
		case ast.Stmt:
			if !d.blockIsLegalFor(n) {
				return discover.Guard{}, false
			}
			return d.statementGuard(n)
		}
	}
	return discover.Guard{}, false
}

// blockIsLegalFor reports whether a statement may be replaced by an `if`
// statement, which no simple-statement slot allows.
func (d *hintDeriver) blockIsLegalFor(stmt ast.Stmt) bool {
	switch parent := d.parent[stmt].(type) {
	case *ast.ForStmt:
		return parent.Init != stmt && parent.Post != stmt
	case *ast.IfStmt:
		return parent.Init != stmt
	case *ast.SwitchStmt:
		return parent.Init != stmt
	case *ast.TypeSwitchStmt:
		return parent.Init != stmt && parent.Assign != stmt
	case *ast.CommClause:
		return parent.Comm != stmt
	default:
		return true
	}
}

// statementGuard classifies one statement into Form S or Form D, on exactly the
// division internal/discover draws: a statement that declares is Form D.
func (d *hintDeriver) statementGuard(stmt ast.Stmt) (discover.Guard, bool) {
	span := d.span(stmt)
	switch s := stmt.(type) {
	case *ast.ExprStmt, *ast.ReturnStmt, *ast.IncDecStmt, *ast.SendStmt, *ast.DeferStmt, *ast.GoStmt:
		return discover.Guard{Form: discover.GuardFormS, SiteSpan: span}, true
	case *ast.AssignStmt:
		if s.Tok != token.DEFINE {
			return discover.Guard{Form: discover.GuardFormS, SiteSpan: span}, true
		}
		names := make([]*ast.Ident, 0, len(s.Lhs))
		for _, lhs := range s.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok {
				return discover.Guard{}, false
			}
			names = append(names, ident)
		}
		return discover.Guard{Form: discover.GuardFormD, SiteSpan: span, DeclTypes: d.declTypes(names)}, true
	case *ast.DeclStmt:
		gen, ok := s.Decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			return discover.Guard{}, false
		}
		var names []*ast.Ident
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				return discover.Guard{}, false
			}
			names = append(names, value.Names...)
		}
		return discover.Guard{Form: discover.GuardFormD, SiteSpan: span, DeclTypes: d.declTypes(names)}, true
	default:
		return discover.Guard{}, false
	}
}

// declTypes pairs every declared name with the type the fixture says it has.
// The blank identifier declares nothing and is passed over, exactly as
// discovery passes over it.
func (d *hintDeriver) declTypes(names []*ast.Ident) []discover.DeclType {
	d.t.Helper()

	out := make([]discover.DeclType, 0, len(names))
	for _, name := range names {
		if name.Name == "_" {
			continue
		}
		declared, ok := d.opts.declared[name.Name]
		if !ok {
			d.t.Fatalf("%s: the fixture declares %q and does not say what type it has", d.path, name.Name)
		}
		out = append(out, discover.DeclType{Name: name.Name, Type: declared})
	}
	return out
}

// span is the byte range of a node.
func (d *hintDeriver) span(node ast.Node) mutation.Span {
	return mutation.Span{
		StartByte: uint32(d.tok.Offset(node.Pos())),
		EndByte:   uint32(d.tok.Offset(node.End())),
	}
}

// text is a node's own bytes.
func (d *hintDeriver) text(node ast.Node) string {
	span := d.span(node)
	return string(d.src[span.StartByte:span.EndByte])
}

// An editSpec is one candidate as a fixture states it: the rule, the bytes it
// replaces located by a snippet that holds them, and what it writes.
//
// Locating an edit by text rather than by offset is what keeps the fixtures
// editable: a comment added at the top of a fixture would otherwise renumber
// every span in its table.
type editSpec struct {
	// rule is the canonical registry name of the operator.
	rule string
	// in is a snippet of the fixture that holds the edit, and must occur in it
	// exactly once.
	in string
	// find is the text to replace, located inside the first occurrence of in.
	// Empty means the whole of in, which is how a statement deletion is written.
	find string
	// with is the replacement, empty for a deletion.
	with string
}

// editsIn resolves a fixture's edit table into candidates.
func editsIn(t *testing.T, src []byte, edits ...editSpec) []mutation.Candidate {
	t.Helper()

	text := string(src)
	digest := mutation.Digest(src)
	out := make([]mutation.Candidate, 0, len(edits))
	for _, edit := range edits {
		start := strings.Index(text, edit.in)
		if start < 0 {
			t.Fatalf("the fixture does not hold %q", edit.in)
		}
		if strings.Contains(text[start+1:], edit.in) {
			t.Fatalf("the fixture holds %q more than once, so it does not locate an edit", edit.in)
		}
		original := edit.in
		if edit.find != "" {
			within := strings.Index(edit.in, edit.find)
			if within < 0 {
				t.Fatalf("%q does not hold %q", edit.in, edit.find)
			}
			start += within
			original = edit.find
		}
		out = append(out, mutation.Candidate{
			Path:         sampleFile,
			Rule:         lookupRule(t, edit.rule),
			Span:         mutation.Span{StartByte: uint32(start), EndByte: uint32(start + len(original))},
			Original:     original,
			Replacement:  edit.with,
			SourceDigest: digest,
		})
	}
	return out
}
