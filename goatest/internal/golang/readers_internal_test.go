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

func firstValueSpec(t *testing.T, file *ast.File) ast.Node {
	t.Helper()
	for _, declaration := range file.Decls {
		if general, ok := declaration.(*ast.GenDecl); ok && general.Tok == token.VAR {
			return general.Specs[0]
		}
	}
	t.Fatal("the fixture declares no variable")
	return nil
}

func TestInitializerReferencesReadsOnlyWhatRunsBeforeAnyTest(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		source string
		want   []string
	}{
		{
			name:   "a call this package makes",
			source: "package subject\n\nvar value = compute(input)\n",
			want:   []string{"compute", "input"},
		},
		{
			name:   "a call inside a function literal that runs later",
			source: "package subject\n\nvar value = func() int { return compute() }\n",
		},
		{
			name:   "a value sync.OnceValue defers",
			source: "package subject\n\nimport \"sync\"\n\nvar value = sync.OnceValue(compute)\n",
			want:   []string{"sync"},
		},
		{
			name:   "a pair sync.OnceValues defers",
			source: "package subject\n\nimport \"sync\"\n\nvar value = sync.OnceValues(compute)\n",
			want:   []string{"sync"},
		},
		{
			name:   "a value another package's OnceValue computes now",
			source: "package subject\n\nimport \"other\"\n\nvar value = other.OnceValue(compute)\n",
			want:   []string{"compute", "other"},
		},
		{
			name:   "a value sync computes with another call",
			source: "package subject\n\nimport \"sync\"\n\nvar value = sync.Other(compute)\n",
			want:   []string{"compute", "sync"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := parseSubject(t, test.source)
			got := sortedKeys(initializerReferences(firstValueSpec(t, file), repositoryImports(file)))
			if !slices.Equal(got, test.want) {
				t.Fatalf("initializerReferences = %q, want %q", got, test.want)
			}
		})
	}
}

func TestInitializerDependencyReferencesNamesThePackagesThatRunBeforeAnyTest(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		source string
		want   []string
	}{
		{
			name:   "a call into another package",
			source: "package subject\n\nimport \"os\"\n\nvar value, _ = os.ReadFile(name)\n",
			want:   []string{"os"},
		},
		{
			name:   "a call inside a function literal that runs later",
			source: "package subject\n\nimport \"os\"\n\nvar value = func() { os.ReadFile(name) }\n",
		},
		{
			name: "a pair sync.OnceValues defers",
			source: "package subject\n\nimport (\n\t\"os\"\n\t\"sync\"\n)\n\n" +
				"var value = sync.OnceValues(func() ([]byte, error) { return os.ReadFile(name) })\n",
		},
		{
			name: "a call sync.OnceValue defers",
			source: "package subject\n\nimport (\n\t\"os\"\n\t\"sync\"\n)\n\n" +
				"var value = sync.OnceValue(func() []byte { data, _ := os.ReadFile(name); return data })\n",
		},
		{
			name: "a call another package's OnceValue makes now",
			source: "package subject\n\nimport (\n\t\"os\"\n\t\"other\"\n)\n\n" +
				"var value = other.OnceValue(os.DirFS(name))\n",
			want: []string{"os", "other"},
		},
		{
			name:   "a scope a dot import opens",
			source: "package subject\n\nimport . \"strings\"\n\nvar value = Title(name)\n",
			want:   []string{"strings"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := parseSubject(t, test.source)
			got := sortedKeys(initializerDependencyReferences(firstValueSpec(t, file),
				repositoryImports(file), repositoryDotImports(file)))
			if !slices.Equal(got, test.want) {
				t.Fatalf("initializerDependencyReferences = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMergeRepositoryReferencesKeepsWhatWasThereBefore(t *testing.T) {
	t.Parallel()
	graph := map[string]map[string]struct{}{}
	mergeRepositoryReferences(graph, "one", map[string]struct{}{"a": {}})
	mergeRepositoryReferences(graph, "one", map[string]struct{}{"b": {}})
	mergeRepositoryReferences(graph, "two", nil)
	if got := sortedKeys(graph["one"]); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("the graph names %q for one, want both references", got)
	}
	if graph["two"] == nil {
		t.Fatal("a name with no references is absent from the graph, want it present and empty")
	}
}

func TestRepositoryReaderSelectorsReadEveryImportShapeItCanSee(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		source  string
		opaque  bool
		callers []string
	}{
		{
			name:    "a reader imported plainly",
			source:  "package subject\n\nimport \"os\"\n",
			callers: []string{"os"},
		},
		{
			name:    "a reader imported under another name",
			source:  "package subject\n\nimport stdos \"os\"\n",
			callers: []string{"stdos"},
		},
		{
			name:    "a reader whose path ends in a segment",
			source:  "package subject\n\nimport \"path/filepath\"\n",
			callers: []string{"filepath"},
		},
		{
			name:    "two readers imported together",
			source:  "package subject\n\nimport (\n\t\"os\"\n\t\"path/filepath\"\n)\n",
			callers: []string{"filepath", "os"},
		},
		{
			name:   "a reader imported for its side effects alone",
			source: "package subject\n\nimport _ \"os\"\n",
		},
		{name: "a package this scan does not list", source: "package subject\n\nimport \"strings\"\n"},
		{name: "a reader whose scope a dot import opens", source: "package subject\n\nimport . \"os\"\n", opaque: true},
		{name: "a file the C toolchain reads", source: "package subject\n\nimport \"C\"\n", opaque: true},
		{name: "a file that imports nothing", source: "package subject\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			selectors, opaque := repositoryReaderSelectors(parseSubject(t, test.source))
			if opaque != test.opaque {
				t.Fatalf("repositoryReaderSelectors said opaque %t, want %t", opaque, test.opaque)
			}
			if opaque && selectors != nil {
				t.Fatalf("an opaque file still named %v, want no selectors at all", selectors)
			}
			if got := sortedKeys(selectorQualifiers(selectors)); !slices.Equal(got, test.callers) {
				t.Fatalf("repositoryReaderSelectors named %q, want %q", got, test.callers)
			}
			if len(test.callers) == 0 && selectors != nil {
				t.Errorf("a file naming no reader answered with %v, want no map at all", selectors)
			}
		})
	}
}

func selectorQualifiers(selectors map[string][]repositoryReaderCall) map[string]struct{} {
	names := make(map[string]struct{}, len(selectors))
	for name := range selectors {
		names[name] = struct{}{}
	}
	return names
}

func TestRepositoryReferencesWalkIntoASelectorTheCallInFrontOfItProduced(t *testing.T) {
	t.Parallel()
	file := parseSubject(t, `package subject

import "os"

func Subject() {
	_ = compute(inner).Field
	_ = compute(os.ReadFile(name)).Other
}
`)
	body := file.Decls[len(file.Decls)-1]
	got := sortedKeys(repositoryReferences(body, repositoryImports(file)))
	for _, want := range []string{"Field", "Other", "compute", "inner", "name"} {
		if !slices.Contains(got, want) {
			t.Errorf("repositoryReferences = %q, want it to name %q", got, want)
		}
	}
	dependencies := sortedKeys(repositoryDependencyReferences(body, repositoryImports(file), nil))
	if !slices.Equal(dependencies, []string{"os"}) {
		t.Fatalf("repositoryDependencyReferences = %q, want the package behind the nested call", dependencies)
	}
}

func TestAnalyzeRepositoryReadsKeepsEveryFunctionsAnswerAndFollowsWhatRunsFirst(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		source       string
		candidate    bool
		unobservable bool
	}{
		{
			name: "one observable reader beside one nothing can observe",
			source: `package subject

import "os"

func Seen() ([]byte, error) { return os.ReadFile(name) }

func Unseen() (string, error) { return os.Readlink(name) }
`,
			candidate: true, unobservable: true,
		},
		{
			name: "an initialization that reaches a reader through a helper",
			source: `package subject

import "os"

func init() { helper() }

func helper() (string, error) { return os.Readlink(name) }
`,
			candidate: true, unobservable: true,
		},
		{
			name: "an initialization that reaches nothing that reads",
			source: `package subject

func init() { helper() }

func helper() int { return 1 }
`,
		},
		{
			name: "an initialization that names something this package does not declare",
			source: `package subject

func init() { absent.Helper() }
`,
		},
		{
			name: "a value an initialization reads through",
			source: `package subject

import "os"

func init() { _ = listing }

var listing, _ = os.Readlink(name)
`,
			candidate: true, unobservable: true,
		},
		{
			name: "a constant declaration beside a reader",
			source: `package subject

import "os"

const name = "go.mod"

func Seen() ([]byte, error) { return os.ReadFile(name) }
`,
			candidate: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := parseSubject(t, test.source)
			scan := analyzeRepositoryReads([]repositoryReadFile{{file: file, production: true}}, false)
			if scan.candidate != test.candidate || scan.unobservable != test.unobservable {
				t.Fatalf("analyzeRepositoryReads = candidate %t, unobservable %t, want %t and %t",
					scan.candidate, scan.unobservable, test.candidate, test.unobservable)
			}
		})
	}
}
