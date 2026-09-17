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

type spellingCase struct {
	name    string
	imports string
	decl    string
	result  string
	want    string
	mapped  bool
}

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

func (c spellingCase) rule() string {
	if c.mapped {
		return "return-empty-map"
	}
	return "return-empty-slice"
}

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
