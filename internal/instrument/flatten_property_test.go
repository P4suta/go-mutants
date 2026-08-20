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

// This file generates the multi-line Go the flattener exists for.
//
// Rendering a generated AST with go/printer would not do: go/printer decides
// its own line breaks, and for a small expression it decides on none, so the
// property would be tested almost entirely against single-line input. What
// matters is where the breaks fall, so the generator emits tokens together
// with the positions at which a line break keeps the source valid Go, and the
// renderer chooses among them — along with the comments and the whitespace
// that make a real source file awkward.
//
// Fragments come in both of the shapes discovery hands to the instrumenter: a
// single expression, and a list of statements. The second is not a variation on
// the first. Automatic semicolon insertion only ever decides anything at a
// statement boundary, and inside one expression there is no boundary for it to
// decide: the semicolon Go inserts before the "}" of `func() int { return a }`
// is redundant, so a flattener that got it wrong there would still produce a
// tree that compares equal. Statement lists are where an inserted semicolon
// holds two statements apart, and dropping one fuses them into a different
// program. TestGeneratorProducesHardInput measures that difference rather than
// assuming it.

// A sepKind says what may be written after a piece.
type sepKind int

const (
	// sepInline allows whitespace and a one-line comment and nothing else. A
	// line break here would either split an expression down the middle or be
	// rejected outright.
	sepInline sepKind = iota
	// sepBreakOK allows a line break as well. Whether the scanner then inserts
	// a semicolon is its business — and, where it does, the point.
	sepBreakOK
	// sepStmtEnd marks a place the fragment needs a statement terminator: a
	// line break, or the ";" an author would otherwise have to write.
	sepStmtEnd
)

// A piece is one rendered token, plus what may follow it.
type piece struct {
	text string
	sep  sepKind
}

func lit(text string) piece       { return piece{text: text, sep: sepInline} }
func breakable(text string) piece { return piece{text: text, sep: sepBreakOK} }

// endStmt is a zero-width piece that ends a statement. It carries no text
// because the terminator is whatever the renderer draws — usually a line break,
// which is what puts the semicolon in the scanner's hands rather than the
// generator's.
func endStmt() piece { return piece{sep: sepStmtEnd} }

var (
	inlineSeps = []string{" ", " /* c */ ", "\t"}
	breakSeps  = []string{
		" ", "\n", "\n\t\t", "\r\n", " // comment\n",
		" /* inline */ ", " /* multi\nline */ ", "\n\n",
	}
	// Every one of these ends a statement. The comment forms do it too: a
	// general comment containing a line break counts as one for insertion
	// purposes, which is why dropping such a comment must never drop the
	// semicolon that came with it.
	stmtSeps = []string{"\n", "\n\t", "\r\n", " // comment\n", " /* multi\nline */ ", "\n\n", "; ", ";\n"}
)

// nastyValues are the string contents worth putting inside a literal: the ones
// whose flattening is a rewrite rather than a copy.
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

// crLiterals are literals holding a raw carriage return, written out rather
// than produced by strconv.Quote — which would escape it, and so generate the
// easy case instead of the hard one. Each is valid Go: only a newline
// terminates an interpreted string or a rune literal, so a carriage return sits
// inside one untouched, and reaches the flattener as a line break it cannot
// copy through.
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
		// A raw literal cannot contain a backquote; everything else is fair
		// game, and the line breaks in these are what force the rewrite.
		v := rapid.SampledFrom(nastyValues).Draw(t, "raw")
		return []piece{lit("`" + strings.ReplaceAll(v, "`", "'") + "`")}
	case 5:
		return []piece{lit(rapid.SampledFrom(crLiterals).Draw(t, "cr literal"))}
	default:
		return []piece{lit(rapid.SampledFrom([]string{"'a'", `'\n'`, `'\''`, "'×'", `'\\'`}).Draw(t, "rune"))}
	}
}

// genOperand generates something a call, selector or index can be applied to,
// keeping to the shapes Go actually allows in that position.
func genOperand(t *rapid.T, depth int) []piece {
	if depth <= 0 {
		return []piece{lit(rapid.SampledFrom([]string{"a", "b", "f", "obj"}).Draw(t, "base"))}
	}
	switch rapid.IntRange(0, 3).Draw(t, "operand") {
	case 0:
		return []piece{lit(rapid.SampledFrom([]string{"a", "b", "f", "obj"}).Draw(t, "base"))}
	case 1: // call
		out := append(genOperand(t, depth-1), breakable("("))
		n := rapid.IntRange(0, 3).Draw(t, "args")
		for i := range n {
			out = append(out, genExpr(t, depth-1)...)
			if i < n-1 || rapid.Bool().Draw(t, "trailing comma") {
				out = append(out, breakable(","))
			}
		}
		return append(out, lit(")"))
	case 2: // selector
		out := append(genOperand(t, depth-1), breakable("."))
		return append(out, lit(rapid.SampledFrom([]string{"Field", "m", "日本"}).Draw(t, "sel")))
	default: // index
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
	case 2: // binary
		op := rapid.SampledFrom([]string{
			"+", "-", "*", "/", "%", "&&", "||", "==", "!=", "<", "<=", ">", ">=",
			"&", "|", "^", "<<", ">>", "&^",
		}).Draw(t, "binop")
		out := genExpr(t, depth-1)
		out = append(out, breakable(op))
		return append(out, genExpr(t, depth-1)...)
	case 3: // unary
		op := rapid.SampledFrom([]string{"-", "!", "^", "+"}).Draw(t, "unop")
		return append([]piece{breakable(op)}, genExpr(t, depth-1)...)
	case 4: // parenthesised
		out := []piece{breakable("(")}
		out = append(out, genExpr(t, depth-1)...)
		return append(out, lit(")"))
	case 5: // composite literal
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
	default: // function literal, the one expression shape with a statement inside it
		out := []piece{lit("func"), lit("("), lit(")"), lit("int"), breakable("{")}
		out = append(out, lit("return"))
		out = append(out, genExpr(t, depth-1)...)
		out = append(out, breakable(""))
		return append(out, lit("}"), lit("("), lit(")"))
	}
}

// genCond generates an expression for the header of an `if` or a `for`.
//
// Composite literals are excluded on purpose rather than by accident: go/parser
// refuses a bare `T{...}` at the top level of a control clause, because the
// brace is already spoken for by the block. The restriction lifts inside
// parentheses and brackets, so the call arguments and index expressions
// genOperand produces may still hold one.
func genCond(t *rapid.T, depth int) []piece {
	left := genOperand(t, depth)
	if !rapid.Bool().Draw(t, "compare") {
		return left
	}
	op := rapid.SampledFrom([]string{"==", "!=", "<", "<=", ">", ">=", "&&", "||"}).Draw(t, "cmp")
	return append(append(left, breakable(op)), genOperand(t, depth)...)
}

// genTarget generates something that can be assigned to or incremented, which
// a call — genOperand's other shape — cannot be.
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

// genStmt draws one statement, favouring the shapes whose line breaks carry a
// semicolon: a break after an operand or a "++", and a break in front of an
// operator that can also be unary.
func genStmt(t *rapid.T, depth int) []piece {
	last := 3
	if depth > 0 {
		last = 6
	}
	switch rapid.IntRange(0, last).Draw(t, "stmt") {
	case 0: // call statement
		return genCall(t, depth)

	case 1: // increment or decrement, after which a line break ends the statement
		out := genTarget(t, depth)
		return append(out, lit(rapid.SampledFrom([]string{"++", "--"}).Draw(t, "incdec")))

	case 2: // assignment
		if rapid.Bool().Draw(t, "declare") {
			// ":=" wants plain identifiers on its left; anything else is a
			// parse error rather than the type error it looks like.
			out := []piece{lit(rapid.SampledFrom([]string{"v", "w"}).Draw(t, "newvar")), breakable(":=")}
			return append(out, genExpr(t, depth)...)
		}
		out := genTarget(t, depth)
		out = append(out, breakable(rapid.SampledFrom([]string{"=", "+=", "-=", "*="}).Draw(t, "assign")))
		return append(out, genExpr(t, depth)...)

	case 3:
		// An operator at the start of the next line. Go ends the statement at
		// the break, so "a\n+ b" is two statements and "a + b" is one sum, and
		// the semicolon the scanner inserts is the entire difference between
		// them. Only operators that are also unary can open a statement.
		out := genExpr(t, depth)
		out = append(out, breakable(""))
		out = append(out, lit(rapid.SampledFrom([]string{"+", "-", "^"}).Draw(t, "split")))
		return append(out, genExpr(t, depth)...)

	case 4:
		// "return" ends a statement all by itself, so a break after it makes
		// the expression that follows a statement of its own.
		out := []piece{breakable("return")}
		if rapid.Bool().Draw(t, "value") {
			out = append(out, genExpr(t, depth-1)...)
		}
		return out

	case 5: // if, optionally with an else block
		out := []piece{lit("if")}
		out = append(out, genCond(t, depth-1)...)
		out = append(out, breakable("{"))
		out = append(out, genStmts(t, depth-1)...)
		// A break in front of "}" is fine; one in front of "else" is not, since
		// the semicolon it inserts would close the if statement.
		out = append(out, breakable(""), lit("}"))
		if rapid.Bool().Draw(t, "else") {
			out = append(out, lit("else"), breakable("{"))
			out = append(out, genStmts(t, depth-1)...)
			out = append(out, breakable(""), lit("}"))
		}
		return out

	default: // for, in its condition-only and three-clause forms
		out := []piece{lit("for")}
		if rapid.Bool().Draw(t, "three clause") {
			// The semicolons here are the author's, not the scanner's: a
			// flattener that dropped them would break the loop outright.
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

// render joins the pieces with whitespace and comments, breaking lines only
// where the generator said a break is legal and always where it said one is
// needed.
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
		default:
			b.WriteString(rapid.SampledFrom(inlineSeps).Draw(t, "gap"))
		}
	}
	return b.String()
}

// A fragment is a generated source together with the way it has to be parsed.
// Comparing an expression tree against a statement tree would report a
// difference that is an artefact of the harness.
type fragment struct {
	src  string
	kind fragmentKind
}

// fragmentGen draws a whole fragment: a random expression or statement list
// laid out across random line breaks.
func fragmentGen() *rapid.Generator[fragment] {
	return rapid.Custom(func(t *rapid.T) fragment {
		depth := rapid.IntRange(0, 4).Draw(t, "depth")
		if rapid.Bool().Draw(t, "statements") {
			return fragment{src: render(t, genStmts(t, depth)), kind: kindStmts}
		}
		return fragment{src: render(t, genExpr(t, depth)), kind: kindExpr}
	})
}

// A scannedToken is one token of a generated source, as the census reads it
// back. The census works from tokens rather than from substrings because every
// interesting question about a fragment — is this "\r" a line ending or the
// contents of a literal, is this ";" the author's or the scanner's — is a
// question about tokens that a substring search answers wrongly.
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

// insertedSemicolon reports whether t is a semicolon the scanner supplied at a
// line break rather than one the author wrote. go/scanner spells the two
// differently: an inserted one carries the line break it stood for as its
// literal, an explicit one carries ";".
func insertedSemicolon(t scannedToken) bool {
	return t.tok == token.SEMICOLON && t.lit != ";"
}

// withoutInsertedSemicolons renders src's tokens on one line with exactly the
// semicolons the scanner inserted left out, and reports whether there were any
// to leave out.
//
// It strips from the source rather than from Flatten's output, which is the
// same thing done in an easier place: Flatten preserves token order, so the two
// streams line up index for index and stripping either one removes the same
// semicolons. Doing it here needs no alignment bookkeeping to get right.
// Comments go too, since they carry no meaning to a parser, and tokens are
// joined by single spaces because a space between two tokens is always safe —
// it is only the absence of one that can fuse them.
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

// insertedSemicolonsMatter reports whether the semicolons Go inserted at src's
// line breaks are load-bearing: whether a fragment written without them would
// be a different program, or no program at all.
//
// This is the question TestFlattenPreservesMeaning needs answered about its own
// input. Flatten writes an explicit ";" wherever the scanner inserted one, and
// the property test can only catch a wrong decision there if the decision shows
// up in the tree. Where it does not — `func() int { return a }` inserts a
// semicolon before its "}" that the parser would have supplied anyway — the
// property runs the code and proves nothing about it.
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
		// Without them it does not parse at all: they were holding it together.
		return true
	}
	return astDiff(before, after) != ""
}

// rawStringSpansLines reports whether src holds a raw literal containing a line
// break — the first of Flatten's two token rewrites.
func rawStringSpansLines(tokens []scannedToken) bool {
	for _, t := range tokens {
		if t.tok == token.STRING && strings.HasPrefix(t.lit, "`") && strings.ContainsAny(t.lit, "\n\r") {
			return true
		}
	}
	return false
}

// literalHoldsCarriageReturn reports whether src holds an interpreted string or
// rune literal containing a raw carriage return — the second rewrite. Only the
// scanner can tell these apart from a CRLF line ending, and it has already
// stripped the carriage returns out of any raw literal by the time it reports
// one, so a hit here is always the interpreted form.
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

// TestGeneratorProducesHardInput keeps the property below from going vacuous.
// A generator that emitted only single-line expressions would satisfy every
// assertion in TestFlattenPreservesMeaning while testing none of the work, so
// the shapes that make flattening difficult are counted explicitly.
//
// Each count is of the difficulty itself rather than of a substring that tends
// to come with it. The semicolon count in particular used to look for the word
// "return" next to a line break, which is neither necessary nor sufficient: the
// break might fall somewhere the semicolon does not matter, and it usually did.
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

// TestCensusDiscriminates guards the census's own measure.
//
// A count that said "yes" to everything would certify a generator as thoroughly
// as the substring proxy it replaced. These cases pin both answers, and the
// first pair is the reason the measure had to change: the semicolon Go inserts
// before the closing brace of a function literal is one the parser would have
// supplied anyway, so flattening cannot get it observably wrong — while the
// same break in a statement list is the whole difference between one program
// and another.
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

// TestFlattenPreservesMeaning is the load-bearing property: for a randomly
// shaped fragment written across randomly chosen line breaks, the flattened
// copy is one line, re-parses, and has the same syntax tree.
func TestFlattenPreservesMeaning(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		f := fragmentGen().Draw(rt, "fragment")

		before, err := parseFragment(f.src, f.kind)
		if err != nil {
			// The generator only emits valid fragments; anything else is a bug
			// in this file rather than in the flattener, and saying so keeps a
			// generator mistake from being reported as a flattener failure.
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
