// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/P4suta/go-mutants/internal/mutation"
)

func TestEveryKeyOfTheSkipSiteOrderDecidesSomething(t *testing.T) {
	t.Parallel()

	base := SkipSite{Path: "pkg/a.go", Line: 10, Column: 4, Reason: SkipConstDecl, Rule: "eq-to-neq"}
	for _, c := range []struct {
		key    string
		larger SkipSite
	}{
		{key: "the path", larger: with(base, func(s *SkipSite) { s.Path = "pkg/b.go" })},
		{key: "the line", larger: with(base, func(s *SkipSite) { s.Line = 11 })},
		{key: "the column", larger: with(base, func(s *SkipSite) { s.Column = 5 })},
		{key: "the reason", larger: with(base, func(s *SkipSite) { s.Reason = SkipGenerated })},
		{key: "the rule", larger: with(base, func(s *SkipSite) { s.Rule = "or-to-and" })},
	} {
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()

			if got := compareSkipSites(base, c.larger); got >= 0 {
				t.Errorf("compareSkipSites ordered %+v at or after %+v (%d), want before it", base, c.larger, got)
			}
			if got := compareSkipSites(c.larger, base); got <= 0 {
				t.Errorf("the comparison is not antisymmetric on %s (%d)", c.key, got)
			}
		})
	}

	if got := compareSkipSites(base, base); got != 0 {
		t.Errorf("compareSkipSites of one site with itself = %d, want 0", got)
	}

	sites := []SkipSite{
		{Path: "pkg/a.go", Line: 10, Column: 1},
		{Path: "pkg/a.go", Line: 9, Column: 1},
		{Path: "pkg/a.go", Line: 9, Column: 12},
		{Path: "pkg/a.go", Line: 9, Column: 2},
	}
	slices.SortFunc(sites, compareSkipSites)
	var order []int
	for _, site := range sites {
		order = append(order, site.Line*100+site.Column)
	}
	if !slices.IsSorted(order) {
		t.Errorf("the sites came out as %v, want them in reading order", order)
	}
}

func with(base SkipSite, edit func(*SkipSite)) SkipSite {
	edit(&base)
	return base
}

func TestEveryKeyOfTheCandidateOrderDecidesSomething(t *testing.T) {
	t.Parallel()

	registry := mutation.CanonicalRegistry()
	rule := func(name string) mutation.Rule {
		t.Helper()
		found, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("the canonical registry holds no rule named %q", name)
		}
		return found
	}
	span := func(start, end uint32) mutation.Span {
		t.Helper()
		s, err := mutation.NewSpan(start, end)
		if err != nil {
			t.Fatalf("NewSpan(%d, %d): %v", start, end, err)
		}
		return s
	}

	early, late := rule("negate-condition"), rule("and-to-or")
	if registryPosition(t, registry, early) > registryPosition(t, registry, late) {
		early, late = late, early
	}

	d := &discovery{candidates: []Located{
		{Candidate: mutation.Candidate{Path: "pkg/b.go", Span: span(0, 4), Rule: early, Replacement: "a"}},
		{Candidate: mutation.Candidate{Path: "pkg/a.go", Span: span(8, 12), Rule: early, Replacement: "a"}},
		{Candidate: mutation.Candidate{Path: "pkg/a.go", Span: span(0, 4), Rule: late, Replacement: "a"}},
		{Candidate: mutation.Candidate{Path: "pkg/a.go", Span: span(0, 4), Rule: early, Replacement: "z"}},
		{Candidate: mutation.Candidate{Path: "pkg/a.go", Span: span(0, 4), Rule: early, Replacement: "a"}},
	}}
	var got []string
	for _, c := range d.sortedCandidates() {
		got = append(got, c.Path+" "+c.Rule.Name+" "+c.Replacement)
	}
	want := []string{
		"pkg/a.go " + early.Name + " a",
		"pkg/a.go " + early.Name + " z",
		"pkg/a.go " + late.Name + " a",
		"pkg/a.go " + early.Name + " a",
		"pkg/b.go " + early.Name + " a",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sortedCandidates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if d.candidates[0].Path != "pkg/b.go" {
		t.Errorf("sortedCandidates reordered its own input: %+v", d.candidates[0])
	}
}

func registryPosition(t *testing.T, registry *mutation.Registry, rule mutation.Rule) int {
	t.Helper()

	position, ok := registry.Position(rule.Name)
	if !ok {
		t.Fatalf("the canonical registry has no position for %q", rule.Name)
	}
	return position
}

func TestASkipOrderIsByPathAndThenByReason(t *testing.T) {
	t.Parallel()

	d := &discovery{skips: map[skipKey]int{
		{path: "pkg/b.go", reason: SkipGenerated}: 1,
		{path: "pkg/a.go", reason: SkipGenerated}: 2,
		{path: "pkg/a.go", reason: SkipCgo}:       3,
	}}
	var got []string
	for _, skip := range d.sortedSkips() {
		got = append(got, skip.Path+" "+string(skip.Reason)+" "+strings.Repeat("x", skip.Count))
	}
	want := []string{
		"pkg/a.go " + string(SkipCgo) + " xxx",
		"pkg/a.go " + string(SkipGenerated) + " xx",
		"pkg/b.go " + string(SkipGenerated) + " x",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sortedSkips =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestATestVariantReportsTheImportPathAUserWouldType(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ given, want string }{
		{"example.com/m/pkg", "example.com/m/pkg"},
		{"example.com/m/pkg [example.com/m/pkg.test]", "example.com/m/pkg"},
		{"example.com/m/pkg.test", "example.com/m/pkg.test"},
		{"example.com/m/pkg [example.com/m/other.test]", "example.com/m/pkg"},
		{"", ""},
	} {
		if got := packagePath(&packages.Package{PkgPath: c.given}); got != c.want {
			t.Errorf("packagePath(%q) = %q, want %q", c.given, got, c.want)
		}
	}
}
