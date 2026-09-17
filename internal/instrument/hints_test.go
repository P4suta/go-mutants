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

type hintOptions struct {
	declared      map[string]string
	namedBool     []string
	unprobed      []string
	unprobedSites []string
	valueTypes    map[string]string
}

var returnValueRules = map[string]bool{
	"return-zero-numeric": true,
	"return-empty-string": true,
	"return-true":         true,
	"return-false":        true,
	"return-nil":          true,
	"return-err-to-nil":   true,
}

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
		hints[m.ID] = deriver.guardFor(m.Span, m.Rule.Name)
	}
	return hints
}

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
		hints[id] = deriver.guardFor(candidate.Span, candidate.Rule.Name)
	}
	return hints
}

func hintsInSource(t *testing.T, src []byte, catalog *mutation.Catalog, opts hintOptions) instrument.Hints {
	t.Helper()

	deriver := newHintDeriver(t, sampleFile, src, opts)
	hints := make(instrument.Hints, catalog.Len())
	for _, m := range catalog.Mutants() {
		hints[m.ID] = deriver.guardFor(m.Span, m.Rule.Name)
	}
	return hints
}

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

func (d *hintDeriver) guardFor(span mutation.Span, rule string) discover.Guard {
	d.t.Helper()

	anchor := d.anchor(span)
	if anchor == nil {
		d.t.Fatalf("%s: no node covers %s", d.path, span)
	}
	guard, ok := d.formC(anchor)
	if ok {
		guard.Probe = d.boolSite(guard)
	} else {
		if guard, ok = d.statementSite(anchor); !ok {
			d.t.Fatalf("%s: no guard form covers the edit at %s (%q)", d.path, span, d.text(anchor))
		}
	}
	if guard.Probe == nil {
		guard.Probe = d.valueSite(anchor)
	}
	if guard.Probe == nil && deletionRules[rule] {
		guard.Probe = d.reachSite(guard)
	}
	if site := d.returnSite(anchor, span, rule); site != nil {
		guard.Probe = site
	}
	return guard
}

var deletionRules = map[string]bool{
	"delete-call-statement": true,
	"delete-assignment":     true,
	"delete-incdec":         true,
}

func (d *hintDeriver) reachSite(guard discover.Guard) *discover.ProbeSite {
	d.t.Helper()

	if guard.Form != discover.GuardFormS {
		return nil
	}
	return &discover.ProbeSite{Form: discover.ProbeFormReach, Span: guard.SiteSpan}
}

func (d *hintDeriver) valueSite(anchor ast.Node) *discover.ProbeSite {
	d.t.Helper()

	if len(d.opts.valueTypes) == 0 {
		return nil
	}
	for node := anchor; node != nil; node = d.parent[node] {
		expr, ok := node.(ast.Expr)
		if !ok {
			return nil
		}
		spelled, named := d.opts.valueTypes[d.text(expr)]
		if !named {
			continue
		}
		return &discover.ProbeSite{
			Form:  discover.ProbeFormValue,
			Span:  d.span(expr),
			Types: []string{spelled},
		}
	}
	return nil
}

func (d *hintDeriver) boolSite(guard discover.Guard) *discover.ProbeSite {
	d.t.Helper()

	text := string(d.src[guard.SiteSpan.StartByte:guard.SiteSpan.EndByte])
	for _, refused := range d.opts.unprobedSites {
		if text == refused {
			return nil
		}
	}
	return &discover.ProbeSite{Form: discover.ProbeFormBool, Span: guard.SiteSpan}
}

func (d *hintDeriver) returnSite(anchor ast.Node, span mutation.Span, rule string) *discover.ProbeSite {
	d.t.Helper()

	if !returnValueRules[rule] {
		return nil
	}
	stmt, fn := d.enclosingReturn(anchor)
	if stmt == nil || fn == nil {
		d.t.Fatalf("%s: the %s candidate at %s is not inside a return statement", d.path, rule, span)
	}
	for _, refused := range d.opts.unprobed {
		if d.text(stmt) == refused {
			return nil
		}
	}
	results := d.resultTypes(fn)
	if len(results) != len(stmt.Results) {
		d.t.Fatalf("%s: the statement %q returns %d values and its function declares %d results",
			d.path, d.text(stmt), len(stmt.Results), len(results))
	}
	for i, value := range stmt.Results {
		if d.span(value).Contains(span) {
			return &discover.ProbeSite{Form: discover.ProbeFormReturn, Span: d.span(stmt), Types: results, Index: i}
		}
	}
	d.t.Fatalf("%s: the %s candidate at %s is in no result of %q", d.path, rule, span, d.text(stmt))
	return nil
}

func (d *hintDeriver) enclosingReturn(anchor ast.Node) (*ast.ReturnStmt, *ast.FuncType) {
	var stmt *ast.ReturnStmt
	for node := anchor; node != nil; node = d.parent[node] {
		switch n := node.(type) {
		case *ast.ReturnStmt:
			if stmt == nil {
				stmt = n
			}
		case *ast.FuncDecl:
			return stmt, n.Type
		case *ast.FuncLit:
			return stmt, n.Type
		}
	}
	return nil, nil
}

func (d *hintDeriver) resultTypes(fn *ast.FuncType) []string {
	if fn == nil || fn.Results == nil {
		return nil
	}
	var out []string
	for _, field := range fn.Results.List {
		spelled := d.text(field.Type)
		for range max(len(field.Names), 1) {
			out = append(out, spelled)
		}
	}
	return out
}

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

func (d *hintDeriver) span(node ast.Node) mutation.Span {
	return mutation.Span{
		StartByte: uint32(d.tok.Offset(node.Pos())),
		EndByte:   uint32(d.tok.Offset(node.End())),
	}
}

func (d *hintDeriver) text(node ast.Node) string {
	span := d.span(node)
	return string(d.src[span.StartByte:span.EndByte])
}

type editSpec struct {
	rule string
	in   string
	find string
	with string
}

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
