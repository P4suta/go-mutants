// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package discover

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// How a type renders is normally a private matter between go/types and whoever
// is printing a diagnostic. The neutral-value family makes it public: the
// rendered type is the *replacement text* of a mutant, and the replacement text
// is one of the nine fields hashed into the mutant identity
// (internal/mutation/id.go). Two consequences follow, and these tests are one
// each.
//
//   - If a toolchain changes how a type prints -- and it has: `any` is printed
//     for one empty interface and `interface{}` for another, a distinction the
//     printer did not always make -- then every mutant of this family is
//     silently reissued under a new identity, every `[[mutation.expect]]` line
//     naming one goes stale, and every cached outcome misses. The answer is to
//     bump the rule version deliberately, which is only possible if something
//     notices. TestTheEmptyValueRendersTheBytesThisToolchainPrints notices.
//   - A rendered type is also *spliced into source*, so it has to parse as a
//     composite literal where the return value was.
//     TestEveryRenderedEmptyValueIsLegalGo proves that for the same table,
//     without trusting that reading it is enough.

// spellingCase is one type and the empty value this file may write for it.
type spellingCase struct {
	name string
	// imports is the import block the fixture needs, or empty.
	imports string
	// decl is a type declaration the fixture needs, or empty.
	decl string
	// result is the declared result type, written as source.
	result string
	// want is the replacement text the family must produce for it, byte for
	// byte. A change here is a change to every one of those mutants' identity.
	want string
	// mapped says the candidate is a map rather than a slice, which is the rule
	// name it carries.
	mapped bool
}

// spellingCases covers every type constructor a slice or a map can be built
// from, because the renderer walks the whole type and any of them could start
// printing differently.
var spellingCases = []spellingCase{{
	name:   "a slice of a predeclared type",
	result: "[]string",
	want:   "[]string{}",
}, {
	name:   "a map of predeclared types",
	result: "map[string]int",
	mapped: true,
	want:   "map[string]int{}",
}, {
	name:   "a slice of slices",
	result: "[][]byte",
	want:   "[][]byte{}",
}, {
	name:   "a slice of arrays",
	result: "[][4]byte",
	want:   "[][4]byte{}",
}, {
	name:   "a slice of pointers to a local named type",
	decl:   "type Node struct{ N int }",
	result: "[]*Node",
	want:   "[]*Node{}",
}, {
	name:   "a slice of the empty interface written as any",
	result: "[]any",
	want:   "[]any{}",
}, {
	name:   "a slice of the empty interface written out",
	result: "[]interface{}",
	want:   "[]interface{}{}",
}, {
	// go/types prints a composite type without the spaces gofmt would put in
	// it, here and in the struct cases below. That is not cosmetic: these bytes
	// are the replacement, and the replacement is hashed. A future renderer
	// that inserts the spaces would be a correct renderer producing a different
	// mutant identity, which is the whole reason this table is verbatim.
	name:   "a slice of a spelled-out interface",
	result: "[]interface{ Read() error }",
	want:   "[]interface{Read() error}{}",
}, {
	name:   "a slice of the predeclared error",
	result: "[]error",
	want:   "[]error{}",
}, {
	name:   "a slice of anonymous structs",
	result: "[]struct{ N int }",
	want:   "[]struct{N int}{}",
}, {
	name:   "a slice of functions",
	result: "[]func(int) (string, error)",
	want:   "[]func(int) (string, error){}",
}, {
	name:   "a slice of variadic functions",
	result: "[]func(...int) bool",
	want:   "[]func(...int) bool{}",
}, {
	name:   "a slice of bidirectional channels",
	result: "[]chan int",
	want:   "[]chan int{}",
}, {
	name:   "a slice of receive-only channels",
	result: "[]<-chan int",
	want:   "[]<-chan int{}",
}, {
	name:   "a slice of send-only channels",
	result: "[]chan<- int",
	want:   "[]chan<- int{}",
}, {
	name:   "a named slice type",
	decl:   "type Lines []string",
	result: "Lines",
	want:   "Lines{}",
}, {
	name:   "a named map type",
	decl:   "type Index map[string]int",
	result: "Index",
	mapped: true,
	want:   "Index{}",
}, {
	name:   "a map keyed by a named type",
	decl:   "type Key string",
	result: "map[Key][]int",
	mapped: true,
	want:   "map[Key][]int{}",
}, {
	name:   "a map keyed by a struct",
	result: "map[struct{ N int }]bool",
	mapped: true,
	want:   "map[struct{N int}]bool{}",
}, {
	name:    "a slice of an imported type",
	imports: `import "bytes"`,
	result:  "[]bytes.Buffer",
	want:    "[]bytes.Buffer{}",
}, {
	name:    "a slice of pointers to an imported type",
	imports: `import "bytes"`,
	result:  "[]*bytes.Buffer",
	want:    "[]*bytes.Buffer{}",
}, {
	name:    "a slice of an imported type under a local name",
	imports: `import bb "bytes"`,
	result:  "[]bb.Buffer",
	want:    "[]bb.Buffer{}",
}, {
	name:    "a map from an imported type to another",
	imports: "import (\n\t\"bytes\"\n\t\"strings\"\n)",
	result:  "map[*bytes.Buffer]*strings.Reader",
	mapped:  true,
	want:    "map[*bytes.Buffer]*strings.Reader{}",
}, {
	name:   "a slice of a generic instantiation",
	decl:   "type Box[T any] struct{ V T }",
	result: "[]Box[int]",
	want:   "[]Box[int]{}",
}}

// source is the fixture that returns one value of the case's type.
func (c spellingCase) source() string {
	var b strings.Builder
	b.WriteString("package pkg\n\n")
	if c.imports != "" {
		b.WriteString(c.imports + "\n\n")
	}
	if c.decl != "" {
		b.WriteString(c.decl + "\n\n")
	}
	b.WriteString("func Value(in " + c.result + ") " + c.result + " {\n\treturn in\n}\n")
	return b.String()
}

// rule is the rule name the case's candidate must carry.
func (c spellingCase) rule() string {
	if c.mapped {
		return "return-empty-map"
	}
	return "return-empty-slice"
}

// TestTheEmptyValueRendersTheBytesThisToolchainPrints pins the replacement text
// of every shape a neutral value can take.
//
// It is deliberately a verbatim table and not a re-derivation. Re-deriving the
// expected string from types.TypeString would agree with any rendering the
// toolchain chose, including a new one -- and agreeing with the new rendering
// is exactly the silent reissue this test exists to catch. When Go changes a
// rendering, the honest response is to update a line here and bump the rule's
// version in the same commit, so that the identity change is one someone
// decided rather than one that happened.
func TestTheEmptyValueRendersTheBytesThisToolchainPrints(t *testing.T) {
	t.Parallel()

	for _, c := range spellingCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := scanSource(t, c.source())
			if !got.has(c.rule(), "in", c.want) {
				t.Errorf("scan found %v, want %s in->%s", got.rules(), c.rule(), c.want)
			}
		})
	}
}

// TestEveryRenderedEmptyValueIsLegalGo is the other half, and it is not
// implied by the first: a replacement can be the text a reader expects and
// still fail to parse where it is spliced. `[]func(){}` and `[]struct{}{}` both
// put a brace-delimited type immediately before the literal's own braces, and a
// composite literal of a channel type puts an arrow there. The instrumenter
// splices the replacement into the return position of a guard, so this checks
// it exactly there -- and by type-checking rather than only parsing, so a
// spelling that parses as something *else* is caught too.
func TestEveryRenderedEmptyValueIsLegalGo(t *testing.T) {
	t.Parallel()

	for _, c := range spellingCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			var b strings.Builder
			b.WriteString("package pkg\n\n")
			if c.imports != "" {
				b.WriteString(c.imports + "\n\n")
			}
			if c.decl != "" {
				b.WriteString(c.decl + "\n\n")
			}
			b.WriteString("func Value() " + c.result + " {\n\treturn " + c.want + "\n}\n")
			typeCheck(t, b.String())
		})
	}
}

// TestTheEmptyValueOfAGenericSliceIsSpelledInItsOwnScope is the one shape the
// table above cannot hold, because the type only exists inside the function
// that declares the parameter. The spelling has to be the parameter's own
// name -- which is in scope exactly where the edit goes, and nowhere else.
func TestTheEmptyValueOfAGenericSliceIsSpelledInItsOwnScope(t *testing.T) {
	t.Parallel()

	source := `package pkg

func Values[E comparable](in []E) []E {
	return in
}
`
	got := scanSource(t, source)
	if !got.has("return-empty-slice", "in", "[]E{}") {
		t.Fatalf("scan found %v, want return-empty-slice in->[]E{}", got.rules())
	}
	typeCheck(t, `package pkg

func Values[E comparable](in []E) []E {
	return []E{}
}
`)
}

// TestTheTwoEmptyInterfacesRenderDifferently pins the sharpest edge in the
// table, because it is the one where two spellings of the *same type* produce
// two different mutant identities.
//
// `any` is an alias for `interface{}`, so a function declared to return `[]any`
// and one declared to return `[]interface{}` have identical types -- and
// go/types prints them differently anyway, because the alias is preserved and
// the printer honours it. Both are legal Go and the choice is the source's, so
// following the source is right. What would be wrong is for it to change
// without anyone deciding: the printer collapsing the two spellings would
// reissue every mutant in one of these two shapes.
func TestTheTwoEmptyInterfacesRenderDifferently(t *testing.T) {
	t.Parallel()

	aliased := scanSource(t, `package pkg

func Values(in []any) []any {
	return in
}
`)
	written := scanSource(t, `package pkg

func Values(in []interface{}) []interface{} {
	return in
}
`)
	if !aliased.has("return-empty-slice", "in", "[]any{}") {
		t.Errorf("[]any scanned as %v", aliased.rules())
	}
	if !written.has("return-empty-slice", "in", "[]interface{}{}") {
		t.Errorf("[]interface{} scanned as %v", written.rules())
	}
}

// typeCheck fails the test unless the source compiles as far as go/types can
// tell, which for a splice of this kind is as far as it needs to.
func typeCheck(t *testing.T, src string) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "spliced.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("the replacement does not parse where it is spliced:\n%s\n%v", src, err)
	}
	var errs []error
	conf := types.Config{
		Importer: importer.ForCompiler(fset, "source", nil),
		Error:    func(e error) { errs = append(errs, e) },
	}
	if _, _ = conf.Check("example.com/m/pkg", fset, []*ast.File{file}, nil); len(errs) > 0 {
		t.Fatalf("the replacement does not type-check where it is spliced:\n%s\n%v", src, errs)
	}
}
