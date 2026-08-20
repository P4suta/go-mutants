// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
)

// fuzzSeeds are inputs worth keeping in the corpus beyond the table cases:
// shapes found by earlier fuzzing runs, and the ones whose flattening depends
// on a rule that is easy to get subtly wrong. They live here rather than in
// testdata/ so that the corpus is reviewable in the same place as the rules it
// exercises, and so that `go test` without -fuzz still runs every one of them.
var fuzzSeeds = []string{
	"",
	" ",
	";",
	"a",
	"a+b",
	"a /* c */ b",
	"f(\n\ta,\n)",
	"func() int {\n\treturn 1\n}()",
	"`raw\nstring`",
	"`\r`",
	"`a\r\nb`",
	"\"interpreted\\n\"",
	"a.\n\tb",
	"x <- -1",
	"1.0+.5",
	"a[1:\n2]",
	"[]T{\n\t{1},\n}",
	"a /* multi\nline */ + b",
	"a // line\n+ b",
	"日本語 + `語\n本`",
	"'\\n'",
	"\"a\rb\"",
	"'\r'",
	"f(\"a\rb\", '\r')",
	"0x1f . b",
	"1i . b",
	"x := 1\ny++\nz\n+ w",
	"^&^x",
	"a&^b",
	"struct{}{}",
	"map[string]struct{}{\n\t`k`: {},\n}",
	"func() { for {\n} }",
	"interface{ M() }(nil)",
	"1 . b",
	"(((a)))",
}

// FuzzFlatten drives the flattener with arbitrary bytes and asserts the whole
// contract for every input that is a Go fragment at all: one line out, output
// that re-parses, a syntax tree that did not change, and a result that does not
// move under a second pass.
//
// Inputs that do not parse are skipped rather than failing. Flatten works at
// the token level and accepts fragments go/parser would not, but the meaning
// of "the meaning did not change" is only defined where there is a tree to
// compare, and a fuzzer left to assert on unparsable input would only be
// testing the harness.
func FuzzFlatten(f *testing.F) {
	for _, tc := range flattenCases {
		f.Add(tc.src)
	}
	for _, seed := range fuzzSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, src string) {
		// Long inputs cost parser time without reaching new rules; the shapes
		// that matter are small.
		if len(src) > 2048 {
			t.Skip()
		}

		var before ast.Node
		kind := kindExpr
		if e, err := parser.ParseExpr(src); err == nil {
			before = e
		} else if b, err := parseFragment(src, kindStmts); err == nil {
			before, kind = b, kindStmts
		} else {
			t.Skip()
		}

		flat, err := instrument.Flatten([]byte(src))
		if err != nil {
			t.Fatalf("Flatten(%q) = error %v", src, err)
		}
		if i := bytes.IndexAny(flat, "\n\r"); i >= 0 {
			t.Fatalf("Flatten(%q) = %q, which is not one line (byte %d)", src, flat, i)
		}

		after, err := parseFragment(string(flat), kind)
		if err != nil {
			t.Fatalf("flattened %q does not parse as a %s: %v\nsource: %q", flat, kind, err, src)
		}
		if d := astDiff(before, after); d != "" {
			t.Fatalf("flattening changed the tree at %s\nsource:    %q\nflattened: %q", d, src, flat)
		}

		again, err := instrument.Flatten(flat)
		if err != nil {
			t.Fatalf("Flatten(Flatten(%q)) = error %v", src, err)
		}
		if !bytes.Equal(flat, again) {
			t.Fatalf("Flatten is not idempotent\nsource: %q\nonce:   %q\ntwice:  %q", src, flat, again)
		}
	})
}
