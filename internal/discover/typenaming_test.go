// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/types"
	"strconv"
	"testing"
)

type typeFixture struct {
	here  *types.Package
	other *types.Package
}

func newTypeFixture() typeFixture {
	return typeFixture{
		here:  types.NewPackage("example.com/m/pkg", "pkg"),
		other: types.NewPackage("example.com/m/other", "other"),
	}
}

func (f typeFixture) named(pkg *types.Package, name string, underlying types.Type) *types.Named {
	return types.NewNamed(types.NewTypeName(0, pkg, name, nil), underlying, nil)
}

func (f typeFixture) field(pkg *types.Package, name string, typ types.Type) *types.Var {
	return types.NewField(0, pkg, name, typ, false)
}

func (f typeFixture) method(pkg *types.Package, name string, result types.Type) *types.Func {
	sig := types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewVar(0, pkg, "", result)), false)
	return types.NewFunc(0, pkg, name, sig)
}

func (f typeFixture) param(name string) *types.TypeParam {
	p := types.NewTypeParam(types.NewTypeName(0, f.here, name, nil), nil)
	p.SetConstraint(types.NewInterfaceType(nil, nil))
	return p
}

func TestWhichTypesThisFileCanSpell(t *testing.T) {
	t.Parallel()

	f := newTypeFixture()
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

	hidden := f.named(f.other, "hidden", types.NewStruct(nil, nil))
	unspellable, err := types.Instantiate(nil, list, []types.Type{hidden}, false)
	if err != nil {
		t.Fatalf("instantiating List[other.hidden]: %v", err)
	}
	if g.nameable(unspellable, map[types.Type]bool{}) {
		t.Error("nameable accepted an instantiation whose argument it cannot spell")
	}
}

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

	for n := 2; n < 64; n++ {
		g.taken["carrier"+strconv.Itoa(n)] = true
	}
	if got := g.freeName("carrier"); got != "carrier" {
		t.Errorf("freeName over an exhausted search = %q, want the preferred name back", got)
	}
}

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

		if spelled, _, ok := resolver(none, none).typeString(types.Typ[types.UnsafePointer]); ok {
			t.Errorf("typeString = %q, want a refusal for unsafe.Pointer", spelled)
		}
	})

	t.Run("the completions come out in one order", func(t *testing.T) {
		t.Parallel()

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
