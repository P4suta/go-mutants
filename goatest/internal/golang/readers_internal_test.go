// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

func parseSubject(t *testing.T, source string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "subject.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func sortedKeys(set map[string]struct{}) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestRepositoryImportsNamesEveryImportAUsableQualifierCanReach(t *testing.T) {
	t.Parallel()
	file := parseSubject(t, `package subject

import (
	"os"
	"path/filepath"
	renamed "io/fs"
	. "strings"
	_ "embed"
)
`)
	imports := repositoryImports(file)
	want := map[string]string{"os": "os", "filepath": "path/filepath", "renamed": "io/fs"}
	if len(imports) != len(want) {
		t.Fatalf("repositoryImports = %v, want %v", imports, want)
	}
	for name, path := range want {
		if imports[name] != path {
			t.Errorf("repositoryImports[%q] = %q, want %q", name, imports[name], path)
		}
	}
}

func TestRepositoryDotImportsNamesOnlyThePackagesThatOpenTheirScope(t *testing.T) {
	t.Parallel()
	file := parseSubject(t, `package subject

import (
	"os"
	. "strings"
	. "path/filepath"
	_ "embed"
	renamed "io/fs"
)
`)
	got := repositoryDotImports(file)
	want := []string{"strings", "path/filepath"}
	if !slices.Equal(got, want) {
		t.Fatalf("repositoryDotImports = %q, want %q", got, want)
	}
	if paths := repositoryDotImports(parseSubject(t, "package subject\n")); paths != nil {
		t.Errorf("a file that opens no scope named %q, want nothing at all", paths)
	}
}

func TestRepositoryReferencesNamesWhatIsNotAnImportQualifier(t *testing.T) {
	t.Parallel()
	file := parseSubject(t, `package subject

import "os"

func Subject(value receiver) {
	os.ReadFile(name)
	value.Method()
	helper()
}
`)
	references := repositoryReferences(file.Decls[len(file.Decls)-1], repositoryImports(file))
	got := sortedKeys(references)
	want := []string{"Method", "Subject", "helper", "name", "os", "receiver", "value"}
	if !slices.Equal(got, want) {
		t.Fatalf("repositoryReferences = %q, want %q", got, want)
	}
}

func TestRepositoryDependencyReferencesNamesEveryPackageAQualifierReaches(t *testing.T) {
	t.Parallel()
	file := parseSubject(t, `package subject

import (
	"os"
	renamed "io/fs"
	. "strings"
)

func Subject(value receiver) {
	os.ReadFile(name)
	renamed.Stat(nil, name)
	value.Method()
	local.Field.Method()
}
`)
	references := repositoryDependencyReferences(file.Decls[len(file.Decls)-1],
		repositoryImports(file), repositoryDotImports(file))
	got := sortedKeys(references)
	want := []string{"io/fs", "os", "strings"}
	if !slices.Equal(got, want) {
		t.Fatalf("repositoryDependencyReferences = %q, want %q", got, want)
	}
}

func TestInitializerCallsSeesACallAndNothingThatMerelyNamesOne(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		source string
		want   bool
	}{
		{name: "a value a call computes", source: "package subject\n\nvar value = compute()\n", want: true},
		{name: "a value a literal holds", source: "package subject\n\nvar value = 1\n", want: false},
		{name: "a value that names a function", source: "package subject\n\nvar value = compute\n", want: false},
		{
			name:   "a value a function literal computes later",
			source: "package subject\n\nvar value = func() int { return compute() }\n", want: false,
		},
		{name: "a declaration with no value at all", source: "package subject\n\nvar value int\n", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			declaration, ok := parseSubject(t, test.source).Decls[0].(*ast.GenDecl)
			if !ok {
				t.Fatalf("the fixture does not declare a value")
			}
			if got := initializerCalls(declaration.Specs[0]); got != test.want {
				t.Fatalf("initializerCalls(%q) = %t, want %t", test.source, got, test.want)
			}
		})
	}
}

func TestOpaqueReasonNamesCgoAheadOfAnyOtherOpenScope(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{name: "a file the C toolchain reads", source: "package subject\n\nimport \"C\"\n", want: reasonCgo},
		{
			name: "a file that opens another scope", source: "package subject\n\nimport . \"strings\"\n",
			want: reasonUnqualifiedScope,
		},
		{
			name:   "a file that does both",
			source: "package subject\n\nimport (\n\t. \"strings\"\n\t\"C\"\n)\n", want: reasonCgo,
		},
		{name: "a file that imports nothing", source: "package subject\n", want: reasonUnqualifiedScope},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := opaqueReason(parseSubject(t, test.source)); got != test.want {
				t.Fatalf("opaqueReason = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRepositoryReadReasonNamesTheCallWhereTheCallIsWhatMatters(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path string
		name string
		want string
	}{
		{path: "os", name: "Readlink", want: "os.Readlink"},
		{path: "path/filepath", name: "Walk", want: "path/filepath.Walk"},
		{path: "io/ioutil", name: "ReadFile", want: "io/ioutil.ReadFile"},
		{path: "golang.org/x/sys/unix", name: "Open", want: "golang.org/x/sys"},
		{path: "golang.org/x/sys/windows", name: "Open", want: "golang.org/x/sys"},
		{path: "io/fs", name: "ReadFile", want: "io/fs"},
		{path: "syscall", name: "Open", want: "syscall"},
	} {
		t.Run(test.path+"."+test.name, func(t *testing.T) {
			t.Parallel()
			if got := repositoryReadReason(test.path, test.name); got != test.want {
				t.Fatalf("repositoryReadReason(%q, %q) = %q, want %q", test.path, test.name, got, test.want)
			}
		})
	}
}

func TestSortedReasonsOrdersWhatItHasAndHoldsNothingWhenEmpty(t *testing.T) {
	t.Parallel()
	if got := sortedReasons(map[string]struct{}{"b": {}, "a": {}, "c": {}}); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("sortedReasons = %q, want them in order", got)
	}
	if got := sortedReasons(map[string]struct{}{}); got != nil {
		t.Fatalf("sortedReasons of nothing = %q, want no slice at all", got)
	}
}

func TestDependencyReasonsFallsBackToNamingTheDependencyItself(t *testing.T) {
	t.Parallel()
	stated := map[string]struct{}{"os.Readlink": {}}
	if got := dependencyReasons(repositoryReadScan{productionReasons: stated}); len(got) != 1 {
		t.Fatalf("dependencyReasons = %v, want the reasons the dependency stated", got)
	}
	got := dependencyReasons(repositoryReadScan{})
	if _, named := got[reasonDependency]; !named || len(got) != 1 {
		t.Fatalf("dependencyReasons of a scan that stated none = %v, want only %q", got, reasonDependency)
	}
}
