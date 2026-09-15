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

// What a branch proof rests on: the claim that evaluating a condition does
// nothing a test could observe.
//
// The proof published for a narrowing edit says that a test during which none
// of the guarded body ran could not have told the two programs apart. That is
// only true if evaluating the condition itself is invisible -- no effect, no
// panic, no non-termination -- so [fileScan.inert] is the load-bearing half of
// the record, and every expression kind it admits is one the proof is staked
// on. These are written as questions about expressions rather than as
// assertions about a fixture's candidates, because the interesting shapes are
// the refusals and a fixture holds only what somebody thought to put in it.

// probed is a type-checked file built around one expression, and the expression
// itself.
//
// Every predicate in this package reads the checker's record of what a name
// denotes, what a selection selects and what a call calls, so an expression
// assembled by hand has no record and every question about it would be answered
// from the syntax alone. Building the file is the only honest way to ask, and
// the same file answers for [fileScan] and for [guardResolver] alike.
type probed struct {
	file    *ast.File
	info    *types.Info
	pkg     *types.Package
	tokFile *token.File
	expr    ast.Expr
}

// probeSource type-checks `decls` and one expression, and returns both.
//
// The expression is the right-hand side of the one assignment in a function
// named probe, which is how it is found again without counting nodes.
func probeSource(t *testing.T, decls, expr string) probed {
	t.Helper()

	return probeIn(t, decls, "()", expr)
}

// probeIn is [probeSource] with a signature of the caller's own, which is how a
// fixture reaches the shapes only a generic function can hold: a field selected
// from a type parameter whose constraint has a core struct type is a field
// selection whose base has an interface underneath, and no ordinary function
// can be written to produce one.
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

// resolver is the guard resolver for the probed file.
func (p probed) resolver() *guardResolver {
	return newGuardResolver(p.file, p.info, p.pkg, p.tokFile, nil)
}

// inertProbe answers [fileScan.inert] for the probed expression.
func inertProbe(t *testing.T, decls, expr string) bool {
	t.Helper()

	p := probeSource(t, decls, expr)
	return (&fileScan{info: p.info}).inert(p.expr)
}

// parseProbe parses a probe fixture, which is a whole file the caller wrote.
func parseProbe(t *testing.T, src string) *ast.File {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "probe.go", src, 0)
	if err != nil {
		t.Fatalf("parsing:\n%s\n%v", src, err)
	}
	return file
}

// probeBody is the body of the function named probe.
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

// probedExpr is the right-hand side of the assignment in `probe`.
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

// TestWhatCanBeEvaluatedWithoutTheProgramNoticing is the allowlist itself, one
// row per shape it admits and one per shape it refuses.
//
// The refusals are the half that matters. Admitting an expression that panics,
// blocks or allocates would publish a proof that is simply false, and the
// failure mode is silent: a consumer that trusted it would skip a mutant a test
// really could have told apart.
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

		// Selections. The checker leaves a qualified identifier out of its
		// selection table, which is the only way this phase can tell one from a
		// field read written the same way.
		{name: "a qualified constant", decls: `import "time"`, expr: "time.Nanosecond", want: true},
		{name: "a field of a value", decls: "var s struct{ n int }", expr: "s.n", want: true},
		{name: "a field through a pointer", decls: "var p *struct{ n int }", expr: "p.n"},
		{name: "a field of a call's result", decls: "func f() struct{ n int } { return struct{ n int }{} }", expr: "f().n"},
		{name: "a method value", decls: "import \"time\"\n\nvar d time.Duration", expr: "d.String"},

		// Arithmetic. Total on every operand type the compiler admits, except
		// where a run-time value can make it trap.
		{name: "addition of names", decls: "var a, b int", expr: "a + b", want: true},
		{name: "division by a constant", decls: "var a int", expr: "a / 2", want: true},
		{name: "division by a name", decls: "var a, b int", expr: "a / b"},
		{name: "a remainder by a name", decls: "var a, b int", expr: "a % b"},
		{name: "a shift by a constant", decls: "var a int", expr: "a << 2", want: true},
		{name: "a shift by a name", decls: "var a int\n\nvar k uint", expr: "a << k"},
		{name: "an ordering comparison", decls: "var a, b int", expr: "a < b", want: true},

		// Equality, which is where a comparison of interface values can panic.
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

		// Calls. A conversion computes a value; everything else runs code this
		// phase cannot see.
		{name: "a conversion to a basic type", decls: "var n int", expr: "int64(n)", want: true},
		{name: "a conversion to an array", decls: "var xs []int", expr: "[4]int(xs)"},
		{name: "a conversion to a pointer to an array", decls: "var xs []int", expr: "(*[4]int)(xs)"},
		{name: "a conversion to a pointer to a basic type", decls: "type myInt int\n\nvar p *myInt", expr: "(*int)(p)", want: true},
		{name: "a conversion of a call's result", decls: "func f() int { return 0 }", expr: "int64(f())"},
		{name: "a builtin over a slice", decls: "var xs []int", expr: "len(xs)", want: true},
		{name: "a builtin over a call's result", decls: "func f() []int { return nil }", expr: "len(f())"},
		{name: "an ordinary call", decls: "func f() int { return 0 }", expr: "f()"},
		{
			// The name is the builtin's; the object is not. A package that
			// declares its own `len` gets an ordinary call, and an ordinary call
			// runs code.
			name:  "a call of a function the package named after a builtin",
			decls: "func len(xs []int) int { return 0 }\n\nvar xs []int",
			expr:  "len(xs)",
		},

		// And the shapes nobody taught it, refused by falling off the end.
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

// TestAnExpressionThatIsNotThereIsNotInert is what the walk's refusal rests on.
//
// [fileScan.gatedBody] says "no proof" by returning the nil condition, and the
// caller puts every condition through inert without asking first. That is only
// a refusal because the allowlist has no case for an absent expression, so it
// is pinned here rather than left to be rediscovered by whoever adds the
// fiftieth case to the switch.
func TestAnExpressionThatIsNotThereIsNotInert(t *testing.T) {
	t.Parallel()

	if (&fileScan{}).inert(nil) {
		t.Error("inert(nil) = true, want false: a proof must not rest on an expression that is not there")
	}
}

// TestWhichTypesCanBeComparedWithoutAPanic is [comparableWithoutPanic] over the
// types a file cannot easily be written to produce.
//
// Three of them are unreachable from source through [fileScan.safelyComparable]
// -- the absent type, the type of an untyped `nil`, and the invalid type are
// all screened off before the question is put -- and one, a type parameter, is
// reachable only from a generic function. They are asked directly because the
// predicate is recursive: an array of a struct of a type parameter reaches all
// of them, and a test that could only build the types Go lets it compare would
// be testing the language rather than the predicate.
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

// TestWhichTypesMayTurnOutToBeAnArray is [mayBeArray], which is the whole of
// what [inertConversion] refuses.
//
// It fails towards "yes" on purpose: a conversion to an array checks the length
// of the slice it came from and panics when it is short, so a type this phase
// cannot rule an array out of has to be treated as one. The two rows a file
// cannot produce -- the absent type and a type parameter -- are the two rows
// that say so.
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

	// And the conversion predicate over the same types, which is where the
	// pointer case earns its line: `(*[4]int)(xs)` panics for exactly the reason
	// `[4]int(xs)` does, one indirection later.
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
