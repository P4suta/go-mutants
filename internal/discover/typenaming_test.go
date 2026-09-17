// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/types"
	"strconv"
	"testing"
)

// Whether a type can be written down in the file being rewritten, and whether
// it mentions a type parameter.
//
// Both are recursive walks over go/types' type graph, and both are asked
// before a guard commits to a form: Form D writes a declaration's type out,
// Form C' writes a conversion, Form E writes a closure's result. A predicate
// that said yes about a type it cannot spell produces a generated file that
// does not compile -- in a snapshot, about a program nobody wrote -- and one
// that said no too often turns a mutable site into an `unnameable-decl-type`
// skip nobody can act on.
//
// They are asked here with types built rather than parsed. A fixture can only
// hold the types somebody thought to write, and these walks have an arm for
// every shape go/types has: an unexported field of another package's struct, a
// method of an interface whose signature mentions one, a recursive type that
// reaches itself, a tuple, an alias with type arguments. Written as source,
// most of those are a package of their own; built, they are a line each.

// typeFixture is the two packages every row below is about: the one being
// rewritten, and another one.
type typeFixture struct {
	here  *types.Package
	other *types.Package
}

// newTypeFixture builds them.
func newTypeFixture() typeFixture {
	return typeFixture{
		here:  types.NewPackage("example.com/m/pkg", "pkg"),
		other: types.NewPackage("example.com/m/other", "other"),
	}
}

// named builds a named type in a package.
func (f typeFixture) named(pkg *types.Package, name string, underlying types.Type) *types.Named {
	return types.NewNamed(types.NewTypeName(0, pkg, name, nil), underlying, nil)
}

// field builds one struct field.
func (f typeFixture) field(pkg *types.Package, name string, typ types.Type) *types.Var {
	return types.NewField(0, pkg, name, typ, false)
}

// method builds one interface method with no parameters and one result.
func (f typeFixture) method(pkg *types.Package, name string, result types.Type) *types.Func {
	sig := types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewVar(0, pkg, "", result)), false)
	return types.NewFunc(0, pkg, name, sig)
}

// param builds a type parameter constrained to the empty interface.
func (f typeFixture) param(name string) *types.TypeParam {
	p := types.NewTypeParam(types.NewTypeName(0, f.here, name, nil), nil)
	p.SetConstraint(types.NewInterfaceType(nil, nil))
	return p
}

// TestWhichTypesThisFileCanSpell is [guardResolver.nameable], one row per arm.
func TestWhichTypesThisFileCanSpell(t *testing.T) {
	t.Parallel()

	f := newTypeFixture()
	// A resolver that has imported nothing: the other package is reachable
	// only through the sibling index, which the rows that need it set.
	resolver := func(siblings map[string]string) *guardResolver {
		return &guardResolver{
			pkg:      f.here,
			imports:  map[string]string{},
			added:    map[string]string{},
			taken:    map[string]bool{},
			siblings: siblings,
		}
	}
	reachableOther := map[string]string{"example.com/m/other": "other"}

	exported := f.named(f.other, "Exported", types.NewStruct(nil, nil))
	unexported := f.named(f.other, "hidden", types.NewStruct(nil, nil))
	ours := f.named(f.here, "Ours", types.NewStruct(nil, nil))
	universe := types.Universe.Lookup("error").Type()

	for _, c := range []struct {
		name     string
		typ      types.Type
		siblings map[string]string
		want     bool
	}{
		{name: "nothing at all", typ: nil},
		{name: "an int", typ: types.Typ[types.Int], want: true},
		{name: "the invalid type", typ: types.Typ[types.Invalid]},
		{name: "unsafe.Pointer", typ: types.Typ[types.UnsafePointer]},
		{name: "an untyped constant's type", typ: types.Typ[types.UntypedInt]},
		{name: "the type of an untyped nil", typ: types.Typ[types.UntypedNil]},
		{name: "a type of this package", typ: ours, want: true},
		{name: "a universe type", typ: universe, want: true},
		{name: "an exported type of an unreachable package", typ: exported},
		{name: "an exported type of a reachable package", typ: exported, siblings: reachableOther, want: true},
		{name: "an unexported type of a reachable package", typ: unexported, siblings: reachableOther},
		{name: "a type parameter", typ: f.param("T"), want: true},
		{name: "a pointer to something spellable", typ: types.NewPointer(ours), want: true},
		{name: "a pointer to something unspellable", typ: types.NewPointer(exported)},
		{name: "a slice of something unspellable", typ: types.NewSlice(exported)},
		{name: "an array of something unspellable", typ: types.NewArray(exported, 3)},
		{name: "a channel of something unspellable", typ: types.NewChan(types.SendRecv, exported)},
		{
			name: "a map whose key is unspellable",
			typ:  types.NewMap(exported, types.Typ[types.Int]),
		},
		{
			name: "a map whose value is unspellable",
			typ:  types.NewMap(types.Typ[types.Int], exported),
		},
		{
			name: "a map of spellable parts",
			typ:  types.NewMap(types.Typ[types.String], ours),
			want: true,
		},
		{
			name: "a struct of this package's own fields",
			typ:  types.NewStruct([]*types.Var{f.field(f.here, "hidden", types.Typ[types.Int])}, nil),
			want: true,
		},
		{
			// A field another package declared and did not export cannot be
			// written in a composite literal here, however spellable its type.
			name: "a struct with another package's unexported field",
			typ:  types.NewStruct([]*types.Var{f.field(f.other, "hidden", types.Typ[types.Int])}, nil),
		},
		{
			name: "a struct with another package's exported field",
			typ:  types.NewStruct([]*types.Var{f.field(f.other, "Shown", types.Typ[types.Int])}, nil),
			want: true,
		},
		{
			name: "a struct whose field type is unspellable",
			typ:  types.NewStruct([]*types.Var{f.field(f.here, "n", exported)}, nil),
		},
		{
			name: "an interface with another package's unexported method",
			typ:  types.NewInterfaceType([]*types.Func{f.method(f.other, "hidden", types.Typ[types.Int])}, nil),
		},
		{
			name: "an interface with an exported method",
			typ:  types.NewInterfaceType([]*types.Func{f.method(f.other, "Shown", types.Typ[types.Int])}, nil),
			want: true,
		},
		{
			name: "an interface whose method result is unspellable",
			typ:  types.NewInterfaceType([]*types.Func{f.method(f.here, "Shown", exported)}, nil),
		},
		{
			name: "an interface embedding something unspellable",
			typ:  types.NewInterfaceType(nil, []types.Type{exported}),
		},
		{
			name: "an interface embedding something spellable",
			typ:  types.NewInterfaceType(nil, []types.Type{universe}),
			want: true,
		},
		{
			name: "a signature whose parameter is unspellable",
			typ: types.NewSignatureType(nil, nil, nil,
				types.NewTuple(types.NewVar(0, f.here, "a", exported)), nil, false),
		},
		{
			name: "a signature whose result is unspellable",
			typ: types.NewSignatureType(nil, nil, nil, nil,
				types.NewTuple(types.NewVar(0, f.here, "", exported)), false),
		},
		{
			name: "a signature of spellable parts",
			typ: types.NewSignatureType(nil, nil, nil,
				types.NewTuple(types.NewVar(0, f.here, "a", types.Typ[types.Int])),
				types.NewTuple(types.NewVar(0, f.here, "", types.Typ[types.Bool])), false),
			want: true,
		},
		{name: "an empty tuple", typ: types.NewTuple(), want: true},
		{
			// An alias is a second name for a type and is spelled like a named
			// one: its own name has to be writable here, and so does every
			// argument it was instantiated with.
			name: "an alias of this package's own",
			typ:  types.NewAlias(types.NewTypeName(0, f.here, "Alias", nil), types.Typ[types.Int]),
			want: true,
		},
		{
			name: "an alias of an unreachable package",
			typ:  types.NewAlias(types.NewTypeName(0, f.other, "Alias", nil), types.Typ[types.Int]),
		},
		{
			name:     "an alias of a reachable package",
			typ:      types.NewAlias(types.NewTypeName(0, f.other, "Alias", nil), types.Typ[types.Int]),
			siblings: reachableOther, want: true,
		},
		{
			// A type set, which is what an interface embedding `~int | ~string`
			// holds. It is a type go/types builds and no file writes on its
			// own, so it falls off the end of the list rather than being
			// admitted by silence.
			name: "a union of terms",
			typ: types.NewUnion([]*types.Term{
				types.NewTerm(true, types.Typ[types.Int]),
				types.NewTerm(true, types.Typ[types.String]),
			}),
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			g := resolver(c.siblings)
			if got := g.nameable(c.typ, map[types.Type]bool{}); got != c.want {
				t.Errorf("nameable(%s) = %v, want %v", c.typ, got, c.want)
			}
		})
	}
}

// TestANamedTypeIsSpelledByItsName is what makes both walks terminate over a
// type that reaches itself.
//
// `type node struct { next *node }` is ordinary Go and its type graph is a
// cycle. Neither walk follows it, and the reason is not the recursion guard:
// a named type is written down as its *name*, so what its definition holds is
// not something either question has to look at. The guard is there for the
// shapes that have no name to stop at.
//
// The consequence is worth stating in both directions, because it is easy to
// read these walks as answering about a type's contents. A named type whose
// definition holds another package's unexported type is still perfectly
// spellable, and one whose definition holds a type parameter still mentions
// none -- what mentions one is a name written *with* an argument.
func TestANamedTypeIsSpelledByItsName(t *testing.T) {
	t.Parallel()

	f := newTypeFixture()
	g := &guardResolver{
		pkg:      f.here,
		imports:  map[string]string{},
		added:    map[string]string{},
		taken:    map[string]bool{},
		siblings: map[string]string{},
	}

	node := f.named(f.here, "node", types.NewStruct(nil, nil))
	node.SetUnderlying(types.NewStruct([]*types.Var{f.field(f.here, "next", types.NewPointer(node))}, nil))
	if !g.nameable(node, map[types.Type]bool{}) {
		t.Error("nameable refused a recursive type of this package's own")
	}
	if mentionsTypeParam(node, map[types.Type]bool{}) {
		t.Error("mentionsTypeParam looked inside a named type's definition")
	}

	// And an instantiation, where the argument *is* written down: `List[T]`
	// inside a generic function needs T in scope, and `List[int]` does not.
	param := f.param("T")
	list := types.NewNamed(types.NewTypeName(0, f.here, "List", nil), nil, nil)
	element := types.NewTypeParam(types.NewTypeName(0, f.here, "E", nil), nil)
	element.SetConstraint(types.NewInterfaceType(nil, nil))
	list.SetTypeParams([]*types.TypeParam{element})
	list.SetUnderlying(types.NewSlice(element))

	generic, err := types.Instantiate(nil, list, []types.Type{param}, false)
	if err != nil {
		t.Fatalf("instantiating List[T]: %v", err)
	}
	if !mentionsTypeParam(generic, map[types.Type]bool{}) {
		t.Error("mentionsTypeParam missed a type parameter written as a type argument")
	}
	if !g.nameable(generic, map[types.Type]bool{}) {
		t.Error("nameable refused this package's own generic type instantiated with a parameter")
	}

	concrete, err := types.Instantiate(nil, list, []types.Type{types.Typ[types.Int]}, false)
	if err != nil {
		t.Fatalf("instantiating List[int]: %v", err)
	}
	if mentionsTypeParam(concrete, map[types.Type]bool{}) {
		t.Error("mentionsTypeParam found a parameter in a concrete instantiation")
	}

	// And an argument this file cannot spell makes the whole instantiation
	// unspellable, which is the arm that would otherwise render
	// `List[other.hidden]`.
	hidden := f.named(f.other, "hidden", types.NewStruct(nil, nil))
	unspellable, err := types.Instantiate(nil, list, []types.Type{hidden}, false)
	if err != nil {
		t.Fatalf("instantiating List[other.hidden]: %v", err)
	}
	if g.nameable(unspellable, map[types.Type]bool{}) {
		t.Error("nameable accepted an instantiation whose argument it cannot spell")
	}
}

// TestWhichTypesMentionATypeParameter is [mentionsTypeParam], which decides
// whether a rendered type is one a *generic* declaration could carry.
//
// It is the same shape of walk as nameable and a different question, so the two
// are asked side by side: a type may be perfectly spellable and still mention a
// parameter, and every arm that forgot to look inside itself would answer no
// for a type whose parameter is one level down.
func TestWhichTypesMentionATypeParameter(t *testing.T) {
	t.Parallel()

	f := newTypeFixture()
	param := f.param("T")
	plain := types.Typ[types.Int]

	for _, c := range []struct {
		name string
		typ  types.Type
		want bool
	}{
		{name: "nothing at all", typ: nil},
		{name: "an int", typ: plain},
		{name: "a type parameter", typ: param, want: true},
		{name: "a pointer to one", typ: types.NewPointer(param), want: true},
		{name: "a pointer to none", typ: types.NewPointer(plain)},
		{name: "a slice of one", typ: types.NewSlice(param), want: true},
		{name: "a slice of none", typ: types.NewSlice(plain)},
		{name: "an array of one", typ: types.NewArray(param, 2), want: true},
		{name: "an array of none", typ: types.NewArray(plain, 2)},
		{name: "a channel of one", typ: types.NewChan(types.SendRecv, param), want: true},
		{name: "a channel of none", typ: types.NewChan(types.SendRecv, plain)},
		{name: "an alias of one", typ: types.NewAlias(types.NewTypeName(0, f.here, "A", nil), param), want: true},
		{name: "a map keyed by one", typ: types.NewMap(param, plain), want: true},
		{name: "a map valued by one", typ: types.NewMap(plain, param), want: true},
		{name: "a map of neither", typ: types.NewMap(plain, plain)},
		{
			name: "a struct holding one",
			typ:  types.NewStruct([]*types.Var{f.field(f.here, "v", param)}, nil),
			want: true,
		},
		{
			name: "a struct holding none",
			typ:  types.NewStruct([]*types.Var{f.field(f.here, "v", plain)}, nil),
		},
		{
			name: "an interface whose method mentions one",
			typ:  types.NewInterfaceType([]*types.Func{f.method(f.here, "Get", param)}, nil),
			want: true,
		},
		{
			name: "an interface whose method mentions none",
			typ:  types.NewInterfaceType([]*types.Func{f.method(f.here, "Get", plain)}, nil),
		},
		{
			name: "an interface embedding one that mentions it",
			typ: types.NewInterfaceType(nil, []types.Type{
				types.NewInterfaceType([]*types.Func{f.method(f.here, "Get", param)}, nil),
			}),
			want: true,
		},
		{
			name: "a signature whose parameter mentions one",
			typ: types.NewSignatureType(nil, nil, nil,
				types.NewTuple(types.NewVar(0, f.here, "a", param)), nil, false),
			want: true,
		},
		{
			name: "a signature whose result mentions one",
			typ: types.NewSignatureType(nil, nil, nil, nil,
				types.NewTuple(types.NewVar(0, f.here, "", param)), false),
			want: true,
		},
		{
			name: "a signature mentioning none",
			typ: types.NewSignatureType(nil, nil, nil,
				types.NewTuple(types.NewVar(0, f.here, "a", plain)), nil, false),
		},
		{name: "an empty tuple", typ: types.NewTuple()},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := mentionsTypeParam(c.typ, map[types.Type]bool{}); got != c.want {
				t.Errorf("mentionsTypeParam(%s) = %v, want %v", c.typ, got, c.want)
			}
		})
	}
}

// TestAPackageIsReachableThreeWays pins [guardResolver.reachable], which is
// what stands between a rendered qualifier and an import the file does not
// have.
//
// The three ways are not interchangeable. A package the file already imports
// needs no completion; one a *sibling* file imports may be completed, because
// the package compiles today with that edge in its graph; and one an earlier
// guard in this same file already added is reachable because that guard's own
// import is going in. Anything else is a package this rewrite may not name.
func TestAPackageIsReachableThreeWays(t *testing.T) {
	t.Parallel()

	f := newTypeFixture()
	elsewhere := types.NewPackage("example.com/m/elsewhere", "elsewhere")

	base := func() *guardResolver {
		return &guardResolver{
			pkg:      f.here,
			imports:  map[string]string{},
			added:    map[string]string{},
			taken:    map[string]bool{},
			siblings: map[string]string{},
		}
	}

	if !base().reachable(nil) {
		t.Error("a type with no package is not reachable, and a universe type has none")
	}
	if !base().reachable(f.here) {
		t.Error("this package is not reachable from itself")
	}
	if base().reachable(elsewhere) {
		t.Error("a package nothing imports is reachable")
	}

	imported := base()
	imported.imports["example.com/m/elsewhere"] = ""
	if !imported.reachable(elsewhere) {
		t.Error("a package this file imports is not reachable")
	}

	added := base()
	added.added["example.com/m/elsewhere"] = "elsewhere"
	if !added.reachable(elsewhere) {
		t.Error("a package an earlier guard added is not reachable")
	}

	sibling := base()
	sibling.siblings["example.com/m/elsewhere"] = "elsewhere"
	if !sibling.reachable(elsewhere) {
		t.Error("a package a sibling file imports is not reachable")
	}
}

// TestANameIsBumpedUntilItIsFree pins [guardResolver.freeName], and the bound
// on the search.
//
// The counter stops because an unbounded search over a set that only grows is a
// loop whose termination depends on the data. A file binding `x`, `x2` … `x63`
// is not one this tool needs to rewrite, and the preferred name is handed back
// rather than the search running on -- the caller's own check is what refuses
// the collision after that.
func TestANameIsBumpedUntilItIsFree(t *testing.T) {
	t.Parallel()

	g := &guardResolver{taken: map[string]bool{}}
	if got := g.freeName("carrier"); got != "carrier" {
		t.Errorf("freeName over an empty file = %q, want %q", got, "carrier")
	}

	g.taken["carrier"] = true
	if got := g.freeName("carrier"); got != "carrier2" {
		t.Errorf("freeName = %q, want %q", got, "carrier2")
	}

	g.taken["carrier2"] = true
	if got := g.freeName("carrier"); got != "carrier3" {
		t.Errorf("freeName = %q, want %q", got, "carrier3")
	}

	// Every name the search can reach is taken, so the search ends rather than
	// running on.
	for n := 2; n < 64; n++ {
		g.taken["carrier"+strconv.Itoa(n)] = true
	}
	if got := g.freeName("carrier"); got != "carrier" {
		t.Errorf("freeName over an exhausted search = %q, want the preferred name back", got)
	}
}

// TestHowATypeIsSpelledInThisFile is [guardResolver.typeString] and the
// qualifier under it, which is what every form that writes a type down goes
// through.
//
// A type is spelled with the name its package has *in the file being
// rewritten*, and there are four answers: this package's own types render bare,
// a package the file imports renders under the name that import binds, a
// package only a *sibling* file imports renders under a name this rewrite will
// add, and anything else is not spellable here at all.
func TestHowATypeIsSpelledInThisFile(t *testing.T) {
	t.Parallel()

	f := newTypeFixture()
	elsewhere := types.NewPackage("example.com/m/elsewhere", "elsewhere")
	exported := f.named(elsewhere, "Box", types.NewStruct(nil, nil))
	ours := f.named(f.here, "Ours", types.NewStruct(nil, nil))

	resolver := func(imports, siblings map[string]string) *guardResolver {
		return &guardResolver{
			pkg:      f.here,
			imports:  imports,
			added:    map[string]string{},
			taken:    map[string]bool{},
			siblings: siblings,
		}
	}
	none := map[string]string{}

	t.Run("this package's own type renders bare", func(t *testing.T) {
		t.Parallel()

		spelled, needs, ok := resolver(none, none).typeString(ours)
		if !ok || spelled != "Ours" {
			t.Fatalf("typeString = (%q, %v)", spelled, ok)
		}
		if len(needs) != 0 {
			t.Errorf("spelling this package's own type needs %v", needs)
		}
	})

	t.Run("a type of a package this file imports", func(t *testing.T) {
		t.Parallel()

		spelled, needs, ok := resolver(map[string]string{"example.com/m/elsewhere": ""}, none).
			typeString(types.NewSlice(exported))
		if !ok || spelled != "[]elsewhere.Box" {
			t.Fatalf("typeString = (%q, %v), want []elsewhere.Box", spelled, ok)
		}
		if len(needs) != 0 {
			t.Errorf("spelling a type this file already imports needs %v", needs)
		}
	})

	t.Run("a type under the name the import binds", func(t *testing.T) {
		t.Parallel()

		spelled, _, ok := resolver(map[string]string{"example.com/m/elsewhere": "alias"}, none).
			typeString(exported)
		if !ok || spelled != "alias.Box" {
			t.Fatalf("typeString = (%q, %v), want alias.Box", spelled, ok)
		}
	})

	t.Run("a type of a package only a sibling imports", func(t *testing.T) {
		t.Parallel()

		g := resolver(none, map[string]string{"example.com/m/elsewhere": "carrier"})
		spelled, needs, ok := g.typeString(types.NewPointer(exported))
		if !ok || spelled != "*carrier.Box" {
			t.Fatalf("typeString = (%q, %v), want *carrier.Box", spelled, ok)
		}
		if len(needs) != 1 || needs[0].Path != "example.com/m/elsewhere" || needs[0].Local != "carrier" {
			t.Fatalf("the completion is %v, want one naming the sibling's import", needs)
		}
		// The name is chosen once per path per file and remembered: two names
		// for one package would be two imports, and the second a redeclaration.
		again, _, ok := g.typeString(exported)
		if !ok || again != "carrier.Box" {
			t.Errorf("the second spelling is %q, want the first's name", again)
		}
	})

	t.Run("a name the file already binds is bumped", func(t *testing.T) {
		t.Parallel()

		g := resolver(none, map[string]string{"example.com/m/elsewhere": "carrier"})
		g.taken["carrier"] = true
		spelled, needs, ok := g.typeString(exported)
		if !ok || spelled != "carrier2.Box" {
			t.Fatalf("typeString = (%q, %v), want carrier2.Box", spelled, ok)
		}
		if len(needs) != 1 || needs[0].Local != "carrier2" {
			t.Errorf("the completion is %v, want the bumped name", needs)
		}
	})

	t.Run("a sibling that named no alias falls back to the package's own name", func(t *testing.T) {
		t.Parallel()

		g := resolver(none, map[string]string{"example.com/m/elsewhere": ""})
		spelled, needs, ok := g.typeString(exported)
		if !ok || spelled != "elsewhere.Box" {
			t.Fatalf("typeString = (%q, %v), want elsewhere.Box", spelled, ok)
		}
		if len(needs) != 1 || needs[0].Local != "elsewhere" {
			t.Errorf("the completion is %v, want the package's own name", needs)
		}
	})

	t.Run("a type of a package nothing imports", func(t *testing.T) {
		t.Parallel()

		if spelled, _, ok := resolver(none, none).typeString(exported); ok {
			t.Errorf("typeString = %q, want a refusal for a package nothing reaches", spelled)
		}
	})

	t.Run("no type at all", func(t *testing.T) {
		t.Parallel()

		if spelled, _, ok := resolver(none, none).typeString(nil); ok {
			t.Errorf("typeString = %q, want a refusal", spelled)
		}
	})

	t.Run("a type that renders but cannot be named", func(t *testing.T) {
		t.Parallel()

		// unsafe.Pointer renders as a perfectly plausible string and is not
		// something this file can write without an import nothing will add. It
		// is the shape the two checks are separate for: the qualifier answers
		// about *packages*, and nameable answers about the type.
		if spelled, _, ok := resolver(none, none).typeString(types.Typ[types.UnsafePointer]); ok {
			t.Errorf("typeString = %q, want a refusal for unsafe.Pointer", spelled)
		}
	})

	t.Run("the completions come out in one order", func(t *testing.T) {
		t.Parallel()

		// Two packages in one type, drawn in the order go/types renders them.
		// The list is sorted so that the imports a rewrite splices in are a
		// function of the type rather than of the rendering order.
		second := types.NewPackage("example.com/m/aaa", "aaa")
		other := f.named(second, "Other", types.NewStruct(nil, nil))
		g := resolver(none, map[string]string{
			"example.com/m/elsewhere": "elsewhere",
			"example.com/m/aaa":       "aaa",
		})
		_, needs, ok := g.typeString(types.NewMap(exported, other))
		if !ok {
			t.Fatal("typeString refused a map of two reachable types")
		}
		if len(needs) != 2 || needs[0].Path != "example.com/m/aaa" {
			t.Errorf("the completions are %v, want them sorted by path", needs)
		}
	})
}
