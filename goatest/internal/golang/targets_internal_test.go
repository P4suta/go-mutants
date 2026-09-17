// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package golang

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"testing"
)

func TestAliasesRecognizesSupportedImportsOnly(t *testing.T) {
	file := &ast.File{Imports: []*ast.ImportSpec{
		{Path: stringLiteral("testing")},
		{Name: ast.NewIdent("stdtest"), Path: stringLiteral("testing")},
		{Name: ast.NewIdent("."), Path: stringLiteral("testing")},
		{Name: ast.NewIdent("_"), Path: stringLiteral("testing")},
		{Path: stringLiteral("github.com/P4suta/go-mutants/goatest")},
		{Name: ast.NewIdent("gt"), Path: stringLiteral("github.com/P4suta/go-mutants/goatest")},
		{Path: stringLiteral("fmt")},
		{Path: &ast.BasicLit{Kind: token.STRING, Value: "not-quoted"}},
	}}

	testingAliases, goatestAliases := aliases(file)
	if want := map[string]bool{"testing": true, "stdtest": true}; !reflect.DeepEqual(testingAliases, want) {
		t.Fatalf("testing aliases = %v, want %v", testingAliases, want)
	}
	if want := map[string]bool{"goatest": true, "gt": true}; !reflect.DeepEqual(goatestAliases, want) {
		t.Fatalf("goatest aliases = %v, want %v", goatestAliases, want)
	}
}

func TestTargetKindMatchesGoTestNamingRules(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		kind TargetKind
		ok   bool
	}{
		{name: "Test", kind: KindTest, ok: true},
		{name: "TestX", kind: KindTest, ok: true},
		{name: "Test_underscore", kind: KindTest, ok: true},
		{name: "TestÉ", kind: KindTest, ok: true},
		{name: "Fuzz", kind: KindFuzz, ok: true},
		{name: "FuzzRoundTrip", kind: KindFuzz, ok: true},
		{name: "Testlower"},
		{name: "Fuzzlower"},
		{name: "Testing"},
		{name: "BenchmarkX"},
		{name: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			kind, ok := targetKind(test.name)
			if kind != test.kind || ok != test.ok {
				t.Fatalf("targetKind(%q) = (%q, %t), want (%q, %t)", test.name, kind, ok, test.kind, test.ok)
			}
		})
	}
}

func TestTestingSignatureRequiresExactlyOneCorrectParameterAndNoResults(t *testing.T) {
	t.Parallel()
	file := parseTargetSource(t, `package sample
import tst "testing"
func TestGood(t *tst.T) {}
func FuzzGood(f *tst.F) {}
func TestUnnamed(*tst.T) {}
func TestNoParams() {}
func TestTwoFields(a *tst.T, b *tst.T) {}
func TestGrouped(a, b *tst.T) {}
func TestResult(t *tst.T) int { return 0 }
func TestValue(t tst.T) {}
func TestIdent(t *T) {}
func TestOther(t *other.T) {}
func TestWrong(t *tst.F) {}
func FuzzWrong(f *tst.T) {}
func TestEmptyResults(t *tst.T) () {}
`)
	aliases := map[string]bool{"tst": true}
	for _, test := range []struct {
		name string
		kind TargetKind
		want bool
	}{
		{name: "TestGood", kind: KindTest, want: true},
		{name: "FuzzGood", kind: KindFuzz, want: true},
		{name: "TestUnnamed", kind: KindTest, want: true},
		{name: "TestEmptyResults", kind: KindTest, want: true},
		{name: "TestNoParams", kind: KindTest},
		{name: "TestTwoFields", kind: KindTest},
		{name: "TestGrouped", kind: KindTest},
		{name: "TestResult", kind: KindTest},
		{name: "TestValue", kind: KindTest},
		{name: "TestIdent", kind: KindTest},
		{name: "TestOther", kind: KindTest},
		{name: "TestWrong", kind: KindTest},
		{name: "FuzzWrong", kind: KindFuzz},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := testingSignature(findTargetFunction(t, file, test.name), aliases, test.kind)
			if got != test.want {
				t.Fatalf("testingSignature(%s) = %t, want %t", test.name, got, test.want)
			}
		})
	}
	if testingSignature(&ast.FuncDecl{Type: &ast.FuncType{}}, aliases, KindTest) {
		t.Fatal("nil parameter list was accepted")
	}
	nestedSelector := &ast.FuncDecl{Type: &ast.FuncType{Params: &ast.FieldList{List: []*ast.Field{{
		Type: &ast.StarExpr{X: &ast.SelectorExpr{X: selector("outer", "other"), Sel: ast.NewIdent("T")}},
	}}}}}
	if testingSignature(nestedSelector, aliases, KindTest) {
		t.Fatal("nested selector parameter was accepted")
	}
}

func TestTargetCapabilitiesRecognizesOnlyWellFormedIntegrationScopes(t *testing.T) {
	t.Parallel()
	aliases := map[string]bool{"gt": true}
	for _, test := range []struct {
		name string
		body string
		want []string
	}{
		{name: "run", body: `gt.Run(t, gt.Integration("postgres"), callback)`, want: []string{"postgres"}},
		{name: "removed check API", body: `gt.Check(f, gt.Integration("redis"), callback)`},
		{name: "multiple", body: `gt.Run(t, gt.Integration("first"), callback); gt.Run(t, gt.Integration("second"), callback)`, want: []string{"first", "second"}},
		{name: "nested", body: `wrap(gt.Run(t, gt.Integration("nested"), callback))`, want: []string{"nested"}},
		{name: "nested in invalid scope", body: `gt.Run(t, wrap(gt.Run(t, gt.Integration("nested-scope"), callback)), callback)`, want: []string{"nested-scope"}},
		{name: "nested in nonliteral", body: `gt.Run(t, gt.Integration(gt.Run(t, gt.Integration("nested-literal"), callback)), callback)`, want: []string{"nested-literal"}},
		{name: "wrong alias", body: `other.Run(t, gt.Integration("db"), callback)`},
		{name: "wrong call", body: `gt.Other(t, gt.Integration("db"), callback)`},
		{name: "not selector", body: `Run(t, gt.Integration("db"), callback)`},
		{name: "too few call arguments", body: `gt.Run(t, gt.Integration("db"))`},
		{name: "too many call arguments", body: `gt.Run(t, gt.Integration("db"), callback, extra)`},
		{name: "scope is not call", body: `gt.Run(t, scope, callback)`},
		{name: "wrong scope", body: `gt.Run(t, gt.Unit(), callback)`},
		{name: "scope has no argument", body: `gt.Run(t, gt.Integration(), callback)`},
		{name: "scope has two arguments", body: `gt.Run(t, gt.Integration("a", "b"), callback)`, want: []string{"a", "b"}},
		{name: "scope argument is identifier", body: `gt.Run(t, gt.Integration(name), callback)`},
		{name: "scope argument is rune", body: `gt.Run(t, gt.Integration('x'), callback)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := parseTargetSource(t, "package sample\nfunc target() { "+test.body+" }\n")
			got := targetCapabilities(findTargetFunction(t, file, "target"), aliases)
			if !slices.Equal(got, test.want) {
				t.Fatalf("capabilities = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTargetCapabilitiesIgnoresMalformedStringLiteral(t *testing.T) {
	call := &ast.CallExpr{
		Fun: selector("gt", "Run"),
		Args: []ast.Expr{
			ast.NewIdent("t"),
			&ast.CallExpr{Fun: selector("gt", "Integration"), Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: `"unterminated`}}},
			ast.NewIdent("callback"),
		},
	}
	function := &ast.FuncDecl{Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: call}}}}
	if got := targetCapabilities(function, map[string]bool{"gt": true}); len(got) != 0 {
		t.Fatalf("capabilities = %q", got)
	}
}

func TestSelectorIsRequiresSelectorAliasAndAllowedName(t *testing.T) {
	t.Parallel()
	aliases := map[string]bool{"gt": true}
	for _, test := range []struct {
		name string
		expr ast.Expr
		want bool
	}{
		{name: "match", expr: selector("gt", "Run"), want: true},
		{name: "second name", expr: selector("gt", "Check"), want: true},
		{name: "wrong alias", expr: selector("other", "Run")},
		{name: "wrong name", expr: selector("gt", "Other")},
		{name: "not selector", expr: ast.NewIdent("Run")},
		{name: "selector base not identifier", expr: &ast.SelectorExpr{X: selector("outer", "gt"), Sel: ast.NewIdent("Run")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := selectorIs(test.expr, aliases, "Run", "Check"); got != test.want {
				t.Fatalf("selectorIs = %t, want %t", got, test.want)
			}
		})
	}
}

func parseTargetSource(t *testing.T, source string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "sample_test.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func parseCommentedTargetSource(t *testing.T, source string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "sample_test.go", source, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func findTargetFunction(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == name {
			return function
		}
	}
	t.Fatalf("function %s not found", name)
	return nil
}

func stringLiteral(value string) *ast.BasicLit {
	return &ast.BasicLit{Kind: token.STRING, Value: `"` + value + `"`}
}

func selector(pkg, name string) *ast.SelectorExpr {
	return &ast.SelectorExpr{X: ast.NewIdent(pkg), Sel: ast.NewIdent(name)}
}

func TestTargetCapabilitiesReadsTheResourcesADocCommentDeclares(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		source string
		want   []string
	}{
		{
			name:   "a comment that names two resources",
			source: "// goatest:resources postgres redis\nfunc target() {}\n",
			want:   []string{"postgres", "redis"},
		},
		{
			name:   "a comment that names none",
			source: "// goatest:resources\nfunc target() {}\n",
		},
		{
			name:   "a comment that names only whitespace",
			source: "// goatest:resources   \nfunc target() {}\n",
		},
		{
			name:   "another comment entirely",
			source: "// target does something\nfunc target() {}\n",
		},
		{
			name:   "a comment beside a call that names the same resource",
			source: "// goatest:resources postgres\nfunc target() { gt.Run(t, gt.Integration(\"postgres\"), callback) }\n",
			want:   []string{"postgres"},
		},
		{
			name:   "a comment and a call that name different resources",
			source: "// goatest:resources redis\nfunc target() { gt.Run(t, gt.Integration(\"postgres\"), callback) }\n",
			want:   []string{"postgres", "redis"},
		},
		{
			name:   "a call that names one resource twice",
			source: "func target() { gt.Run(t, gt.Integration(\"postgres\", \"postgres\"), callback) }\n",
			want:   []string{"postgres"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := parseCommentedTargetSource(t, "package sample\n\n"+test.source)
			got := targetCapabilities(findTargetFunction(t, file, "target"), map[string]bool{"gt": true})
			if !slices.Equal(got, test.want) {
				t.Fatalf("capabilities = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAnIntegrationScopeNamesOnlyResourcesItCanRead(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		body   string
		want   []string
		scoped bool
	}{
		{
			name: "a scope of one resource", body: `gt.Run(t, gt.Integration("postgres"), callback)`,
			want: []string{"postgres"}, scoped: true,
		},
		{name: "a scope naming an empty resource", body: `gt.Run(t, gt.Integration(""), callback)`},
		{name: "a scope naming only whitespace", body: `gt.Run(t, gt.Integration("  "), callback)`},
		{
			name: "a scope whose second resource is not a string",
			body: `gt.Run(t, gt.Integration("postgres", name), callback)`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := parseTargetSource(t, "package sample\nfunc target() { "+test.body+" }\n")
			function := findTargetFunction(t, file, "target")
			statement, ok := function.Body.List[0].(*ast.ExprStmt)
			if !ok {
				t.Fatal("the fixture body does not hold a call")
			}
			values, scoped := integrationCapabilities(statement.X, map[string]bool{"gt": true})
			if scoped != test.scoped || !slices.Equal(values, test.want) {
				t.Fatalf("integrationCapabilities = (%q, %t), want (%q, %t)",
					values, scoped, test.want, test.scoped)
			}
			if !scoped && values != nil {
				t.Errorf("a scope it refused answered with %q, want nothing at all", values)
			}
		})
	}
}

func TestAnExampleIsATargetOnlyWhenItsBodyStatesItsOutput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		source string
		want   bool
	}{
		{name: "an output comment", source: "func ExampleOne() {\n\t// Output: one\n}\n", want: true},
		{
			name:   "an unordered output comment",
			source: "func ExampleOne() {\n\t// Unordered output: one\n}\n", want: true,
		},
		{name: "the same words in capitals", source: "func ExampleOne() {\n\t// OUTPUT: one\n}\n", want: true},
		{name: "no comment at all", source: "func ExampleOne() {\n}\n"},
		{name: "another comment entirely", source: "func ExampleOne() {\n\t// nothing to say\n}\n"},
		{
			name:   "an output comment before the body",
			source: "// Output: one\nfunc ExampleOne() {\n}\n",
		},
		{
			name:   "an output comment after the body",
			source: "func ExampleOne() {\n}\n\n// Output: one\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := parseCommentedTargetSource(t, "package sample\n\n"+test.source)
			function := findTargetFunction(t, file, "ExampleOne")
			if got := hasExampleOutput(function, file.Comments); got != test.want {
				t.Fatalf("hasExampleOutput = %t, want %t", got, test.want)
			}
		})
	}
}

func TestAnExampleSignatureTakesNothingAndAnswersNothing(t *testing.T) {
	t.Parallel()
	aliases := map[string]bool{"testing": true}
	for _, test := range []struct {
		name   string
		source string
		want   bool
	}{
		{name: "nothing either way", source: "func ExampleOne() {}", want: true},
		{name: "a parameter", source: "func ExampleOne(value int) {}"},
		{name: "a result", source: "func ExampleOne() int { return 0 }"},
		{name: "both", source: "func ExampleOne(value int) int { return value }"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := parseTargetSource(t, "package sample\n\n"+test.source+"\n")
			function := findTargetFunction(t, file, "ExampleOne")
			if got := testingSignature(function, aliases, KindExample); got != test.want {
				t.Fatalf("testingSignature of an example = %t, want %t", got, test.want)
			}
		})
	}
}

func TestATargetKindReadsThePrefixAndWhatFollowsIt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		kind TargetKind
		want bool
	}{
		{name: "Test", kind: KindTest, want: true},
		{name: "Fuzz", kind: KindFuzz, want: true},
		{name: "Example", kind: KindExample, want: true},
		{name: "TestOne", kind: KindTest, want: true},
		{name: "Test_one", kind: KindTest, want: true},
		{name: "Testone"},
		{name: "Fuzzy"},
		{name: "Examples"},
		{name: "Helper"},
		{name: ""},
	} {
		t.Run("name "+test.name, func(t *testing.T) {
			t.Parallel()
			kind, ok := targetKind(test.name)
			if ok != test.want || (ok && kind != test.kind) {
				t.Fatalf("targetKind(%q) = (%q, %t), want (%q, %t)", test.name, kind, ok, test.kind, test.want)
			}
		})
	}
}
