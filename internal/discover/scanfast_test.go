// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

type scanned struct {
	candidates []Located
	sites      []SkipSite
}

func scanSource(t *testing.T, src string) scanned {
	t.Helper()

	return scanWith(t, src, func(mutation.Rule) bool { return true })
}

func scanWith(t *testing.T, src string, want func(mutation.Rule) bool) scanned {
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

	var selected []mutation.Rule
	for _, rule := range SupportedRules() {
		if want(rule) {
			selected = append(selected, rule)
		}
	}
	matchers, err := newMatchers(selected)
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
	if err := d.scanParsed("pkg/scan.go", "example.com/m/pkg", []byte(src), tokFile, file, info, pkg, nil); err != nil {
		t.Fatalf("scanParsed:\n%s\n%v", src, err)
	}
	return scanned{candidates: d.candidates, sites: d.sites}
}

func scanPackage(t *testing.T, primary string, siblings ...string) scanned {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "scan.go", primary, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing the source:\n%s\n%v", primary, err)
	}
	checked := []*ast.File{file}
	for i, source := range siblings {
		name := "sibling" + strconv.Itoa(i) + ".go"
		parsed, parseErr := parser.ParseFile(fset, name, source, parser.ParseComments)
		if parseErr != nil {
			t.Fatalf("parsing %s:\n%s\n%v", name, source, parseErr)
		}
		checked = append(checked, parsed)
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
	pkg, _ := conf.Check("example.com/m/pkg", fset, checked, info)
	if len(typeErrs) > 0 {
		t.Fatalf("the fixture does not type-check (a scan fixture must):\n%s\n%v", primary, typeErrs)
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
	err = d.scanParsed("pkg/scan.go", "example.com/m/pkg", []byte(primary), tokFile, file, info, pkg,
		importsOf(checked))
	if err != nil {
		t.Fatalf("scanParsed:\n%s\n%v", primary, err)
	}
	return scanned{candidates: d.candidates, sites: d.sites}
}

func (s scanned) rules() []string {
	out := make([]string, 0, len(s.candidates))
	for _, c := range s.candidates {
		out = append(out, c.Rule.Name+" "+c.Original+"->"+c.Replacement)
	}
	return out
}

func (s scanned) has(rule, original, replacement string) bool {
	for _, c := range s.candidates {
		if c.Rule.Name == rule && c.Original == original && c.Replacement == replacement {
			return true
		}
	}
	return false
}

func (s scanned) skips() []string {
	out := make([]string, 0, len(s.sites))
	for _, site := range s.sites {
		out = append(out, string(site.Reason)+" "+
			strconv.Itoa(site.Line)+":"+strconv.Itoa(site.Column))
	}
	return out
}

func (s scanned) hasSkip(reason SkipReason) bool {
	for _, site := range s.sites {
		if site.Reason == reason {
			return true
		}
	}
	return false
}

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

func parseAll(t *testing.T, sources []string) []*ast.File {
	t.Helper()

	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(sources))
	for i, source := range sources {
		name := "file" + strconv.Itoa(i) + ".go"
		file, err := parser.ParseFile(fset, name, source, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s:\n%s\n%v", name, source, err)
		}
		files = append(files, file)
	}
	return files
}

func (s scanned) guard(rule, original string) (Guard, bool) {
	for _, c := range s.candidates {
		if c.Rule.Name == rule && c.Original == original {
			return c.Guard, true
		}
	}
	return Guard{}, false
}

func (s scanned) count(rule, original string) int {
	n := 0
	for _, c := range s.candidates {
		if c.Rule.Name == rule && c.Original == original {
			n++
		}
	}
	return n
}

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

func (s scanned) branch(rule, original string) *BranchProof {
	for _, c := range s.candidates {
		if c.Rule.Name == rule && c.Original == original {
			return c.Branch
		}
	}
	return nil
}
