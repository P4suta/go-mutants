// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// Import completion is the last thing standing between a guard that knows what
// it wants to write and one that can write it.
//
// Every form but Form C spells a type, and a type is spelled with the name its
// package has *in the file being rewritten*. A file can hold an expression of a
// type it has no name for — a helper in a sibling file returns one — and that
// was a refusal until a completion could supply the name. imports.go argues why
// a sibling's import and no wider set is the safe thing to draw on.

// completionFixture is the shape every test here is about: one file holding an
// expression whose type belongs to a package only its sibling imports.
//
// A `switch` tag is what closes every other escape, exactly as in package
// unnameable: there is no statement around it for Form S, Form D or Form F to
// stand in, and its type is not boolean, so Form C and Form C' have nothing to
// select. What is left is Form E, which has to write the type out.
const completionFixture = `package pkg

func Widest(a, b int) int {
	switch scaled(a) + scaled(b) {
	default:
		return a
	}
}
`

// completionSibling supplies the type and the import.
const completionSibling = `package pkg

import "time"

func scaled(n int) time.Duration { return time.Duration(n) }
`

// siteOf returns the guard of the one candidate of a rule, failing when the
// scan found none.
func siteOf(t *testing.T, got scanned, rule string) Guard {
	t.Helper()

	for _, candidate := range got.candidates {
		if candidate.Rule.Name == rule {
			return candidate.Guard
		}
	}
	t.Fatalf("the scan found %v, and none of them is %s", got.rules(), rule)
	return Guard{}
}

// TestASiblingsImportMakesATypeSpellable is the whole feature in one assertion
// pair: the site exists, and it says which import it needs to exist.
func TestASiblingsImportMakesATypeSpellable(t *testing.T) {
	t.Parallel()

	got := scanPackage(t, completionFixture, completionSibling)
	if got.hasSkip(SkipUnnameableDeclType) {
		t.Fatalf("the tag is still a refusal: %v", got.skips())
	}
	guard := siteOf(t, got, "add-to-sub")
	if guard.Form != GuardFormE {
		t.Errorf("form = %q, want %q", guard.Form, GuardFormE)
	}
	if guard.SiteType != "time.Duration" {
		t.Errorf("SiteType = %q, want it spelled against the completed import", guard.SiteType)
	}
	want := []Completion{{Path: "time", Local: "time"}}
	if !slices.Equal(guard.Imports, want) {
		t.Errorf("Imports = %+v, want %+v", guard.Imports, want)
	}
}

// TestAFileThatAlreadyImportsThePackageIsCompletedWithNothing keeps the
// completion list a statement about what is *missing*.
//
// A guard that declared an import the file already has would make the rewritten
// file import one package twice, which is a redeclaration rather than a
// redundancy. So the ordinary case — a file that can already spell the type —
// has to come back with an empty list rather than with the import it has.
func TestAFileThatAlreadyImportsThePackageIsCompletedWithNothing(t *testing.T) {
	t.Parallel()

	got := scanPackage(t, `package pkg

import "time"

func Widest(a, b int) time.Duration {
	switch scaled(a) + scaled(b) {
	default:
		return 0
	}
}
`, completionSibling)
	guard := siteOf(t, got, "add-to-sub")
	if guard.SiteType != "time.Duration" {
		t.Errorf("SiteType = %q, want the type this file could already spell", guard.SiteType)
	}
	if len(guard.Imports) != 0 {
		t.Errorf("Imports = %+v, and the file imports that package already", guard.Imports)
	}
}

// TestACompletionDodgesANameTheFileAlreadyBinds is the collision the preferred
// name can walk into.
//
// The name a completion would like is the package's own, and a file is entitled
// to a local variable of that name. Binding the import to it anyway would
// shadow the import for exactly the statements a guard sits in, and the failure
// — "time.Duration undefined (type int has no field Duration)" — would name the
// generated import rather than the collision. So the name is bumped, which is
// what internal/instrument already does for the runtime alias.
func TestACompletionDodgesANameTheFileAlreadyBinds(t *testing.T) {
	t.Parallel()

	got := scanPackage(t, `package pkg

func Widest(a, b int) int {
	time := a
	switch scaled(time) + scaled(b) {
	default:
		return time
	}
}
`, completionSibling)
	guard := siteOf(t, got, "add-to-sub")
	if guard.SiteType == "time.Duration" {
		t.Fatalf("SiteType = %q, which the local variable `time` shadows", guard.SiteType)
	}
	if guard.SiteType != "time2.Duration" {
		t.Errorf("SiteType = %q, want the bumped name", guard.SiteType)
	}
	want := []Completion{{Path: "time", Local: "time2"}}
	if !slices.Equal(guard.Imports, want) {
		t.Errorf("Imports = %+v, want %+v", guard.Imports, want)
	}
}

// TestABlankImportOfTheFilesOwnIsCompleted is the form that looks like an
// exception and is the rule.
//
// A blank import imports the package and binds nothing, so the file has the
// edge and no name for it — which is exactly the condition a completion exists
// for, and exactly why [guardResolver.indexImports] skips those two forms while
// [importsOf] does not.
func TestABlankImportOfTheFilesOwnIsCompleted(t *testing.T) {
	t.Parallel()

	got := scanPackage(t, `package pkg

import _ "time"

func Widest(a, b int) int {
	switch scaled(a) + scaled(b) {
	default:
		return a
	}
}
`, completionSibling)
	guard := siteOf(t, got, "add-to-sub")
	if guard.SiteType != "time.Duration" {
		t.Errorf("SiteType = %q, want the blank import completed to a usable name", guard.SiteType)
	}
	want := []Completion{{Path: "time", Local: "time"}}
	if !slices.Equal(guard.Imports, want) {
		t.Errorf("Imports = %+v, want %+v", guard.Imports, want)
	}
}

// TestASiblingsAliasIsThePreferredName keeps a package's own habits.
//
// A file that renames an import has a reason, and a completion that ignored it
// would put two names for one package in front of a reader of one package's
// source. The alias is only *preferred*: a name this file binds still bumps it.
func TestASiblingsAliasIsThePreferredName(t *testing.T) {
	t.Parallel()

	got := scanPackage(t, completionFixture, `package pkg

import clock "time"

func scaled(n int) clock.Duration { return clock.Duration(n) }
`)
	guard := siteOf(t, got, "add-to-sub")
	if guard.SiteType != "clock.Duration" {
		t.Errorf("SiteType = %q, want the sibling's own name for the package", guard.SiteType)
	}
}

// TestEveryRewriteOfOneFileAgreesAboutWhatAPackageIsCalled is what makes a
// completion a fact about the file rather than about the candidate.
//
// Two sites needing one package must name it once. Two names would be two
// imports of one path, and the second would not compile; and because the
// rewriter unions what the guards declare, one name arrived at twice is what
// that union has to be able to assume.
func TestEveryRewriteOfOneFileAgreesAboutWhatAPackageIsCalled(t *testing.T) {
	t.Parallel()

	got := scanPackage(t, `package pkg

func Widest(a, b int) int {
	switch scaled(a) + scaled(b) {
	default:
		return a
	}
}

func Narrowest(a, b int) int {
	switch scaled(a) * scaled(b) {
	default:
		return b
	}
}
`, completionSibling)
	first := siteOf(t, got, "add-to-sub")
	second := siteOf(t, got, "mul-to-div")
	if first.SiteType != second.SiteType {
		t.Errorf("one file spells the package two ways: %q and %q", first.SiteType, second.SiteType)
	}
	if !slices.Equal(first.Imports, second.Imports) {
		t.Errorf("one file asks for two imports of one package: %+v and %+v", first.Imports, second.Imports)
	}
}

// TestImportsOfPrefersAnExplicitAliasWhateverTheFileOrder pins the index's own
// rule, which the tests above can only see through a spelling.
func TestImportsOfPrefersAnExplicitAliasWhateverTheFileOrder(t *testing.T) {
	t.Parallel()

	for _, order := range []struct {
		name  string
		files []string
	}{
		{"the plain import first", []string{
			"package pkg\n\nimport \"time\"\n",
			"package pkg\n\nimport clock \"time\"\n",
		}},
		{"the alias first", []string{
			"package pkg\n\nimport clock \"time\"\n",
			"package pkg\n\nimport \"time\"\n",
		}},
	} {
		t.Run(order.name, func(t *testing.T) {
			t.Parallel()

			index := importsOf(parseAll(t, order.files))
			if got := index["time"]; got != "clock" {
				t.Errorf("importsOf = %q for \"time\", want the explicit alias %q", got, "clock")
			}
		})
	}
}

// TestImportsOfReadsTheFilesInSourceOrder pins the tie-break that decides which
// of two equally good names a package is completed with.
//
// Two files that both import a path plainly leave the index with one entry and
// no name, which the caller resolves. Two that both alias it leave one alias,
// and *which* one has to be a function of the source rather than of a map
// iteration: a completion that changed between two runs over one tree would
// make the instrumented bytes a function of nothing anybody wrote.
func TestImportsOfReadsTheFilesInSourceOrder(t *testing.T) {
	t.Parallel()

	for _, order := range []struct {
		name  string
		files []string
		want  string
	}{
		{name: "the first file's alias", want: "early", files: []string{
			"package pkg\n\nimport early \"time\"\n",
			"package pkg\n\nimport late \"time\"\n",
		}},
		{name: "the other way round", want: "late", files: []string{
			"package pkg\n\nimport late \"time\"\n",
			"package pkg\n\nimport early \"time\"\n",
		}},
	} {
		t.Run(order.name, func(t *testing.T) {
			t.Parallel()

			index := importsOf(parseAll(t, order.files))
			if got := index["time"]; got != order.want {
				t.Errorf("importsOf = %q for \"time\", want the first file's %q", got, order.want)
			}
		})
	}
}

// TestImportsOfSkipsWhatBindsNoNameAndWhatIsNotAPath covers the specs an index
// has nothing to learn from.
//
// A blank or dot import contributes the path with no name, which is exactly
// what makes it completable: the package is an edge this one genuinely has and
// genuinely cannot spell. A path that will not unquote, or an empty one, is not
// an edge at all.
func TestImportsOfSkipsWhatBindsNoNameAndWhatIsNotAPath(t *testing.T) {
	t.Parallel()

	index := importsOf(parseAll(t, []string{
		"package pkg\n\nimport (\n\t_ \"time\"\n\t. \"strings\"\n\tfp \"path/filepath\"\n)\n",
	}))
	for path, want := range map[string]string{
		"time":          "",
		"strings":       "",
		"path/filepath": "fp",
	} {
		got, ok := index[path]
		if !ok {
			t.Errorf("importsOf left out %q", path)
			continue
		}
		if got != want {
			t.Errorf("importsOf = %q for %q, want %q", got, path, want)
		}
	}
	if len(index) != 3 {
		t.Errorf("importsOf found %v, want exactly the three paths", index)
	}

	// And the two an import spec can hold that are not paths. Neither is
	// reachable from a file go/parser produced -- a parsed import path is a
	// string literal and a valid one -- so they are built by hand, which is the
	// only way to ask whether the reader would carry into the index a name no
	// rewrite could use.
	unreadable := &ast.File{
		Name: ast.NewIdent("pkg"),
		Imports: []*ast.ImportSpec{
			{Path: &ast.BasicLit{Kind: token.STRING, Value: `"time"`}},
			{Path: &ast.BasicLit{Kind: token.STRING, Value: "not a quoted string"}},
			{Path: &ast.BasicLit{Kind: token.STRING, Value: `""`}},
			{Path: nil},
		},
	}
	if got := importsOf([]*ast.File{unreadable}); !maps.Equal(got, map[string]string{"time": ""}) {
		t.Errorf("importsOf = %v, want only the one path that is one", got)
	}
}

// TestImportsOfReadsTheFilesInPositionOrderAndNotTheOrderItWasHanded is the
// sort, which is the whole of what makes "the first file's alias wins" mean
// anything.
//
// go/packages hands a package's syntax trees over in an order it does not
// promise, and the answer this index gives has to be the same whichever order
// that was: a package whose files disagree about what to call an import must
// not have the disagreement settled by the loader. So the files are ordered by
// where they start in the file set, and the only way to watch that happen is to
// hand them over in the other order.
func TestImportsOfReadsTheFilesInPositionOrderAndNotTheOrderItWasHanded(t *testing.T) {
	t.Parallel()

	parsed := parseAll(t, []string{
		"package pkg\n\nimport clock \"time\"\n",
		"package pkg\n\nimport chrono \"time\"\n",
	})
	// The same two files, handed over backwards. Their positions are unchanged,
	// so a reader that sorts answers the same way and one that does not answers
	// with the second file's alias.
	backwards := []*ast.File{parsed[1], parsed[0]}

	for _, order := range []struct {
		name  string
		files []*ast.File
	}{
		{name: "in the order they were parsed", files: parsed},
		{name: "backwards", files: backwards},
	} {
		t.Run(order.name, func(t *testing.T) {
			t.Parallel()

			if got := importsOf(order.files)["time"]; got != "clock" {
				t.Errorf("importsOf = %q, want the first file's alias %q", got, "clock")
			}
		})
	}
}

// TestAPackagesTestFilesAndItsUnplaceableOnesAreLeftOutOfTheIndex is the filter
// [packageImports] applies before it hands anything to [importsOf], and both
// halves of it matter for the same reason: the index is what a *non-test* file
// is completed from.
//
// A test file's imports are not the package's — `testing` is in every `_test.go`
// and in none of the files this index is ever used to rewrite — and a syntax
// tree the file set cannot place is one nothing is known about, name included.
// The second half is a guard against a shape the loader has never produced, so
// it is stated here rather than assumed: a file at [token.NoPos] is what an
// unplaceable tree looks like, and the answer has to be to drop it rather than
// to ask a nil file what it is called.
func TestAPackagesTestFilesAndItsUnplaceableOnesAreLeftOutOfTheIndex(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	parse := func(name, source string) *ast.File {
		t.Helper()
		file, err := parser.ParseFile(fset, name, source, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		return file
	}
	// Placed in the file set, and named the two ways the filter tells apart.
	ordinary := parse("widest.go", "package pkg\n\nimport \"time\"\n")
	itsTests := parse("widest_test.go", "package pkg\n\nimport \"testing\"\n")
	// Not placed in it at all: a tree whose Package token is token.NoPos, which
	// is the zero value and belongs to no file in any file set.
	unplaceable := &ast.File{
		Name: ast.NewIdent("pkg"),
		Imports: []*ast.ImportSpec{
			{Path: &ast.BasicLit{Kind: token.STRING, Value: `"unsafe"`}},
		},
	}

	d := &discovery{}
	index := d.packageImports(
		&loadResult{fset: fset},
		&packages.Package{PkgPath: "example.com/m/pkg", Syntax: []*ast.File{ordinary, itsTests, unplaceable}},
	)

	// An unaliased import contributes the path with no name; see [defaultLocal]
	// for who resolves it and to what.
	if got, ok := index["time"]; !ok || got != "" {
		t.Errorf("the index holds (%q, %v) for \"time\", want the ordinary file's unaliased import", got, ok)
	}
	for _, left := range []string{"testing", "unsafe"} {
		if got, ok := index[left]; ok {
			t.Errorf("the index holds %q for %q, want it left out", got, left)
		}
	}

	// And the answer is remembered under the package's own path, which is what
	// keeps a package with many mutated files from being read many times.
	if second := d.packageImports(&loadResult{}, &packages.Package{PkgPath: "example.com/m/pkg"}); !maps.Equal(second, index) {
		t.Errorf("the second question answered %v, want the first answer %v", second, index)
	}
}

// TestMergeCompletionsIsAFunctionOfWhatTheRewriteNeeds pins both halves of
// [MergeCompletions]: the deduplication and the order.
//
// A guard's completions are a list of imports to splice into one file, so a
// path named twice would be an import declared twice -- a compile error in a
// generated tree rather than a redundancy. And the order has to be a function
// of the set rather than of the order the type checker happened to ask about
// packages, because two runs over one workspace have to produce identical
// bytes. Each key is separated by a pair that agrees on every key before it.
func TestMergeCompletionsIsAFunctionOfWhatTheRewriteNeeds(t *testing.T) {
	t.Parallel()

	render := func(list []Completion) string {
		var parts []string
		for _, one := range list {
			parts = append(parts, one.Local+"="+one.Path)
		}
		return strings.Join(parts, " ")
	}

	t.Run("the path orders first", func(t *testing.T) {
		t.Parallel()

		got := MergeCompletions(
			[]Completion{{Path: "example.com/z", Local: "a"}},
			[]Completion{{Path: "example.com/a", Local: "z"}},
		)
		if want := "z=example.com/a a=example.com/z"; render(got) != want {
			t.Errorf("MergeCompletions = %q, want %q", render(got), want)
		}
	})

	t.Run("the local name orders within one path", func(t *testing.T) {
		t.Parallel()

		// One path under two names is a real shape: a file that already binds
		// `time` gets `time2`, and a second guard in the same file may have
		// chosen it before this one did.
		got := MergeCompletions(
			[]Completion{{Path: "example.com/a", Local: "z"}},
			[]Completion{{Path: "example.com/a", Local: "a"}},
		)
		if want := "a=example.com/a z=example.com/a"; render(got) != want {
			t.Errorf("MergeCompletions = %q, want %q", render(got), want)
		}
	})

	t.Run("a completion already present is not added twice", func(t *testing.T) {
		t.Parallel()

		one := Completion{Path: "example.com/a", Local: "a"}
		got := MergeCompletions([]Completion{one}, []Completion{one, one})
		if want := "a=example.com/a"; render(got) != want {
			t.Errorf("MergeCompletions = %q, want %q", render(got), want)
		}
	})

	t.Run("a path under a second name is not a duplicate", func(t *testing.T) {
		t.Parallel()

		got := MergeCompletions(
			[]Completion{{Path: "example.com/a", Local: "a"}},
			[]Completion{{Path: "example.com/a", Local: "b"}},
		)
		if want := "a=example.com/a b=example.com/a"; render(got) != want {
			t.Errorf("MergeCompletions = %q, want %q", render(got), want)
		}
	})

	t.Run("merging nothing into nothing is nothing", func(t *testing.T) {
		t.Parallel()

		if got := MergeCompletions(nil, nil); len(got) != 0 {
			t.Errorf("MergeCompletions(nil, nil) = %v, want nothing", got)
		}
	})
}

// TestTheNameAnUnaliasedImportBindsIsTheLastElementOfItsPath pins
// [defaultLocal], the fallback for a completion whose package the checker
// cannot be asked about.
//
// It is a fallback and not the rule: the last element of a path and the name a
// package declares differ often enough to matter -- `gopkg.in/yaml.v3` declares
// `yaml`, `google.golang.org/grpc` declares `grpc` -- and only the declared name
// compiles. What this answers is the case where there is no package object to
// ask, and the answer has to be the path's own last element rather than the
// whole path, which would not be an identifier at all.
func TestTheNameAnUnaliasedImportBindsIsTheLastElementOfItsPath(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ path, want string }{
		{"time", "time"},
		{"path/filepath", "filepath"},
		{"example.com/m/internal/carrier", "carrier"},
		{"gopkg.in/yaml.v3", "yaml.v3"},
		{"", "."},
	} {
		if got := defaultLocal(c.path); got != c.want {
			t.Errorf("defaultLocal(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
