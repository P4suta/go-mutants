// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"go/token"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
)

// flattenCases is the hand-written half of the flattener's evidence: the
// constructs that are known to be hard rather than the ones a generator
// happens to reach. Every case asserts three things — the exact output, the
// one-line postcondition, and structural equality of the parse trees — because
// the first is the change detector and the last is the correctness property.
var flattenCases = []struct {
	name string
	src  string
	want string
}{
	// --- line folding and separators ---
	{
		name: "already one line",
		src:  "a + b",
		want: "a+b",
	},
	{
		name: "multi-line call with trailing comma",
		src:  "foo(\n\ta,\n\tb,\n)",
		want: "foo(a,b,)",
	},
	{
		name: "nested multi-line call",
		src:  "outer(\n\tinner(\n\t\t1,\n\t),\n\t2,\n)",
		want: "outer(inner(1,),2,)",
	},
	{
		name: "nested composite literals",
		src:  "[]T{\n\t{A: 1},\n\t{A: 2},\n}",
		want: "[]T{{A:1},{A:2},}",
	},
	{
		name: "selector chain split across lines",
		src:  "x.\n\ty().\n\tz()",
		want: "x.y().z()",
	},
	{
		name: "binary operators at line ends",
		src:  "a +\n\tb*\n\tc",
		want: "a+b*c",
	},
	{
		// The mirror of the case above, and the reason inserted semicolons
		// cannot simply be dropped: Go ends the statement at the line break,
		// so this is two statements rather than one sum, and flattening it to
		// "a+b" would fuse them into a different program.
		name: "binary operator at a line start splits the statement",
		src:  "a\n+ b",
		want: "a;+b",
	},
	{
		name: "keyword needs a separator",
		src:  "func() int {\n\treturn 1\n}()",
		want: "func()int{return 1;}()",
	},
	{
		name: "send of a negated value keeps operators apart",
		src:  "ch <- -x",
		want: "ch<- -x",
	},
	{
		name: "comparison against a negative value",
		src:  "a <\n\t-b",
		want: "a< -b",
	},
	{
		name: "division followed by a comment is not a comment",
		src:  "a / /* c */ b",
		want: "a/ /* c */b",
	},
	{
		// The space before ".5" is the coarse separator rule at work: "+" and
		// "." cannot fuse into a Go token, but both are operator bytes and the
		// rule keeps every such pair apart rather than enumerating the ones
		// that would actually combine. A spare space costs nothing.
		name: "float literals do not fuse with periods",
		src:  "1.0 + .5",
		want: "1.0+ .5",
	},
	{
		// A numeric literal need not end in a digit, so the byte that meets the
		// "." says nothing about what it is joining. Written flush, "0x1f.b"
		// scans as one hexadecimal float missing its exponent rather than as a
		// selector on an integer.
		name: "hexadecimal literal keeps a following period apart",
		src:  "0x1f . b",
		want: "0x1f .b",
	},
	{
		name: "imaginary literal keeps a following period apart",
		src:  "1i . b",
		want: "1i .b",
	},
	{
		name: "slice expression split after the colon",
		src:  "a[1:\n\t2]",
		want: "a[1:2]",
	},
	{
		name: "type assertion split after the paren",
		src:  "v.(\n\tT)",
		want: "v.(T)",
	},

	// --- comments ---
	{
		name: "line comment mid-expression is dropped",
		src:  "a + // why not\n\tb",
		want: "a+b",
	},
	{
		name: "trailing line comment is dropped",
		src:  "f(a) // call it",
		want: "f(a)",
	},
	{
		name: "single-line block comment is kept",
		src:  "a /* keep me */ + b",
		want: "a/* keep me */ +b",
	},
	{
		name: "block comment between arguments is kept",
		src:  "f(a, /* c */ b)",
		want: "f(a,/* c */b)",
	},
	{
		name: "multi-line block comment is dropped but its semicolon is not",
		src:  "a /* drop\nme */ + b",
		want: "a;+b",
	},
	{
		name: "comment-only input flattens to nothing",
		src:  "// nothing here\n",
		want: "",
	},

	// --- string and rune literals ---
	{
		name: "raw string with a line break becomes interpreted",
		src:  "`a\nb`",
		want: `"a\nb"`,
	},
	{
		name: "raw string with CRLF discards the carriage return",
		src:  "`a\r\nb`",
		want: `"a\nb"`,
	},
	{
		name: "raw string with backslashes and quotes",
		src:  "`a\\b\"c\nd`",
		want: `"a\\b\"c\nd"`,
	},
	{
		name: "raw string on one line is left alone",
		src:  "`a\\b`",
		want: "`a\\b`",
	},
	{
		name: "raw string with a lone carriage return stays raw",
		src:  "`a\rb`",
		want: "`ab`",
	},
	{
		name: "interpreted string is untouched",
		src:  `"a\n\r\tb"`,
		want: `"a\n\r\tb"`,
	},
	{
		// A carriage return is the one line break go/scanner leaves sitting
		// inside an interpreted literal rather than rejecting: only a newline
		// terminates one. So this is valid, compilable Go whose bytes reach the
		// output verbatim unless the literal is re-spelled.
		name: "interpreted string with a raw carriage return is escaped",
		src:  "\"a\rb\"",
		want: `"a\rb"`,
	},
	{
		name: "rune literal holding a raw carriage return is escaped",
		src:  "'\r'",
		want: `'\r'`,
	},
	{
		name: "raw carriage returns inside a multi-line call",
		src:  "f(\n\t\"a\rb\",\n\t'\r',\n)",
		want: `f("a\rb",'\r',)`,
	},
	{
		// The literal keeps its own spelling everywhere the carriage return is
		// already escaped, so the rewrite stays the smallest one that fits the
		// literal on a line.
		name: "escaped carriage return is left as written",
		src:  "\"a\\rb\" + '\\r'",
		want: `"a\rb"+'\r'`,
	},
	{
		name: "raw string inside a multi-line composite literal",
		src:  "map[string]string{\n\t`k`: `v\nw`,\n}",
		want: "map[string]string{`k`:\"v\\nw\",}",
	},
	{
		name: "rune literal is untouched",
		src:  `'\n' + '×'`,
		want: `'\n'+'×'`,
	},

	// --- unicode ---
	{
		name: "unicode identifiers stay apart",
		src:  "日本 +\n\t語",
		want: "日本+語",
	},
	{
		name: "unicode identifier next to a keyword",
		src:  "func() rune {\n\treturn αβ\n}()",
		want: "func()rune{return αβ;}()",
	},
	{
		name: "unicode inside strings survives",
		src:  "f(\n\t\"héllo→\",\n)",
		want: `f("héllo→",)`,
	},

	// --- statements ---
	{
		name: "statement list",
		src:  "a()\nb()",
		want: "a();b()",
	},
	{
		name: "CRLF statement list",
		src:  "a()\r\nb()\r\n",
		want: "a();b()",
	},
	{
		name: "if with an initialiser",
		src:  "if err := f(); err != nil {\n\treturn err\n}",
		want: "if err:=f();err!=nil{return err;}",
	},
	{
		name: "switch with a case body",
		src:  "switch x {\ncase 1:\n\tf()\n}",
		want: "switch x{case 1:f();}",
	},
	{
		name: "for loop",
		src:  "for i := 0; i < n; i++ {\n\ts += i\n}",
		want: "for i:=0;i<n;i++{s+=i;}",
	},
	{
		// The shape the statement guard will meet most often, and the one
		// where a stray line break between "}" and "else" would turn one
		// statement into a syntax error.
		name: "else-if chain",
		src:  "if a {\n\tf()\n} else if b {\n\tg()\n} else {\n\th()\n}",
		want: "if a{f();}else if b{g();}else{h();}",
	},
	{
		name: "labelled loop",
		src:  "L:\n\tfor {\n\t}",
		want: "L:for{}",
	},
	{
		name: "deferred function literal",
		src:  "defer func() {\n\tclose(c)\n}()",
		want: "defer func(){close(c);}()",
	},
	{
		name: "return of a multi-line call",
		src:  "return f(\n\ta,\n)",
		want: "return f(a,)",
	},
	{
		name: "explicit empty statement is kept",
		src:  ";",
		want: ";",
	},
	{
		name: "explicit trailing semicolon is kept",
		src:  "a();",
		want: "a();",
	},
	{
		name: "empty input",
		src:  "",
		want: "",
	},
	{
		name: "whitespace only",
		src:  "  \n\t \r\n",
		want: "",
	},
}

func TestFlatten(t *testing.T) {
	t.Parallel()

	for _, tc := range flattenCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := instrument.Flatten([]byte(tc.src))
			if err != nil {
				t.Fatalf("Flatten(%q) = error %v", tc.src, err)
			}
			if string(got) != tc.want {
				t.Errorf("Flatten(%q)\n got %q\nwant %q", tc.src, got, tc.want)
			}
			if i := bytes.IndexAny(got, "\n\r"); i >= 0 {
				t.Fatalf("Flatten(%q) = %q, which is not one line (byte %d)", tc.src, got, i)
			}
			assertMeaningPreserved(t, tc.src, got)
		})
	}
}

// TestFlattenIsIdempotent asserts that flattening settles after one pass.
// A second pass exercising the same code on already-folded input is a cheap
// way to catch a separator or semicolon rule that is not stable.
func TestFlattenIsIdempotent(t *testing.T) {
	t.Parallel()

	for _, tc := range flattenCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			once, err := instrument.Flatten([]byte(tc.src))
			if err != nil {
				t.Fatalf("Flatten(%q) = error %v", tc.src, err)
			}
			twice, err := instrument.Flatten(once)
			if err != nil {
				t.Fatalf("Flatten(Flatten(%q)) = error %v", tc.src, err)
			}
			if !bytes.Equal(once, twice) {
				t.Errorf("Flatten is not idempotent for %q:\nonce  %q\ntwice %q", tc.src, once, twice)
			}
		})
	}
}

// TestFlattenNeverEmitsALineComment pins the reason line comments are dropped:
// one surviving "//" would comment out everything the guard puts after it.
func TestFlattenNeverEmitsALineComment(t *testing.T) {
	t.Parallel()

	sources := []string{
		"a + // trailing\n\tb",
		"f(\n\ta, // first\n\tb, // second\n)",
		"//only\n",
		"a //\n+ b",
		"a /* fine */ + b // gone",
	}
	for _, src := range sources {
		got, err := instrument.Flatten([]byte(src))
		if err != nil {
			t.Fatalf("Flatten(%q) = error %v", src, err)
		}
		if bytes.Contains(got, []byte("//")) {
			t.Errorf("Flatten(%q) = %q, which contains a line comment", src, got)
		}
		assertMeaningPreserved(t, src, got)
	}
}

func TestFlattenErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want instrument.Code
	}{
		{name: "illegal character", src: "a $ b", want: instrument.CodeUntokenizable},
		{name: "unterminated interpreted string", src: `"abc`, want: instrument.CodeUntokenizable},
		{name: "unterminated raw string", src: "`abc", want: instrument.CodeUntokenizable},
		{
			// The same unterminated literal, now spanning a line, so that the
			// text the scanner hands back holds a line break and the literal
			// rewrite is attempted on it. That rewrite fails — the bytes are
			// not a literal — and the diagnosis the caller needs is still the
			// scanner's, not the rewrite's downstream complaint about it.
			name: "unterminated raw string spanning lines",
			src:  "`abc\ndef",
			want: instrument.CodeUntokenizable,
		},
		{
			name: "unterminated interpreted string after a carriage return",
			src:  "\"a\r",
			want: instrument.CodeUntokenizable,
		},
		{name: "unterminated rune literal after a carriage return", src: "'\r", want: instrument.CodeUntokenizable},
		{name: "unterminated block comment", src: "a /* nope", want: instrument.CodeUntokenizable},
		{name: "unterminated rune literal", src: "'a", want: instrument.CodeUntokenizable},
		{name: "invalid utf8", src: "a + \xff", want: instrument.CodeUntokenizable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := instrument.Flatten([]byte(tc.src))
			if err == nil {
				t.Fatalf("Flatten(%q) = %q, want error %s", tc.src, got, tc.want)
			}
			if code := instrument.CodeOf(err); code != tc.want {
				t.Fatalf("Flatten(%q) = error %v with code %q, want %q", tc.src, err, code, tc.want)
			}
			if got != nil {
				t.Errorf("Flatten(%q) returned %q alongside its error, want nil", tc.src, got)
			}
			if !strings.HasPrefix(err.Error(), string(tc.want)+": ") {
				t.Errorf("error %q does not lead with its code", err)
			}
		})
	}
}

// TestFlattenSelfChecksFire runs the postconditions Flatten applies to its own
// output.
//
// The package's central claim is that a rendering bug surfaces as a loud error
// rather than as a mutant that compiles into a different program, and these
// three checks are the whole of that claim's machinery. No input reaches them —
// that is what makes them postconditions — so they are driven here through the
// test-only hooks in export_test.go, with bytes standing in for the output a
// broken renderer would have produced. An unrun check proves nothing about the
// day it is needed.
func TestFlattenSelfChecksFire(t *testing.T) {
	t.Parallel()

	t.Run("one line", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name string
			out  string
			want instrument.Code
		}{
			{name: "newline", out: "a\nb", want: instrument.CodeNotFlat},
			{name: "carriage return", out: "a\rb", want: instrument.CodeNotFlat},
			{name: "newline inside a literal", out: "\"a\nb\"", want: instrument.CodeNotFlat},
			{name: "one line", out: "a+b"},
			{name: "empty", out: ""},
		} {
			err := instrument.CheckFlat([]byte(tc.out))
			if got := instrument.CodeOf(err); got != tc.want {
				t.Errorf("CheckFlat(%q) = %v with code %q, want %q", tc.out, err, got, tc.want)
			}
		}
		err := instrument.CheckFlat([]byte("ab\ncd"))
		if err == nil || !strings.Contains(err.Error(), "byte 2") {
			t.Errorf("CheckFlat = %v, want it to name the offending byte", err)
		}
	})

	t.Run("re-tokenizes identically", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name       string
			out, want  string
			wantCode   instrument.Code
			wantDetail string
		}{
			{
				// The failure the check exists for: a separator that was not
				// written, so two tokens fused into a third.
				name: "tokens fused",
				out:  "ab", want: "a b",
				wantCode: instrument.CodeNotIdentical, wantDetail: "re-tokenizes to 1 tokens, want 2",
			},
			{
				name: "token changed",
				out:  "a-b", want: "a+b",
				wantCode: instrument.CodeNotIdentical, wantDetail: "at token 1",
			},
			{
				name: "output does not tokenize",
				out:  "a $ b", want: "a b",
				wantCode: instrument.CodeNotIdentical, wantDetail: "does not tokenize",
			},
			{
				name: "output tokenizes to more than it should",
				out:  "a+b", want: "ab",
				wantCode: instrument.CodeNotIdentical, wantDetail: "want 1",
			},
			{name: "faithful rendering", out: "a+b", want: "a + b"},
			{name: "faithful rendering of a statement list", out: "a();b()", want: "a()\nb()"},
		} {
			err := instrument.VerifyTokensAgainst([]byte(tc.out), []byte(tc.want))
			if got := instrument.CodeOf(err); got != tc.wantCode {
				t.Errorf("VerifyTokensAgainst(%q, %q) = %v with code %q, want %q",
					tc.out, tc.want, err, got, tc.wantCode)
				continue
			}
			if tc.wantDetail != "" && !strings.Contains(err.Error(), tc.wantDetail) {
				t.Errorf("VerifyTokensAgainst(%q, %q) = %q, want it to mention %q",
					tc.out, tc.want, err, tc.wantDetail)
			}
		}
	})

	t.Run("literal conversion", func(t *testing.T) {
		t.Parallel()

		// Bytes the scanner would have rejected as an unterminated literal, so
		// that strconv.Unquote is handed something it cannot read. Every route
		// to this branch through Flatten itself is closed by the scan error
		// being reported first, which is the point of the branch: the failure
		// is reported rather than a wrong string silently written.
		if _, err := instrument.FlattenLiteral(token.STRING, "`abc\ndef"); instrument.CodeOf(err) != instrument.CodeRawStringConversion {
			t.Errorf("FlattenLiteral of an unterminated raw literal = %v, want %s", err, instrument.CodeRawStringConversion)
		}
		if _, err := instrument.FlattenLiteral(token.CHAR, "'ab\rcd'"); instrument.CodeOf(err) != instrument.CodeRawStringConversion {
			t.Errorf("FlattenLiteral of a multi-rune literal = %v, want %s", err, instrument.CodeRawStringConversion)
		}
		for _, tc := range []struct {
			tok       token.Token
			lit, want string
		}{
			{tok: token.STRING, lit: "`ab`", want: "`ab`"},
			{tok: token.STRING, lit: `"a\rb"`, want: `"a\rb"`},
			{tok: token.STRING, lit: "`a\nb`", want: `"a\nb"`},
			{tok: token.STRING, lit: "\"a\rb\"", want: `"a\rb"`},
			{tok: token.CHAR, lit: "'a'", want: "'a'"},
			{tok: token.CHAR, lit: "'\r'", want: `'\r'`},
		} {
			got, err := instrument.FlattenLiteral(tc.tok, tc.lit)
			if err != nil {
				t.Errorf("FlattenLiteral(%s, %q) = error %v", tc.tok, tc.lit, err)
				continue
			}
			if got != tc.want {
				t.Errorf("FlattenLiteral(%s, %q) = %q, want %q", tc.tok, tc.lit, got, tc.want)
			}
		}
	})
}

// TestFlattenDoesNotRequireParseableInput records that the flattener works at
// the token level. Discovery hands it byte spans, and a span of a statement is
// not always something go/parser will accept on its own.
//
// These cases carry the separator rules that no parseable fragment can reach.
// A "." followed by a digit is the clearest: written flush, "a.5" scans as an
// identifier beside a floating-point literal rather than as the three tokens
// that went in — and yet no valid Go puts a number after a selector's dot, so
// the rule can only be exercised by a fragment go/parser would turn down. The
// separator is still the difference between reproducing the token stream and
// inventing a new one, which is the flattener's whole contract.
func TestFlattenDoesNotRequireParseableInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want string
	}{
		{name: "case clause", src: "case 1:\n\tf()", want: "case 1:f()"},
		{name: "period before a digit", src: "a . 5", want: "a. 5"},
		{name: "period between numbers", src: "1 . 5", want: "1 . 5"},
		{name: "hexadecimal before a period and a digit", src: "0x1f . 5", want: "0x1f . 5"},
		{name: "dangling operator", src: "+\n\t*", want: "+ *"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := instrument.Flatten([]byte(tc.src))
			if err != nil {
				t.Fatalf("Flatten(%q) of an unparsable but tokenizable fragment: %v", tc.src, err)
			}
			if string(got) != tc.want {
				t.Errorf("Flatten(%q) = %q, want %q", tc.src, got, tc.want)
			}
			// Flatten already asserts this internally; asserting it here too is
			// what makes the case evidence rather than a change detector.
			if err := instrument.VerifyTokensAgainst(got, []byte(tc.src)); err != nil {
				t.Errorf("Flatten(%q) = %q, which does not re-tokenize to its input: %v", tc.src, got, err)
			}
		})
	}
}
