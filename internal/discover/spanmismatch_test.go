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

func scanBytes(t *testing.T, parsed string, bytes []byte) error {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "scan.go", parsed, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing the source:\n%s\n%v", parsed, err)
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
		t.Fatalf("the fixture does not type-check:\n%s\n%v", parsed, err)
	}
	matchers, err := newMatchers(SupportedRules())
	if err != nil {
		t.Fatalf("building the matchers: %v", err)
	}
	d := &discovery{
		root:     "/module",
		matchers: matchers,
		skips:    map[skipKey]int{},
		seen:     map[string]bool{},
		digests:  map[string]string{},
	}
	return d.scanParsed("pkg/scan.go", "example.com/m/pkg", bytes, fset.File(file.Package), file, info, pkg, nil)
}

const spanFixture = `package pkg

// Widest returns the larger of two numbers.
func Widest(a, b int) int {
	if a <= b {
		return b
	}
	return a
}
`

func TestAScanRefusesAFileWhoseBytesAreNotTheOnesItParsed(t *testing.T) {
	t.Parallel()

	t.Run("the bytes it parsed are accepted", func(t *testing.T) {
		t.Parallel()

		if err := scanBytes(t, spanFixture, []byte(spanFixture)); err != nil {
			t.Fatalf("a scan of the source it parsed: %v", err)
		}
	})

	t.Run("a file that got shorter", func(t *testing.T) {
		t.Parallel()

		err := scanBytes(t, spanFixture, []byte(spanFixture[:len(spanFixture)/2]))
		requireSpanMismatch(t, err, "past the end of the file")
	})

	t.Run("a file of the same length holding something else", func(t *testing.T) {
		t.Parallel()

		swapped := strings.Replace(spanFixture, "a <= b", "a >= b", 1)
		if len(swapped) != len(spanFixture) {
			t.Fatalf("the substitution changed the length, which is not the case under test")
		}
		err := scanBytes(t, spanFixture, []byte(swapped))
		requireSpanMismatch(t, err, "the file holds")
	})

}

func requireSpanMismatch(t *testing.T, err error, saying string) {
	t.Helper()

	if err == nil {
		t.Fatal("a scan over bytes that are not the parsed ones succeeded, want a refusal")
	}
	if code := CodeOf(err); code != CodeSpanMismatch {
		t.Fatalf("CodeOf(%v) = %q, want %q", err, code, CodeSpanMismatch)
	}
	if !strings.Contains(err.Error(), saying) {
		t.Errorf("the refusal %q does not say %q", err, saying)
	}
	if !strings.Contains(err.Error(), "pkg/scan.go") {
		t.Errorf("the refusal %q does not name the file it is about", err)
	}
}
