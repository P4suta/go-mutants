// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
)

var wholeMainmod = sync.OnceValues(func() (Result, error) {
	located, err := gocmd.Locate(gocmd.Options{})
	if err != nil {
		return Result{}, err
	}
	root, err := fixturePath("mainmod")
	if err != nil {
		return Result{}, err
	}
	return Discover(context.Background(), Options{SnapshotRoot: root, Toolchain: located})
})

func wholeFixture(t *testing.T) Result {
	t.Helper()
	toolchain(t)
	result, err := wholeMainmod()
	if err != nil {
		t.Fatalf("Discover(mainmod): %v", err)
	}
	return result
}

const siteSnippetWidth = 6

func describeSite(t *testing.T, root string, site SkipSite) string {
	t.Helper()
	where := site.Path + ":" + strconv.Itoa(site.Line) + ":" + strconv.Itoa(site.Column) +
		" " + string(site.Reason)
	if site.Rule != "" {
		where += " " + site.Rule
	}
	if site.Line == 0 && site.Column == 0 {
		return where
	}
	source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(site.Path)))
	if err != nil {
		t.Fatalf("reading the fixture file %s names: %v", where, err)
	}
	lines := strings.Split(string(source), "\n")
	if site.Line < 1 || site.Line > len(lines) {
		return where + " <the file has " + strconv.Itoa(len(lines)) + " lines>"
	}
	line := strings.TrimSuffix(lines[site.Line-1], "\r")
	if site.Column < 1 || site.Column > len(line) {
		return where + " <the line is " + strconv.Itoa(len(line)) + " bytes>"
	}
	rest := line[site.Column-1:]
	if len(rest) > siteSnippetWidth {
		rest = rest[:siteSnippetWidth]
	}
	return where + " " + strconv.Quote(rest)
}

func describeSitesIn(t *testing.T, root string, sites []SkipSite, path string) []string {
	t.Helper()
	var out []string
	for _, site := range sites {
		if site.Path == path {
			out = append(out, describeSite(t, root, site))
		}
	}
	return out
}

func TestSuppressedSitesCarryTheirCoordinates(t *testing.T) {
	root := fixture(t, "mainmod")
	result := wholeFixture(t)

	for _, want := range []struct {
		path  string
		sites []string
	}{
		{
			path: "suppressed/suppressed.go",
			sites: []string{
				`suppressed/suppressed.go:18:12 const-decl true-to-false "true"`,
				`suppressed/suppressed.go:20:13 const-decl gt-to-ge "> 1"`,
				`suppressed/suppressed.go:27:18 const-decl le-to-lt "<= 4"`,
				`suppressed/suppressed.go:33:28 array-length lt-to-le "< 2, t"`,
				`suppressed/suppressed.go:33:33 array-length true-to-false "true})"`,
				`suppressed/suppressed.go:36:19 package-var-init lt-to-le "< 5"`,
				`suppressed/suppressed.go:39:15 package-var-init true-to-false "true"`,
				`suppressed/suppressed.go:50:33 package-var-init return-true "1 == 2"`,
				`suppressed/suppressed.go:50:35 package-var-init eq-to-neq "== 2 }"`,
				`suppressed/suppressed.go:58:18 const-decl gt-to-ge "> 2"`,
			},
		},
		{
			path: "generics/generics.go",
			sites: []string{
				`generics/generics.go:29:27 type-param true-to-false "true})"`,
				`generics/generics.go:36:28 type-param false-to-true "false}"`,
				`generics/generics.go:43:27 type-param true-to-false "true})"`,
				`generics/generics.go:60:25 type-param true-to-false "true})"`,
				`generics/generics.go:60:51 type-param false-to-true "false}"`,
			},
		},
		{
			path:  "negate/negate.go",
			sites: nil,
		},
		{
			path:  "unnameable/unnameable.go",
			sites: []string{`unnameable/unnameable.go:42:23 unnameable-decl-type add-to-sub "+ hidd"`},
		},
	} {
		equalStrings(t, describeSitesIn(t, root, result.SkipSites, want.path), want.sites)
	}
}

func TestEverySuppressedSitePointsIntoItsFile(t *testing.T) {
	root := fixture(t, "mainmod")
	result := wholeFixture(t)
	if len(result.SkipSites) == 0 {
		t.Fatal("the fixture module recorded no suppressed sites at all")
	}

	for _, site := range result.SkipSites {
		if site.Line == 0 {
			continue
		}
		source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(site.Path)))
		if err != nil {
			t.Fatalf("reading the fixture file a site names: %v", err)
		}
		lines := strings.Split(string(source), "\n")
		if site.Line > len(lines) {
			t.Errorf("%s has %d lines and a %s site is recorded on line %d",
				site.Path, len(lines), site.Reason, site.Line)
			continue
		}
		line := strings.TrimSuffix(lines[site.Line-1], "\r")
		if site.Column < 1 || site.Column > len(line) {
			t.Errorf("%s:%d is %d bytes long and a %s site is recorded at column %d",
				site.Path, site.Line, len(line), site.Reason, site.Column)
			continue
		}
		if c := line[site.Column-1]; c == ' ' || c == '\t' {
			t.Errorf("%s:%d:%d points at whitespace, so it does not name the %s site: %q",
				site.Path, site.Line, site.Column, site.Reason, line)
		}
	}
}

var linedModule = map[string]string{
	"go.mod": "module example.com/lined\n\ngo 1.26\n",
	"lined.go": "// SPDX-FileCopyrightText: 2026 go-mutants contributors\n" +
		"// SPDX-License-Identifier: MIT OR Apache-2.0\n" +
		"\n" +
		"// Package lined is generated-looking source with a line directive in it.\n" +
		"package lined\n" +
		"\n" +
		"//line fake.go:100\n" +
		"\n" +
		"const Enabled = true\n" +
		"\n" +
		"// Greater is the live candidate beside the suppressed constant.\n" +
		"func Greater(a, b int) bool {\n" +
		"	return a > b\n" +
		"}\n",
}

const (
	linedSkipLine      = 9
	linedCandidateLine = 13
)

func TestSkipSitesIgnoreLineDirectivesExactlyAsCandidatesDo(t *testing.T) {
	root := writeModule(t, linedModule)
	assertLineDirectiveMoves(t, filepath.Join(root, "lined.go"))

	result, err := Discover(context.Background(), Options{SnapshotRoot: root, Toolchain: toolchain(t)})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	equalStrings(t, summarizeSites(result.SkipSites),
		[]string{"lined.go:" + strconv.Itoa(linedSkipLine) + " const-decl"})
	got, ok := candidateOf(t, result.Candidates, "gt-to-ge")
	if !ok {
		t.Fatal("the line-directive fixture produced no gt-to-ge candidate")
	}
	if got.Line != linedCandidateLine {
		t.Errorf("the operator is reported on line %d, want the real file's %d",
			got.Line, linedCandidateLine)
	}
}

func assertLineDirectiveMoves(t *testing.T, path string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	var literal token.Pos
	ast.Inspect(file, func(node ast.Node) bool {
		if ident, isIdent := node.(*ast.Ident); isIdent && ident.Name == "true" {
			literal = ident.Pos()
			return false
		}
		return true
	})
	if !literal.IsValid() {
		t.Fatal("the fixture no longer holds the boolean literal this test is about")
	}
	tokFile := fset.File(literal)
	real, adjusted := tokFile.PositionFor(literal, false), tokFile.PositionFor(literal, true)
	if real.Line != linedSkipLine {
		t.Fatalf("the literal is on line %d of the fixture, want %d", real.Line, linedSkipLine)
	}
	if adjusted.Line == real.Line {
		t.Fatalf("the //line directive moves nothing (both readings say line %d), so this test proves nothing",
			real.Line)
	}
}

func summarizeSites(sites []SkipSite) []string {
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.Path+":"+strconv.Itoa(s.Line)+" "+string(s.Reason))
	}
	return out
}

func TestWholeFileSkipsCarryLineZero(t *testing.T) {
	result := discoverFixture(t, "mainmod", Options{
		Exclude: patterns(t, "legacy/**"),
	})

	wholeFile := map[SkipReason]bool{SkipGenerated: true, SkipCgo: true, SkipExcluded: true}
	seen := make(map[SkipReason]bool, len(wholeFile))
	for _, site := range result.SkipSites {
		if !wholeFile[site.Reason] {
			if site.Line == 0 || site.Column == 0 {
				t.Errorf("%s %s is recorded at %d:%d, want the coordinates of the suppressed expression",
					site.Path, site.Reason, site.Line, site.Column)
			}
			continue
		}
		seen[site.Reason] = true
		if site.Line != 0 || site.Column != 0 {
			t.Errorf("%s %s is recorded at %d:%d, want 0:0 for a file that was never opened",
				site.Path, site.Reason, site.Line, site.Column)
		}
	}
	for reason := range wholeFile {
		if !seen[reason] {
			t.Errorf("the fixture module recorded no %s site, so this test proved nothing about it", reason)
		}
	}
}

func TestSkipSitesSumToTheAggregateCounts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		discover func(*testing.T) Result
	}{
		{name: "everything", discover: wholeFixture},
		{name: "excluded", discover: func(t *testing.T) Result {
			return discoverFixture(t, "mainmod", Options{Exclude: patterns(t, "legacy/**", "suppressed/**")})
		}},
		{name: "included", discover: func(t *testing.T) Result {
			return discoverFixture(t, "mainmod", Options{Include: patterns(t, "generics/**")})
		}},
		{name: "one package", discover: func(t *testing.T) Result {
			return discoverFixture(t, "mainmod", Options{Packages: []string{"./suppressed"}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.discover(t)
			counts := make(map[skipKey]int, len(result.SkipSites))
			for _, site := range result.SkipSites {
				counts[skipKey{path: site.Path, reason: site.Reason}]++
			}
			summed := make([]string, 0, len(counts))
			for key, count := range counts {
				summed = append(summed, key.path+" "+string(key.reason)+" "+strconv.Itoa(count))
			}
			slices.Sort(summed)
			equalStrings(t, summed, summarizeSkips(result.Skips))
		})
	}
}

func TestSkipSitesAreOrderedByPathLineColumnAndReason(t *testing.T) {
	result := wholeFixture(t)
	for i := 1; i < len(result.SkipSites); i++ {
		if compareSkipSites(result.SkipSites[i-1], result.SkipSites[i]) > 0 {
			t.Fatalf("sites %d and %d are out of order: %+v then %+v",
				i-1, i, result.SkipSites[i-1], result.SkipSites[i])
		}
	}
}
