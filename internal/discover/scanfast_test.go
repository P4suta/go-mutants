// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// scanned is what a fast scan produced: the candidates and the skip sites, the
// two halves of what discovery says about a file.
type scanned struct {
	candidates []Located
	sites      []SkipSite
}

// scanSource parses and type-checks one file of Go source and runs the whole
// mutation walk over it with every implemented rule selected, without loading a
// package or starting a toolchain.
//
// It is the fast counterpart to the toolchain-driven tests in discover_test.go:
// those load a fixture module through go/packages to exercise the same walk,
// which costs a package load each. This feeds [discovery.scanParsed] exactly
// what go/parser and go/types produce, so a test can pin the AST-level
// behaviour of a single construct in milliseconds. A fixture that needs no
// import type-checks with no importer at all; one that imports the standard
// library is checked from source, which needs no build.
//
// The source is treated as the file `pkg/scan.go` of package
// `example.com/m/pkg`, so a test asserting a path or an import path knows what
// to expect.
func scanSource(t *testing.T, src string) scanned {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "scan.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing the source:\n%s\n%v", src, err)
	}

	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	var typeErrs []error
	conf := types.Config{
		Importer: importer.ForCompiler(fset, "source", nil),
		Error:    func(e error) { typeErrs = append(typeErrs, e) },
	}
	pkg, _ := conf.Check("example.com/m/pkg", fset, []*ast.File{file}, info)
	if len(typeErrs) > 0 {
		t.Fatalf("the fixture does not type-check (a scan fixture must):\n%s\n%v", src, typeErrs)
	}

	matchers, err := newMatchers(SupportedRules())
	if err != nil {
		t.Fatalf("building the matchers: %v", err)
	}
	d := &discovery{
		root:     "/module",
		matchers: matchers,
		skips:    map[skipKey]int{},
		seen:     map[string]bool{},
		digests:  map[string]string{},
	}
	tokFile := fset.File(file.Package)
	if err := d.scanParsed("pkg/scan.go", "example.com/m/pkg", []byte(src), tokFile, file, info, pkg); err != nil {
		t.Fatalf("scanParsed:\n%s\n%v", src, err)
	}
	return scanned{candidates: d.candidates, sites: d.sites}
}

// rules returns the sorted "rule original->replacement" strings of the
// candidates, which is how a fast test names what a scan found.
func (s scanned) rules() []string {
	out := make([]string, 0, len(s.candidates))
	for _, c := range s.candidates {
		out = append(out, c.Rule.Name+" "+c.Original+"->"+c.Replacement)
	}
	return out
}

// has reports whether the scan produced a candidate of the given rule replacing
// original with replacement.
func (s scanned) has(rule, original, replacement string) bool {
	for _, c := range s.candidates {
		if c.Rule.Name == rule && c.Original == original && c.Replacement == replacement {
			return true
		}
	}
	return false
}

// TestScanSourceFindsAComparison is the harness's own smoke test: a single
// comparison in an import-free file produces the comparison-family candidates,
// proving the fast path reaches the same walk the toolchain tests do.
func TestScanSourceFindsAComparison(t *testing.T) {
	t.Parallel()

	got := scanSource(t, `package pkg

func Positive(v int) bool {
	return v > 0
}
`)
	if !got.has("gt-to-ge", ">", ">=") {
		t.Errorf("scan found %v, want a gt-to-ge candidate", got.rules())
	}
}

// guard returns the Guard of the first candidate matching rule and original,
// and whether one was found. It is how a fast test pins the rewrite site
// (Form S, C, or D and its declared types) discovery computed for a construct.
func (s scanned) guard(rule, original string) (Guard, bool) {
	for _, c := range s.candidates {
		if c.Rule.Name == rule && c.Original == original {
			return c.Guard, true
		}
	}
	return Guard{}, false
}

// count returns how many candidates match rule and original, which is how a
// fast test pins that a construct is a mutation site exactly once (or not at
// all).
func (s scanned) count(rule, original string) int {
	n := 0
	for _, c := range s.candidates {
		if c.Rule.Name == rule && c.Original == original {
			n++
		}
	}
	return n
}

// declSummary renders a Form D guard's declared types as "name:type" joined by
// spaces, in source order, which is how a fast test names what a `:=` or `var`
// site must declare.
func declSummary(g Guard) string {
	out := ""
	for i, d := range g.DeclTypes {
		if i > 0 {
			out += " "
		}
		out += d.Name + ":" + d.Type
	}
	return out
}

// branch returns the BranchProof of the first candidate matching rule and
// original, or nil. It is how a fast test pins whether the branch-proof phase
// discharged a decreasing edit's gated body.
func (s scanned) branch(rule, original string) *BranchProof {
	for _, c := range s.candidates {
		if c.Rule.Name == rule && c.Original == original {
			return c.Branch
		}
	}
	return nil
}
