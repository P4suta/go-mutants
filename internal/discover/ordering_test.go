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

// The orders a result promises, one tie-break at a time.
//
// A discovery is run twice over the same bytes — by a developer and by CI, by
// two shards of one run — and the two have to agree byte for byte, because the
// catalogue's digest is what an outcome cache is keyed on and a run report is
// what a reviewer diffs. The walk itself reaches nodes in a deterministic
// order, so a comparison that ignored one key would still *look* stable on
// every fixture in this repository while resting on the walk rather than on the
// order it claims. Each of these tests separates one key: two values that agree
// on every key before it and differ on that one.

// TestEveryKeyOfTheSkipSiteOrderDecidesSomething is [compareSkipSites], which
// is the order `list --explain` prints.
//
// The last two keys are the ones worth stating. Two rules can be declined at
// one coordinate for one reason each, so the reason is a key rather than a
// grouping; and three rules can be declined at one coordinate for one reason,
// which a named boolean condition produces every time — so the rule name is a
// key after it, and without it those three would come out in whatever order the
// walk reached them.
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

	// A larger line before a smaller one, with every earlier key equal: the
	// coordinates are compared as numbers, which is what keeps line 9 before
	// line 10 rather than after it.
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

// with copies a skip site and applies one edit, so that a table row names the
// one key it is about.
func with(base SkipSite, edit func(*SkipSite)) SkipSite {
	edit(&base)
	return base
}

// TestEveryKeyOfTheCandidateOrderDecidesSomething is the same for
// [discovery.sortedCandidates], where the third key is the one no lexical order
// could supply.
//
// Two rules proposing an edit over the same bytes are ordered by their position
// in the canonical registry rather than by their names, because the registry's
// order is the one `docs/operators.md` prints and the one a reader of a report
// is looking at. Sorting by name would put `add-to-sub` before `negate-condition`
// for no reason anybody could see.
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

	// Two rules over one span, given to the sorter in registry order reversed.
	// `negate-condition` precedes `and-to-or` in the catalogue, so the answer
	// is not the order they were handed over and not their alphabetical one
	// either — which is what makes this row decide the third key.
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
	// And the argument is not reordered: a caller still holding the slice it
	// handed over would otherwise find it rearranged underneath it.
	if d.candidates[0].Path != "pkg/b.go" {
		t.Errorf("sortedCandidates reordered its own input: %+v", d.candidates[0])
	}
}

// registryPosition is one rule's place in the canonical order.
func registryPosition(t *testing.T, registry *mutation.Registry, rule mutation.Rule) int {
	t.Helper()

	position, ok := registry.Position(rule.Name)
	if !ok {
		t.Fatalf("the canonical registry has no position for %q", rule.Name)
	}
	return position
}

// TestASkipOrderIsByPathAndThenByReason pins [discovery.sortedSkips], whose two
// keys are both lexical and whose aggregation is a map — so the order it comes
// out in is the one this comparison imposes and no other.
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

// TestATestVariantReportsTheImportPathAUserWouldType pins [packagePath].
//
// go/packages decorates the path of a package compiled for a test binary --
// `example.com/m/pkg [example.com/m/pkg.test]` -- and every place a candidate's
// package is printed, compared or grouped by wants the undecorated one. The
// decoration is also how a package and its test variant are told apart in the
// loader's own IDs, which is why it is stripped here and not there.
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
