// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument

import (
	"bytes"
	"fmt"
	"go/scanner"
	"go/token"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Flatten renders one Go expression or statement on a single line, preserving
// its meaning exactly.
//
// The guard forms splice a mutated copy of a statement into the same line the
// original occupies, so the copy has to fit on one line however the author
// wrote it — a call broken across five lines with a trailing comma, a raw
// string holding a here-document, a comment in the middle of a condition.
// Flatten is what makes that possible, and the line-preservation invariant
// described in the package documentation is what makes it necessary.
//
// The mechanism is re-tokenization, not pretty-printing: src is scanned with
// go/scanner and the tokens are written back out joined by the smallest
// separator that keeps them scanning as the same tokens. Every token is
// reproduced byte for byte, with four exceptions, each of which is a
// consequence of collapsing the line breaks rather than a liberty taken with
// the source:
//
//   - Where Go's automatic semicolon insertion fired at a line break, an
//     explicit ";" is written. This is what keeps a multi-statement fragment —
//     or a function literal with a body — meaning what it meant, and it is
//     precisely the information that would otherwise be lost with the newline.
//   - Comments are dropped when they cannot survive the fold: a "//" comment
//     would swallow the remainder of the line, and a "/* */" comment
//     containing a line break cannot be folded without rewriting its innards,
//     which Flatten will not do. Single-line "/* */" comments are kept.
//     Nothing of meaning is lost — a flattened copy is machine-generated code
//     that only ever needs to compile, and the pristine original is what the
//     guard's other branch keeps and what the user reads.
//   - A raw string literal containing a line break becomes an interpreted
//     literal with an identical value, via [strconv.Quote] of the value the
//     scanner reports. It is value-preserving including the language's rule
//     that carriage returns are discarded from raw string values.
//   - An interpreted string or rune literal containing a raw carriage return is
//     re-quoted with that carriage return escaped, again through the value the
//     scanner reports. Such a literal is valid, compilable Go — a carriage
//     return is the one line break go/scanner keeps inside a token rather than
//     rejecting, since only a newline terminates these literals — so refusing
//     it would be refusing source a user is entitled to write.
//
// Those last two are the only token rewrites Flatten performs. Each is a
// literal that carries a line break inside itself being re-spelled as the same
// value without one, which is the minimum a one-line rendering can get away
// with; everything else is the source's own bytes.
//
// The returned bytes contain no "\n" and no "\r", and re-tokenize to the
// stream Flatten emitted. Both are verified on every call rather than trusted,
// because a separator bug would otherwise produce a plausible-looking mutant
// that compiles as a different program. Flatten does not parse: input that
// tokenizes but does not parse is flattened all the same, and input that does
// not tokenize is a [CodeUntokenizable] error.
//
// Leading and trailing whitespace is not part of a token and does not survive.
// Input that holds no tokens at all — empty, whitespace, or nothing but
// dropped comments — flattens to empty output without error.
func Flatten(src []byte) ([]byte, error) {
	tokens, err := scanFragment(src)
	if err != nil {
		return nil, err
	}
	tokens = dropTrailingImplicitSemicolons(tokens)
	out := render(tokens)

	// Postconditions. Both failures are bugs in this package rather than
	// anything the caller did, and both are checked because the cost of
	// checking is one scan of a statement and the cost of not checking is a
	// silently miscompiled mutant.
	if err := checkFlat(out); err != nil {
		return nil, err
	}
	if err := verifyTokens(out, tokens); err != nil {
		return nil, err
	}
	return out, nil
}

// checkFlat reports whether out still holds a line break.
//
// Carriage returns count as well as newlines. A lone carriage return does not
// start a line as far as go/token is concerned, so it would not disturb the
// line numbering the package exists to preserve — but it would be a byte the
// flattener could not account for, and every one that can appear inside a token
// has an escaped spelling that fits on the line. Rejecting it keeps the
// postcondition a statement about the output rather than about the platform's
// line endings.
func checkFlat(out []byte) error {
	i := bytes.IndexAny(out, "\n\r")
	if i < 0 {
		return nil
	}
	return &Error{
		Code:    CodeNotFlat,
		Message: fmt.Sprintf("flattened source still contains a line break at byte %d", i),
	}
}

// fragToken is one token of a scanned fragment, paired with the exact bytes
// Flatten will write for it.
type fragToken struct {
	tok  token.Token
	text string
	// implicit records a semicolon that go/scanner inserted at a line break
	// rather than one the author wrote. The distinction survives only until
	// the trailing ones are dropped; the text written is ";" either way.
	implicit bool
}

// scanFragment tokenizes src, dropping the comments that cannot be folded onto
// one line and rewriting the raw string literals that cannot either.
func scanFragment(src []byte) ([]fragToken, error) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))

	// The scanner reports through a handler; with a nil handler it only bumps
	// a counter, and a fragment with an unterminated string would be flattened
	// into nonsense with no explanation of what went wrong.
	var firstErr error
	var s scanner.Scanner
	s.Init(file, src, func(pos token.Position, msg string) {
		if firstErr == nil {
			firstErr = fmt.Errorf("%d:%d: %s", pos.Line, pos.Column, msg)
		}
	}, scanner.ScanComments)

	tokens := make([]fragToken, 0, 32)
	illegal := false
	var convErr error
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		switch tok {
		case token.ILLEGAL:
			illegal = true
		case token.COMMENT:
			if foldableComment(lit) {
				tokens = append(tokens, fragToken{tok: tok, text: lit})
			}
		case token.SEMICOLON:
			// go/scanner reports an inserted semicolon with the line break it
			// stood for as its literal, and an explicit one as ";".
			tokens = append(tokens, fragToken{tok: tok, text: ";", implicit: lit != ";"})
		case token.STRING, token.CHAR:
			text, err := flattenLiteral(tok, lit)
			if err != nil {
				// Held rather than returned, so that the scan can finish and
				// the error the scanner itself reported — if there is one — is
				// the error the caller sees. An unterminated literal reaches
				// this branch as a literal whose value cannot be recovered, and
				// reporting that instead would answer "why can this not be
				// converted" when the caller asked "what is wrong with my
				// source".
				if convErr == nil {
					convErr = err
				}
				text = lit
			}
			tokens = append(tokens, fragToken{tok: tok, text: text})
		default:
			tokens = append(tokens, fragToken{tok: tok, text: tokenText(tok, lit)})
		}
	}

	if firstErr != nil {
		return nil, &Error{Code: CodeUntokenizable, Message: "source does not tokenize", Err: firstErr}
	}
	if illegal {
		return nil, &Error{Code: CodeUntokenizable, Message: "source contains an illegal token"}
	}
	if convErr != nil {
		return nil, convErr
	}
	return tokens, nil
}

// tokenText returns the bytes that reproduce a token. Operators and keywords
// carry no literal and are spelled by the token itself; literals and
// identifiers are their own text.
func tokenText(tok token.Token, lit string) string {
	switch {
	case tok == token.SEMICOLON:
		return ";"
	case tok == token.COMMENT, tok.IsLiteral():
		return lit
	default:
		return tok.String()
	}
}

// foldableComment reports whether a comment can be written onto a single line
// unchanged. A "//" comment cannot: everything after it on the flattened line
// would be commented out. A "/* */" comment cannot once it contains a line
// break, and rewriting its innards to make it fit is not something a tool
// should do to somebody's prose — dropping it costs nothing, since the guard's
// other branch keeps the original bytes with the comment intact.
//
// Dropping a multi-line block comment never loses a semicolon: go/scanner
// treats such a comment as a line break for insertion purposes and reports the
// inserted semicolon as its own token, which survives independently.
func foldableComment(lit string) bool {
	if strings.HasPrefix(lit, "//") {
		return false
	}
	return !strings.ContainsAny(lit, "\n\r")
}

// flattenLiteral returns the literal text to write for a string or rune token.
//
// A literal is rewritten exactly when it carries a line break inside itself,
// and it is rewritten to a literal of the same kind of value spelled without
// one. Two shapes can:
//
//   - A raw string literal spanning lines, which becomes an interpreted
//     literal. go/scanner has already discarded the carriage returns from such
//     a literal, per the language's rule for raw string values, so what is left
//     to fold is the newlines.
//   - An interpreted string or rune literal holding a raw carriage return,
//     which becomes the same literal with that carriage return escaped. Only a
//     newline terminates these literals, so a carriage return inside one is
//     valid Go that reaches this function verbatim.
//
// Both go through the value rather than the spelling: [strconv.Unquote] of the
// literal the scanner reported is the language's definition of what that
// literal denotes, so quoting the result back reproduces the value exactly. A
// literal that already fits on one line is returned untouched, because the
// smallest edit is the one least able to be wrong.
func flattenLiteral(tok token.Token, lit string) (string, error) {
	if !strings.ContainsAny(lit, "\n\r") {
		return lit, nil
	}
	value, err := strconv.Unquote(lit)
	if err != nil {
		return "", &Error{
			Code:    CodeRawStringConversion,
			Message: "literal containing a line break could not be converted",
			Err:     err,
		}
	}
	if tok == token.CHAR {
		// Unquote accepts a rune literal only when it denotes exactly one rune,
		// so the first rune of the value it returned is the whole of it.
		r, _ := utf8.DecodeRuneInString(value)
		return strconv.QuoteRune(r), nil
	}
	return strconv.Quote(value), nil
}

// dropTrailingImplicitSemicolons removes the semicolons the scanner inserted
// at the end of the fragment.
//
// A fragment ending in an identifier, a literal, or a closing bracket picks up
// an inserted semicolon at its final line break or at EOF. Keeping it would be
// harmless in a statement and fatal in an expression: go/parser accepts a
// trailing inserted semicolon after an expression precisely because it can
// tell it was inserted, and an explicit ";" written in its place turns a valid
// operand into a syntax error the moment it is spliced into a larger
// expression. Semicolons the author wrote are kept wherever they are.
func dropTrailingImplicitSemicolons(tokens []fragToken) []fragToken {
	for len(tokens) > 0 {
		last := tokens[len(tokens)-1]
		if last.tok != token.SEMICOLON || !last.implicit {
			break
		}
		tokens = tokens[:len(tokens)-1]
	}
	return tokens
}

// render concatenates the tokens, separating only the pairs that would
// otherwise scan as something else.
func render(tokens []fragToken) []byte {
	out := make([]byte, 0, 64)
	for i, t := range tokens {
		if i > 0 && needsSeparator(tokens[i-1], t) {
			out = append(out, ' ')
		}
		out = append(out, t.text...)
	}
	return out
}

// needsSeparator reports whether writing next straight after prev would let
// the scanner read something other than the two tokens.
//
// The decision is made mostly on the two bytes that meet, which is most of what
// can fuse: every Go token is a maximal munch of a single character class. Two
// word bytes meeting would join two identifiers, keywords, or numbers into one;
// two operator bytes meeting can form a longer operator ("<" "-" becoming "<-")
// or open a comment ("/" "/"); and a digit meeting "." or "." meeting a digit
// would produce a floating-point literal. Anything else — a bracket, a comma,
// a quote — cannot combine with its neighbour and is written flush against it.
//
// The one case the meeting bytes cannot decide is a numeric literal followed by
// a ".", because a numeric literal need not end in a digit: "0x1f", "1_0", "1i"
// and "0b1" all end in a byte that says nothing about the token it closes. A
// "." written flush against one of those continues the number instead of
// starting a selector — "0x1f.b" scans as a hexadecimal float that is missing
// its exponent — so the token kind decides it.
//
// The rule is deliberately a little coarser than strictly necessary: a spare
// space is free, and [Flatten] proves the result re-tokenizes rather than
// relying on this function being exactly minimal.
func needsSeparator(prev, next fragToken) bool {
	if prev.text == "" || next.text == "" {
		return false
	}
	last, first := prev.text[len(prev.text)-1], next.text[0]
	switch {
	case isWordByte(last) && isWordByte(first):
		return true
	case isOperatorByte(last) && isOperatorByte(first):
		return true
	case isDigit(last) && first == '.':
		return true
	case last == '.' && isDigit(first):
		return true
	case isNumericLiteral(prev.tok) && first == '.':
		return true
	default:
		return false
	}
}

// isNumericLiteral reports whether tok is one of the number literals, whose
// final byte may be a letter and whose text a following "." would extend.
func isNumericLiteral(tok token.Token) bool {
	switch tok {
	case token.INT, token.FLOAT, token.IMAG:
		return true
	default:
		return false
	}
}

// isWordByte reports whether b can appear inside an identifier or a numeric
// literal. Bytes above ASCII are included: they are the encoding of a letter
// or digit in a unicode identifier, and two of them meeting must be kept
// apart.
func isWordByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_', b >= 0x80:
		return true
	default:
		return false
	}
}

// isOperatorByte reports whether b can start or continue a multi-byte operator
// or a comment introducer. Brackets, commas and semicolons are excluded: no Go
// token begins with one of them and another operator byte.
func isOperatorByte(b byte) bool {
	return strings.IndexByte("+-*/%&|^<>=!:.~", b) >= 0
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// verifyTokens re-scans the rendered output and checks it reads back as the
// stream it was rendered from.
//
// This is the flattener proving its own postcondition on every call. A
// separator this package failed to write does not produce an error anywhere
// else in the pipeline: it produces source that compiles and means something
// different, which is the one outcome a mutation testing tool must never
// produce on its own.
func verifyTokens(out []byte, want []fragToken) error {
	got, err := scanFragment(out)
	if err != nil {
		return &Error{Code: CodeNotIdentical, Message: "flattened source does not tokenize", Err: err}
	}
	// Re-scanning necessarily appends the semicolon the scanner inserts at
	// EOF, which is not part of what was rendered. It is the only implicit
	// semicolon that can appear, since the rendered bytes hold no line break.
	got = dropTrailingImplicitSemicolons(got)

	if len(got) != len(want) {
		return &Error{
			Code: CodeNotIdentical,
			Message: fmt.Sprintf("flattened source re-tokenizes to %d tokens, want %d",
				len(got), len(want)),
		}
	}
	for i := range got {
		if got[i].tok == want[i].tok && got[i].text == want[i].text {
			continue
		}
		return &Error{
			Code: CodeNotIdentical,
			Message: fmt.Sprintf("flattened source re-tokenizes differently at token %d: got %s %q, want %s %q",
				i, got[i].tok, got[i].text, want[i].tok, want[i].text),
		}
	}
	return nil
}
