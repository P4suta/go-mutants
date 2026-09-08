// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The coordinates half of the skip record. [Skip] says how much of a file was
// passed over and why; [SkipSite] says which of it, and these are the tests
// that hold the two together.
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

// wholeMainmod is the one discovery of testdata/mainmod with nothing selected.
//
// Four tests below ask exactly that question, and a loader pass over the whole
// fixture module is the most expensive thing in this package's suite: running
// it once took the package from twenty seconds back to thirteen. The result is
// read and never written, which is what makes sharing it safe.
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

// wholeFixture returns that shared pass.
//
// [toolchain] is called for its skip and not for its answer: a machine with no
// Go on it cannot load anything, and this has to report that the same way
// [discoverFixture] does rather than as a discovery failure.
func wholeFixture(t *testing.T) Result {
	t.Helper()
	toolchain(t)
	result, err := wholeMainmod()
	if err != nil {
		t.Fatalf("Discover(mainmod): %v", err)
	}
	return result
}

// siteSnippetWidth is how many bytes of the fixture a rendered site quotes.
//
// Enough to reach past the operator or the literal a coordinate points at, and
// short enough that a table of them stays one line each. It is a fixed width
// rather than the length of whatever was expected, so that the quoted text is a
// reading of the file and not a restatement of the expectation.
const siteSnippetWidth = 6

// describeSite renders one site as `path:line:col reason "text"`, with the text
// read out of the fixture at the coordinate the site names.
//
// Quoting the source is the whole point. A table of bare coordinates agrees
// with any consistent miscount — `Column+1` everywhere passes it, and passes a
// bounds check too, because a column one to the right of an operator is still a
// column on the line. The six bytes found there are what tie the number to the
// file: they change the moment the coordinate stops naming the expression.
//
// A coordinate that is out of range renders as a sentence rather than panicking
// or returning an error, so that a table comparison reports it as the row it is
// instead of ending the test somewhere else.
func describeSite(t *testing.T, root string, site SkipSite) string {
	t.Helper()
	where := site.Path + ":" + strconv.Itoa(site.Line) + ":" + strconv.Itoa(site.Column) +
		" " + string(site.Reason)
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

// describeSitesIn renders every site of one file.
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

// TestSuppressedSitesCarryTheirCoordinates is the whole point of the record: a
// user reading "four const-decl sites in this file" can now be told which four.
//
// The four packages named here are the ones whose coordinates can be read
// straight off the fixture with an eye, which is what makes the table an
// independent statement rather than a transcription of whatever the walk
// happened to produce. Each row carries the bytes the fixture holds at the
// coordinate, so the columns are pinned against the source rather than against
// this implementation's arithmetic. forms/forms.go contributes twelve more
// refusals and is left to TestSkipSitesSumToTheAggregateCounts, because its
// sites are the ones the guard forms decline rather than ones a reader can
// point at.
func TestSuppressedSitesCarryTheirCoordinates(t *testing.T) {
	root := fixture(t, "mainmod")
	result := wholeFixture(t)

	for _, want := range []struct {
		path  string
		sites []string
	}{
		{
			// Every suppressed context of the fixture package written for
			// them, in source order: the const block and the two lone const
			// declarations, both expressions hiding in one array length, the
			// three package-level initialisers — the last of which is a
			// function literal holding a comparison and a return, so it is
			// three sites on one line — and the four case labels.
			path: "suppressed/suppressed.go",
			sites: []string{
				`suppressed/suppressed.go:18:12 const-decl "true"`,
				`suppressed/suppressed.go:20:13 const-decl "> 1"`,
				`suppressed/suppressed.go:27:18 const-decl "<= 4"`,
				`suppressed/suppressed.go:33:28 array-length "< 2, t"`,
				`suppressed/suppressed.go:33:33 array-length "true})"`,
				`suppressed/suppressed.go:36:19 package-var-init "< 5"`,
				`suppressed/suppressed.go:39:15 package-var-init "true"`,
				`suppressed/suppressed.go:50:33 package-var-init "1 == 2"`,
				`suppressed/suppressed.go:50:33 package-var-init "1 == 2"`,
				`suppressed/suppressed.go:50:35 package-var-init "== 2 }"`,
				`suppressed/suppressed.go:58:18 const-decl "> 2"`,
				`suppressed/suppressed.go:68:9 case-label "== b:"`,
				`suppressed/suppressed.go:72:10 case-label "== fal"`,
				`suppressed/suppressed.go:72:13 case-label "false:"`,
				`suppressed/suppressed.go:89:16 case-label "< b):"`,
			},
		},
		{
			// The generic function's constraint, the single explicit type
			// argument, the generic type's constraint, and both members of the
			// type argument list.
			path: "generics/generics.go",
			sites: []string{
				`generics/generics.go:29:27 type-param "true})"`,
				`generics/generics.go:36:28 type-param "false}"`,
				`generics/generics.go:43:27 type-param "true})"`,
				`generics/generics.go:60:25 type-param "true})"`,
				`generics/generics.go:60:51 type-param "false}"`,
			},
		},
		{
			// The condition of a named boolean type, which is negatable Go and
			// no guard form's site.
			path:  "negate/negate.go",
			sites: []string{`negate/negate.go:48:5 unnameable-decl-type "f {"`},
		},
		{
			// The addition inside the call on the `:=` line, which is the edit
			// whose Form D site declares a type this file cannot spell. The
			// coordinate is the edit's and not the declaration's, which is
			// what makes it findable: the refusal is about the statement, and
			// the statement is where the reader has to look.
			path:  "unnameable/unnameable.go",
			sites: []string{`unnameable/unnameable.go:19:20 unnameable-decl-type "+ b)"`},
		},
	} {
		equalStrings(t, describeSitesIn(t, root, result.SkipSites, want.path), want.sites)
	}
}

// TestEverySuppressedSitePointsIntoItsFile is the check the table above cannot
// make for the sites nobody can read off the source: forms/forms.go's twelve
// refusals, which are the shapes the guard forms decline.
//
// It is deliberately weaker than the table and deliberately not nothing. A
// coordinate is only worth printing if it names a place that exists, and the
// place a suppressed edit sits at is the first byte of an expression or an
// operator — never whitespace, and never past the end of the line.
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

// linedModule is a module whose suppressed const and live candidate both sit
// under a `//line` directive claiming to be somewhere else entirely.
//
// The claimed line is far past the end of the real file, so an adjusted
// coordinate cannot be mistaken for a real one — and the directive is above
// both the const declaration and the function, so the skip site and the
// candidate are the two halves of one question.
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

// The coordinates of linedModule, counted off the fixture above: the constant's
// literal is on the ninth line of the real file and the comparison on the
// thirteenth. `//line fake.go:100` on line 7 renumbers everything under it, so
// the adjusted answers are 101 and 105 — numbers this file has no lines for.
const (
	linedSkipLine      = 9
	linedCandidateLine = 13
)

// TestSkipSitesIgnoreLineDirectivesExactlyAsCandidatesDo pins the one policy
// the coordinates have.
//
// A `//line` directive relocates a compiler diagnostic on purpose: generated
// code says where its own input was, so that a `go build` failure points at the
// template rather than at the output. A skip site is not a diagnostic. It is a
// place in the snapshot's own copy of the file, printed beside mutants whose
// coordinates are that file's, and `list --explain` output where the skips had
// been renumbered and the mutants had not would be two halves of one listing
// disagreeing about where they are.
//
// Both halves are asserted together for that reason, and the directive is
// proved live first: a test that only checked the real line numbers would pass
// just as well against a parser that had ignored the directive, which is the
// one way this could be green while saying nothing.
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

// assertLineDirectiveMoves proves the fixture's directive is one a Go parser
// acts on, by asking go/parser for both readings of the same position.
//
// Without this the test above would be satisfied by a toolchain that had
// stopped honouring `//line` at all — and by the mutation it exists to catch,
// since an adjusted position and an unadjusted one are the same number when
// nothing adjusts them.
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

// summarizeSites renders sites without their columns, for the cases where the
// line is the whole of what is being said.
func summarizeSites(sites []SkipSite) []string {
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.Path+":"+strconv.Itoa(s.Line)+" "+string(s.Reason))
	}
	return out
}

// TestWholeFileSkipsCarryLineZero pins the other half of the record.
//
// A generated file, a cgo package's file and an excluded file are never opened,
// so there is no site in them to point at and inventing one — line 1, say —
// would be a coordinate that reads as a fact. Zero is the answer, and
// `list --explain` prints such a row as the bare path.
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

// TestSkipSitesSumToTheAggregateCounts is the invariant the report rests on.
//
// [Result.Skips] is what the catalogue document and the run report carry, and
// this phase must not have changed it: the sites are a second view of the same
// events, so grouping them by file and reason has to reproduce the aggregate
// exactly — not approximately, and not with the whole-file rows left out.
//
// It runs over every selection the fixture module has a distinct answer for,
// because the two records are built at different call sites and a reason that
// is only reachable through one selection is exactly where they would drift.
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

// TestSkipSitesAreOrderedByPathLineColumnAndReason keeps the record diffable.
//
// Two discoveries over the same bytes produce identical results field for
// field, and a slice built by appending as a walk goes would be in whatever
// order the walk visited — which is stable today and is not a promise the
// package makes. The order is sorted for the same reason the candidates and the
// skips are: `list --explain` is output people diff between two runs.
func TestSkipSitesAreOrderedByPathLineColumnAndReason(t *testing.T) {
	result := wholeFixture(t)
	for i := 1; i < len(result.SkipSites); i++ {
		if compareSkipSites(result.SkipSites[i-1], result.SkipSites[i]) > 0 {
			t.Fatalf("sites %d and %d are out of order: %+v then %+v",
				i-1, i, result.SkipSites[i-1], result.SkipSites[i])
		}
	}
}

// TestCompareSkipSitesOrdersByEachKeyInTurn pins compareSkipSites directly, by
// the sign it returns for two sites that differ in exactly one key. The
// monotonic check elsewhere in this file re-uses the comparator to verify its
// own output and so cannot see a key drop out; naming the expected sign for
// each key does. Path orders first, then line, then column, then reason, and a
// site equals itself.
func TestCompareSkipSitesOrdersByEachKeyInTurn(t *testing.T) {
	t.Parallel()

	base := SkipSite{Path: "b/f.go", Line: 10, Column: 5, Reason: SkipConstDecl}
	cases := []struct {
		name string
		x, y SkipSite
		want int // -1 x before y, +1 x after y, 0 equal
	}{
		{"path decides first", SkipSite{Path: "a/f.go", Line: 99, Column: 99}, base, -1},
		{"then line", SkipSite{Path: "b/f.go", Line: 9, Column: 99}, base, -1},
		{"then column", SkipSite{Path: "b/f.go", Line: 10, Column: 4}, base, -1},
		{"then reason", SkipSite{Path: "b/f.go", Line: 10, Column: 5, Reason: SkipArrayLength}, base, -1},
		{"a site equals itself", base, base, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := compareSkipSites(tc.x, tc.y)
			if sign(got) != tc.want {
				t.Errorf("compareSkipSites = %d (sign %d), want sign %d", got, sign(got), tc.want)
			}
			if sign(compareSkipSites(tc.y, tc.x)) != -tc.want {
				t.Errorf("comparator is not antisymmetric for %s", tc.name)
			}
		})
	}
	// The "then reason" row leans on the reason being compared as a string:
	// "array-length" < "const-decl", which is the order compareSkipSites
	// promises and not the reasonRank order the suppression sort uses.
	if "array-length" >= "const-decl" {
		t.Fatal("this test assumes array-length sorts before const-decl as a string")
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}
