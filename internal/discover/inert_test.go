// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

type probed struct {
	file    *ast.File
	info    *types.Info
	pkg     *types.Package
	tokFile *token.File
	expr    ast.Expr
}

func probeSource(t *testing.T, decls, expr string) probed {
	t.Helper()

	return probeIn(t, decls, "()", expr)
}

func probeIn(t *testing.T, decls, signature, expr string) probed {
	t.Helper()

	src := "package pkg\n\n" + decls + "\n\nfunc probe" + signature + " {\n\t_ = " + expr + "\n}\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "probe.go", src, 0)
	if err != nil {
		t.Fatalf("parsing:\n%s\n%v", src, err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("example.com/m/pkg", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("type-checking (a probe fixture must):\n%s\n%v", src, err)
	}
	return probed{
		file:    file,
		info:    info,
		pkg:     pkg,
		tokFile: fset.File(file.Package),
		expr:    probedExpr(t, file),
	}
}

func (p probed) resolver() *guardResolver {
	return newGuardResolver(p.file, p.info, p.pkg, p.tokFile, nil)
}

func inertProbe(t *testing.T, decls, expr string) bool {
	t.Helper()

	p := probeSource(t, decls, expr)
	return (&fileScan{info: p.info}).inert(p.expr)
}

func parseProbe(t *testing.T, src string) *ast.File {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "probe.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing:\n%s\n%v", src, err)
	}
	return file
}

func probeBody(t *testing.T, file *ast.File) []ast.Stmt {
	t.Helper()

	for _, decl := range file.Decls {
		if fn, isFunc := decl.(*ast.FuncDecl); isFunc && fn.Name.Name == "probe" {
			return fn.Body.List
		}
	}
	t.Fatalf("the probe fixture has no function named probe")
	return nil
}

func probedExpr(t *testing.T, file *ast.File) ast.Expr {
	t.Helper()

	for _, decl := range file.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if !isFunc || fn.Name.Name != "probe" || len(fn.Body.List) != 1 {
			continue
		}
		assign, isAssign := fn.Body.List[0].(*ast.AssignStmt)
		if !isAssign || len(assign.Rhs) != 1 {
			continue
		}
		return assign.Rhs[0]
	}
	t.Fatalf("the probe fixture has no `_ = <expr>` in a function named probe")
	return nil
}

func TestWhatCanBeEvaluatedWithoutTheProgramNoticing(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name  string
		decls string
		expr  string
		want  bool
	}{
		{name: "a name", decls: "var n int", expr: "n", want: true},
		{name: "a literal", expr: "42", want: true},
		{name: "a parenthesised name", decls: "var n int", expr: "(n)", want: true},
		{name: "a parenthesised call", decls: "func f() int { return 0 }", expr: "(f())"},
		{name: "a negated name", decls: "var b bool", expr: "!b", want: true},
		{name: "a negated call", decls: "func g() bool { return false }", expr: "!g()"},
		{name: "a numeric negation", decls: "var n int", expr: "-n", want: true},
		{name: "a receive", decls: "var ch chan int", expr: "<-ch"},
		{name: "an address", decls: "var n int", expr: "&n"},

		{name: "a qualified constant", decls: `import "time"`, expr: "time.Nanosecond", want: true},
		{name: "a field of a value", decls: "var s struct{ n int }", expr: "s.n", want: true},
		{name: "a field through a pointer", decls: "var p *struct{ n int }", expr: "p.n"},
		{name: "a field of a call's result", decls: "func f() struct{ n int } { return struct{ n int }{} }", expr: "f().n"},
		{name: "a method value", decls: "import \"time\"\n\nvar d time.Duration", expr: "d.String"},

		{name: "addition of names", decls: "var a, b int", expr: "a + b", want: true},
		{name: "division by a constant", decls: "var a int", expr: "a / 2", want: true},
		{name: "division by a name", decls: "var a, b int", expr: "a / b"},
		{name: "a remainder by a name", decls: "var a, b int", expr: "a % b"},
		{name: "a shift by a constant", decls: "var a int", expr: "a << 2", want: true},
		{name: "a shift by a name", decls: "var a int\n\nvar k uint", expr: "a << k"},
		{name: "an ordering comparison", decls: "var a, b int", expr: "a < b", want: true},

		{name: "a comparison against nil", decls: "var e error", expr: "e == nil", want: true},
		{name: "a comparison of ints", decls: "var a, b int", expr: "a == b", want: true},
		{name: "a comparison of pointers", decls: "var p, q *int", expr: "p == q", want: true},
		{name: "a comparison of channels", decls: "var a, b chan int", expr: "a == b", want: true},
		{name: "a comparison of arrays of ints", decls: "var a, b [3]int", expr: "a == b", want: true},
		{name: "a comparison of interfaces", decls: "var a, b any", expr: "a == b"},
		{name: "an interface against a concrete value", decls: "var v any\n\nvar n int", expr: "v == n"},
		{name: "a comparison of structs holding an interface", decls: "var a, b struct{ v any }", expr: "a == b"},
		{name: "a comparison of structs holding ints", decls: "var a, b struct{ n int }", expr: "a == b", want: true},
		{name: "a comparison of arrays of interfaces", decls: "var a, b [2]any", expr: "a == b"},

		{name: "a conversion to a basic type", decls: "var n int", expr: "int64(n)", want: true},
		{name: "a conversion to an array", decls: "var xs []int", expr: "[4]int(xs)"},
		{name: "a conversion to a pointer to an array", decls: "var xs []int", expr: "(*[4]int)(xs)"},
		{name: "a conversion to a pointer to a basic type", decls: "type myInt int\n\nvar p *myInt", expr: "(*int)(p)", want: true},
		{name: "a conversion of a call's result", decls: "func f() int { return 0 }", expr: "int64(f())"},
		{name: "a builtin over a slice", decls: "var xs []int", expr: "len(xs)", want: true},
		{name: "a builtin over a call's result", decls: "func f() []int { return nil }", expr: "len(f())"},
		{name: "an ordinary call", decls: "func f() int { return 0 }", expr: "f()"},
		{
			name:  "a call of a function the package named after a builtin",
			decls: "func len(xs []int) int { return 0 }\n\nvar xs []int",
			expr:  "len(xs)",
		},

		{name: "an index", decls: "var xs []int", expr: "xs[0]"},
		{name: "a slice", decls: "var xs []int", expr: "xs[1:]"},
		{name: "a type assertion", decls: "var v any", expr: "v.(int)"},
		{name: "a composite literal", expr: "[]int{1}"},
		{name: "a function literal", expr: "func() int { return 0 }"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := inertProbe(t, c.decls, c.expr); got != c.want {
				t.Errorf("inert(%s) = %v, want %v", c.expr, got, c.want)
			}
		})
	}
}

func TestAnExpressionThatIsNotThereIsNotInert(t *testing.T) {
	t.Parallel()

	if (&fileScan{}).inert(nil) {
		t.Error("inert(nil) = true, want false: a proof must not rest on an expression that is not there")
	}
}

func TestWhichTypesCanBeComparedWithoutAPanic(t *testing.T) {
	t.Parallel()

	comparableParam := typeParam(t, types.Universe.Lookup("comparable").Type())
	for _, c := range []struct {
		name string
		typ  types.Type
		want bool
	}{
		{name: "nothing at all", typ: nil},
		{name: "the type of an untyped nil", typ: types.Typ[types.UntypedNil]},
		{name: "the invalid type", typ: types.Typ[types.Invalid]},
		{name: "an int", typ: types.Typ[types.Int], want: true},
		{name: "a string", typ: types.Typ[types.String], want: true},
		{name: "a type parameter, however constrained", typ: comparableParam},
		{name: "a pointer to a type parameter", typ: types.NewPointer(comparableParam), want: true},
		{name: "an array of type parameters", typ: types.NewArray(comparableParam, 2)},
		{name: "an array of ints", typ: types.NewArray(types.Typ[types.Int], 2), want: true},
		{name: "a slice", typ: types.NewSlice(types.Typ[types.Int])},
		{name: "a map", typ: types.NewMap(types.Typ[types.Int], types.Typ[types.Int])},
		{name: "an interface", typ: types.NewInterfaceType(nil, nil)},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := comparableWithoutPanic(c.typ); got != c.want {
				t.Errorf("comparableWithoutPanic(%s) = %v, want %v", c.typ, got, c.want)
			}
		})
	}
}

func TestWhichTypesMayTurnOutToBeAnArray(t *testing.T) {
	t.Parallel()

	param := typeParam(t, types.NewInterfaceType(nil, nil))
	for _, c := range []struct {
		name string
		typ  types.Type
		want bool
	}{
		{name: "nothing at all", typ: nil, want: true},
		{name: "a type parameter", typ: param, want: true},
		{name: "an array", typ: types.NewArray(types.Typ[types.Int], 4), want: true},
		{name: "a slice", typ: types.NewSlice(types.Typ[types.Int])},
		{name: "an int", typ: types.Typ[types.Int]},
		{name: "a pointer to an array", typ: types.NewPointer(types.NewArray(types.Typ[types.Int], 4))},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := mayBeArray(c.typ); got != c.want {
				t.Errorf("mayBeArray(%s) = %v, want %v", c.typ, got, c.want)
			}
		})
	}

	for _, c := range []struct {
		name   string
		target types.Type
		want   bool
	}{
		{name: "to an int", target: types.Typ[types.Int], want: true},
		{name: "to a slice", target: types.NewSlice(types.Typ[types.Int]), want: true},
		{name: "to an array", target: types.NewArray(types.Typ[types.Int], 4)},
		{name: "to a pointer to an array", target: types.NewPointer(types.NewArray(types.Typ[types.Int], 4))},
		{name: "to a pointer to an int", target: types.NewPointer(types.Typ[types.Int]), want: true},
		{name: "to a pointer to a type parameter", target: types.NewPointer(param)},
	} {
		t.Run("a conversion "+c.name, func(t *testing.T) {
			t.Parallel()

			if got := inertConversion(c.target); got != c.want {
				t.Errorf("inertConversion(%s) = %v, want %v", c.target, got, c.want)
			}
		})
	}
}
