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

// What happens when the bytes a scan was handed are not the bytes it parsed.
//
// A span is an offset and a length into a file, and everything downstream --
// the identity, the rewrite, the diff a report renders -- reads the file at
// that offset. So the walk checks, for every candidate, that the bytes the span
// covers are the text the rule said it was replacing, and refuses the whole
// file when they are not. It is a check on go-mutants rather than on the user:
// a file that changed between the read and the parse is a race this tool must
// not silently mutate through, and an offset arithmetic bug would otherwise
// produce a catalogue of spans pointing at the wrong bytes.
//
// It is also the only failure the walk itself can produce, which makes it the
// only way to watch an error travel out of the walk at all -- through the
// inspection that stops at the first one, and through every emitter between it
// and the top.

// scanBytes runs the mutation walk over a parsed file while telling it the file
// holds different bytes.
//
// Everything is real but the source: the tree is parsed and type-checked from
// `parsed`, and the walk is handed `bytes` as what the file contains. That is
// exactly the shape of a file that changed underneath a run.
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

// spanFixture holds a candidate of a rule whose original text is more than one
// byte, so that a span past the end of the file and a span over the wrong bytes
// are both reachable from it.
const spanFixture = `package pkg

// Widest returns the larger of two numbers.
func Widest(a, b int) int {
	if a <= b {
		return b
	}
	return a
}
`

// TestAScanRefusesAFileWhoseBytesAreNotTheOnesItParsed is the check, both ways
// it can fail.
//
// Truncation and substitution are different discoveries about the same
// situation, and the messages say so: one span reaches past the end of a file
// that got shorter, and another lands inside a file that is the same length and
// holds something else. The second is the one a length check alone would miss.
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

		// The same number of bytes, so no span reaches past the end; what fails
		// is the comparison of the covered text against the rule's original.
		swapped := strings.Replace(spanFixture, "a <= b", "a >= b", 1)
		if len(swapped) != len(spanFixture) {
			t.Fatalf("the substitution changed the length, which is not the case under test")
		}
		err := scanBytes(t, spanFixture, []byte(swapped))
		requireSpanMismatch(t, err, "the file holds")
	})

}

// requireSpanMismatch asserts that an error is the walk's refusal, and says so.
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
