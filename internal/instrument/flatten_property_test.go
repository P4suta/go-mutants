// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"go/scanner"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/P4suta/go-mutants/internal/instrument"
)

type sepKind int

const (
	sepInline sepKind = iota
	sepBreakOK
	sepStmtEnd
)

type piece struct {
	text string
	sep  sepKind
}

func lit(text string) piece       { return piece{text: text, sep: sepInline} }
func breakable(text string) piece { return piece{text: text, sep: sepBreakOK} }

func endStmt() piece { return piece{sep: sepStmtEnd} }

var (
	inlineSeps = []string{" ", " /* c */ ", "\t"}
	breakSeps  = []string{
		" ", "\n", "\n\t\t", "\r\n", " // comment\n",
		" /* inline */ ", " /* multi\nline */ ", "\n\n",
	}
	stmtSeps = []string{"\n", "\n\t", "\r\n", " // comment\n", " /* multi\nline */ ", "\n\n", "; ", ";\n"}
)

var nastyValues = []string{
	"",
	"a",
	"a\nb",
	"a\r\nb",
	"\r",
	"\n",
	"tab\there",
	`a\nb`,
	`quote"inside`,
	"日本\n語",
	"line1\nline2\n",
	"back\\slash\nand\ttab",
}

var crLiterals = []string{
	"\"a\rb\"",
	"\"\r\"",
	"\"a\r\\nb\"",
	"'\r'",
	"\"日本\r語\"",
}

func genLeaf(t *rapid.T) []piece {
	switch rapid.IntRange(0, 6).Draw(t, "leaf") {
	case 0:
		return []piece{lit(rapid.SampledFrom([]string{"a", "b", "c", "x", "_v", "日本", "αβ", "len"}).Draw(t, "ident"))}
	case 1:
		return []piece{lit(rapid.SampledFrom([]string{"0", "1", "42", "0x1f", "1_000", "0b101"}).Draw(t, "int"))}
	case 2:
		return []piece{lit(rapid.SampledFrom([]string{"1.0", ".5", "1e9", "0.25", "1i"}).Draw(t, "float"))}
	case 3:
		return []piece{lit(strconv.Quote(rapid.SampledFrom(nastyValues).Draw(t, "string")))}
	case 4:
		v := rapid.SampledFrom(nastyValues).Draw(t, "raw")
		return []piece{lit("`" + strings.ReplaceAll(v, "`", "'") + "`")}
	case 5:
		return []piece{lit(rapid.SampledFrom(crLiterals).Draw(t, "cr literal"))}
	default:
		return []piece{lit(rapid.SampledFrom([]string{"'a'", `'\n'`, `'\''`, "'×'", `'\\'`}).Draw(t, "rune"))}
	}
}

func genOperand(t *rapid.T, depth int) []piece {
	if depth <= 0 {
		return []piece{lit(rapid.SampledFrom([]string{"a", "b", "f", "obj"}).Draw(t, "base"))}
	}
	switch rapid.IntRange(0, 3).Draw(t, "operand") {
	case 0:
		return []piece{lit(rapid.SampledFrom([]string{"a", "b", "f", "obj"}).Draw(t, "base"))}
	case 1:
		out := append(genOperand(t, depth-1), breakable("("))
		n := rapid.IntRange(0, 3).Draw(t, "args")
		for i := range n {
			out = append(out, genExpr(t, depth-1)...)
			if i < n-1 || rapid.Bool().Draw(t, "trailing comma") {
				out = append(out, breakable(","))
			}
		}
		return append(out, lit(")"))
	case 2:
		out := append(genOperand(t, depth-1), breakable("."))
		return append(out, lit(rapid.SampledFrom([]string{"Field", "m", "日本"}).Draw(t, "sel")))
	default:
		out := append(genOperand(t, depth-1), breakable("["))
		out = append(out, genExpr(t, depth-1)...)
		return append(out, lit("]"))
	}
}

func genExpr(t *rapid.T, depth int) []piece {
	if depth <= 0 {
		return genLeaf(t)
	}
	switch rapid.IntRange(0, 6).Draw(t, "expr") {
	case 0:
		return genLeaf(t)
	case 1:
		return genOperand(t, depth-1)
	case 2:
		op := rapid.SampledFrom([]string{
			"+", "-", "*", "/", "%", "&&", "||", "==", "!=", "<", "<=", ">", ">=",
			"&", "|", "^", "<<", ">>", "&^",
		}).Draw(t, "binop")
		out := genExpr(t, depth-1)
		out = append(out, breakable(op))
		return append(out, genExpr(t, depth-1)...)
	case 3:
		op := rapid.SampledFrom([]string{"-", "!", "^", "+"}).Draw(t, "unop")
		return append([]piece{breakable(op)}, genExpr(t, depth-1)...)
	case 4:
		out := []piece{breakable("(")}
		out = append(out, genExpr(t, depth-1)...)
		return append(out, lit(")"))
	case 5:
		typ := rapid.SampledFrom([]string{"T", "[]int", "map[string]int", "[2]T", "pkg.T"}).Draw(t, "littype")
		out := []piece{lit(typ), breakable("{")}
		n := rapid.IntRange(0, 3).Draw(t, "elems")
		for range n {
			if rapid.Bool().Draw(t, "keyed") {
				out = append(out, genLeaf(t)...)
				out = append(out, breakable(":"))
			}
			out = append(out, genExpr(t, depth-1)...)
			out = append(out, breakable(","))
		}
		return append(out, lit("}"))
	default:
		out := []piece{lit("func"), lit("("), lit(")"), lit("int"), breakable("{")}
		out = append(out, lit("return"))
		out = append(out, genExpr(t, depth-1)...)
		out = append(out, breakable(""))
		return append(out, lit("}"), lit("("), lit(")"))
	}
}

func genCond(t *rapid.T, depth int) []piece {
	left := genOperand(t, depth)
	if !rapid.Bool().Draw(t, "compare") {
		return left
	}
	op := rapid.SampledFrom([]string{"==", "!=", "<", "<=", ">", ">=", "&&", "||"}).Draw(t, "cmp")
	return append(append(left, breakable(op)), genOperand(t, depth)...)
}

func genTarget(t *rapid.T, depth int) []piece {
	switch rapid.IntRange(0, 2).Draw(t, "target") {
	case 0:
		return []piece{lit(rapid.SampledFrom([]string{"x", "y", "n"}).Draw(t, "var"))}
	case 1:
		return []piece{lit("obj"), breakable("."), lit("Field")}
	default:
		out := []piece{lit("a"), breakable("[")}
		out = append(out, genExpr(t, depth)...)
		return append(out, lit("]"))
	}
}

func genCall(t *rapid.T, depth int) []piece {
	out := []piece{lit(rapid.SampledFrom([]string{"f", "g", "obj.m"}).Draw(t, "fn")), breakable("(")}
	n := rapid.IntRange(0, 2).Draw(t, "args")
	for i := range n {
		out = append(out, genExpr(t, depth)...)
		if i < n-1 || rapid.Bool().Draw(t, "trailing comma") {
			out = append(out, breakable(","))
		}
	}
	return append(out, lit(")"))
}

func genStmt(t *rapid.T, depth int) []piece {
	last := 3
	if depth > 0 {
		last = 6
	}
	switch rapid.IntRange(0, last).Draw(t, "stmt") {
	case 0:
		return genCall(t, depth)

	case 1:
		out := genTarget(t, depth)
		return append(out, lit(rapid.SampledFrom([]string{"++", "--"}).Draw(t, "incdec")))

	case 2:
		if rapid.Bool().Draw(t, "declare") {
			out := []piece{lit(rapid.SampledFrom([]string{"v", "w"}).Draw(t, "newvar")), breakable(":=")}
			return append(out, genExpr(t, depth)...)
		}
		out := genTarget(t, depth)
		out = append(out, breakable(rapid.SampledFrom([]string{"=", "+=", "-=", "*="}).Draw(t, "assign")))
		return append(out, genExpr(t, depth)...)

	case 3:
		out := genExpr(t, depth)
		out = append(out, breakable(""))
		out = append(out, lit(rapid.SampledFrom([]string{"+", "-", "^"}).Draw(t, "split")))
		return append(out, genExpr(t, depth)...)

	case 4:
		out := []piece{breakable("return")}
		if rapid.Bool().Draw(t, "value") {
			out = append(out, genExpr(t, depth-1)...)
		}
		return out

	case 5:
		out := []piece{lit("if")}
		out = append(out, genCond(t, depth-1)...)
		out = append(out, breakable("{"))
		out = append(out, genStmts(t, depth-1)...)
		out = append(out, breakable(""), lit("}"))
		if rapid.Bool().Draw(t, "else") {
			out = append(out, lit("else"), breakable("{"))
			out = append(out, genStmts(t, depth-1)...)
			out = append(out, breakable(""), lit("}"))
		}
		return out

	default:
		out := []piece{lit("for")}
		if rapid.Bool().Draw(t, "three clause") {
			out = append(out,
				lit("i"), breakable(":="), lit("0"), lit(";"),
				lit("i"), breakable("<"), lit("n"), lit(";"),
				lit("i"), lit("++"))
		} else if rapid.Bool().Draw(t, "conditional") {
			out = append(out, genCond(t, depth-1)...)
		}
		out = append(out, breakable("{"))
		out = append(out, genStmts(t, depth-1)...)
		return append(out, breakable(""), lit("}"))
	}
}

func genStmts(t *rapid.T, depth int) []piece {
	n := rapid.IntRange(1, 3).Draw(t, "stmts")
	var out []piece
	for i := range n {
		if i > 0 {
			out = append(out, endStmt())
		}
		out = append(out, genStmt(t, depth)...)
	}
	return out
}

func render(t *rapid.T, pieces []piece) string {
	var b strings.Builder
	for i, p := range pieces {
		b.WriteString(p.text)
		if i == len(pieces)-1 {
			break
		}
		switch p.sep {
		case sepBreakOK:
			b.WriteString(rapid.SampledFrom(breakSeps).Draw(t, "sep"))
		case sepStmtEnd:
			b.WriteString(rapid.SampledFrom(stmtSeps).Draw(t, "terminator"))
		case sepInline:
			b.WriteString(rapid.SampledFrom(inlineSeps).Draw(t, "gap"))
		}
	}
	return b.String()
}

type fragment struct {
	src  string
	kind fragmentKind
}

func fragmentGen() *rapid.Generator[fragment] {
	return rapid.Custom(func(t *rapid.T) fragment {
		depth := rapid.IntRange(0, 4).Draw(t, "depth")
		if rapid.Bool().Draw(t, "statements") {
			return fragment{src: render(t, genStmts(t, depth)), kind: kindStmts}
		}
		return fragment{src: render(t, genExpr(t, depth)), kind: kindExpr}
	})
}

type scannedToken struct {
	tok token.Token
	lit string
}

func scanAll(src string) ([]scannedToken, bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	ok := true
	s.Init(file, []byte(src), func(token.Position, string) { ok = false }, scanner.ScanComments)

	var out []scannedToken
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return out, ok
		}
		out = append(out, scannedToken{tok: tok, lit: lit})
	}
}

func insertedSemicolon(t scannedToken) bool {
	return t.tok == token.SEMICOLON && t.lit != ";"
}

func withoutInsertedSemicolons(tokens []scannedToken) (string, bool) {
	var parts []string
	dropped := false
	for _, t := range tokens {
		switch {
		case t.tok == token.COMMENT:
		case insertedSemicolon(t):
			dropped = true
		case t.tok.IsLiteral():
			parts = append(parts, t.lit)
		default:
			parts = append(parts, t.tok.String())
		}
	}
	return strings.Join(parts, " "), dropped
}

func insertedSemicolonsMatter(src string, kind fragmentKind) bool {
	tokens, ok := scanAll(src)
	if !ok {
		return false
	}
	stripped, dropped := withoutInsertedSemicolons(tokens)
	if !dropped {
		return false
	}
	before, err := parseFragment(src, kind)
	if err != nil {
		return false
	}
	after, err := parseFragment(stripped, kind)
	if err != nil {
		return true
	}
	return astDiff(before, after) != ""
}

func rawStringSpansLines(tokens []scannedToken) bool {
	for _, t := range tokens {
		if t.tok == token.STRING && strings.HasPrefix(t.lit, "`") && strings.ContainsAny(t.lit, "\n\r") {
			return true
		}
	}
	return false
}

func literalHoldsCarriageReturn(tokens []scannedToken) bool {
	for _, t := range tokens {
		if t.tok != token.STRING && t.tok != token.CHAR {
			continue
		}
		if !strings.HasPrefix(t.lit, "`") && strings.Contains(t.lit, "\r") {
			return true
		}
	}
	return false
}

func TestGeneratorProducesHardInput(t *testing.T) {
	t.Parallel()

	const samples = 300
	var multiline, rawWithBreak, crLiteral, comments, semicolons, stmtLists int
	gen := fragmentGen()
	for i := range samples {
		f := gen.Example(i)
		tokens, ok := scanAll(f.src)
		if !ok {
			t.Fatalf("generated source does not tokenize: %q", f.src)
		}
		if f.kind == kindStmts {
			stmtLists++
		}
		if strings.ContainsAny(f.src, "\n\r") {
			multiline++
		}
		if strings.Contains(f.src, "//") || strings.Contains(f.src, "/*") {
			comments++
		}
		if rawStringSpansLines(tokens) {
			rawWithBreak++
		}
		if literalHoldsCarriageReturn(tokens) {
			crLiteral++
		}
		if insertedSemicolonsMatter(f.src, f.kind) {
			semicolons++
		}
	}

	const floor = samples / 20
	for _, c := range []struct {
		what string
		n    int
	}{
		{"statement lists", stmtLists},
		{"multi-line sources", multiline},
		{"sources with comments", comments},
		{"raw strings containing a line break", rawWithBreak},
		{"literals holding a raw carriage return", crLiteral},
		{"sources whose inserted semicolons carry meaning", semicolons},
	} {
		t.Logf("%3d/%d samples were %s", c.n, samples, c.what)
		if c.n < floor {
			t.Errorf("only %d of %d samples were %s, want at least %d", c.n, samples, c.what, floor)
		}
	}
}

func TestCensusDiscriminates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		kind fragmentKind
		want bool
	}{
		{name: "function literal semicolon is redundant", src: "func() int {\n\treturn a\n}()", kind: kindExpr},
		{name: "function literal with a sum is redundant too", src: "func() int {\n\treturn a + b\n}()", kind: kindExpr},
		{name: "single-line expression inserts nothing", src: "a + b", kind: kindExpr},
		{name: "expression broken after an operator inserts nothing", src: "a +\n\tb", kind: kindExpr},
		{name: "operator at a line start splits a statement", src: "a\n+ b", kind: kindStmts, want: true},
		{name: "two call statements", src: "f()\ng()", kind: kindStmts, want: true},
		{name: "increment then a statement", src: "x++\nf()", kind: kindStmts, want: true},
		{name: "return then an expression", src: "return\nx", kind: kindStmts, want: true},
		{name: "explicit semicolons are not the scanner's", src: "f(); g()", kind: kindStmts},
		{name: "block comment carries the break", src: "f() /* one\ntwo */ g()", kind: kindStmts, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := insertedSemicolonsMatter(tc.src, tc.kind); got != tc.want {
				t.Errorf("insertedSemicolonsMatter(%q) = %v, want %v", tc.src, got, tc.want)
			}
		})
	}
}

func TestFlattenPreservesMeaning(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		f := fragmentGen().Draw(rt, "fragment")

		before, err := parseFragment(f.src, f.kind)
		if err != nil {
			rt.Fatalf("generated source does not parse as a %s: %v\nsource: %q", f.kind, err, f.src)
		}

		flat, err := instrument.Flatten([]byte(f.src))
		if err != nil {
			rt.Fatalf("Flatten(%q) = error %v", f.src, err)
		}
		if i := bytes.IndexAny(flat, "\n\r"); i >= 0 {
			rt.Fatalf("Flatten(%q) = %q, which is not one line (byte %d)", f.src, flat, i)
		}
		after, err := parseFragment(string(flat), f.kind)
		if err != nil {
			rt.Fatalf("flattened %q does not parse as a %s: %v\nsource: %q", flat, f.kind, err, f.src)
		}
		if d := astDiff(before, after); d != "" {
			rt.Fatalf("flattening changed the tree at %s\nsource:    %q\nflattened: %q", d, f.src, flat)
		}

		again, err := instrument.Flatten(flat)
		if err != nil {
			rt.Fatalf("Flatten(Flatten(%q)) = error %v", f.src, err)
		}
		if !bytes.Equal(flat, again) {
			rt.Fatalf("Flatten is not idempotent\nsource: %q\nonce:   %q\ntwice:  %q", f.src, flat, again)
		}
	})
}
