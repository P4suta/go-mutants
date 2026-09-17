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

const completionFixture = `package pkg

func Widest(a, b int) int {
	switch scaled(a) + scaled(b) {
	default:
		return a
	}
}
`

const completionSibling = `package pkg

import "time"

func scaled(n int) time.Duration { return time.Duration(n) }
`

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

func TestImportsOfReadsTheFilesInPositionOrderAndNotTheOrderItWasHanded(t *testing.T) {
	t.Parallel()

	parsed := parseAll(t, []string{
		"package pkg\n\nimport clock \"time\"\n",
		"package pkg\n\nimport chrono \"time\"\n",
	})
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
	ordinary := parse("widest.go", "package pkg\n\nimport \"time\"\n")
	itsTests := parse("widest_test.go", "package pkg\n\nimport \"testing\"\n")
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

	if got, ok := index["time"]; !ok || got != "" {
		t.Errorf("the index holds (%q, %v) for \"time\", want the ordinary file's unaliased import", got, ok)
	}
	for _, left := range []string{"testing", "unsafe"} {
		if got, ok := index[left]; ok {
			t.Errorf("the index holds %q for %q, want it left out", got, left)
		}
	}

	if second := d.packageImports(&loadResult{}, &packages.Package{PkgPath: "example.com/m/pkg"}); !maps.Equal(second, index) {
		t.Errorf("the second question answered %v, want the first answer %v", second, index)
	}
}

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
