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

func Flatten(src []byte) ([]byte, error) {
	tokens, err := scanFragment(src)
	if err != nil {
		return nil, err
	}
	tokens = dropTrailingImplicitSemicolons(tokens)
	out := render(tokens)

	if err := checkFlat(out); err != nil {
		return nil, err
	}
	if err := verifyTokens(out, tokens); err != nil {
		return nil, err
	}
	return out, nil
}

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

type fragToken struct {
	tok      token.Token
	text     string
	implicit bool
}

func scanFragment(src []byte) ([]fragToken, error) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))

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
			tokens = append(tokens, fragToken{tok: tok, text: ";", implicit: lit != ";"})
		case token.STRING, token.CHAR:
			text, err := flattenLiteral(tok, lit)
			if err != nil {
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

func foldableComment(lit string) bool {
	if strings.HasPrefix(lit, "//") {
		return false
	}
	return !strings.ContainsAny(lit, "\n\r")
}

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
		r, _ := utf8.DecodeRuneInString(value)
		return strconv.QuoteRune(r), nil
	}
	return strconv.Quote(value), nil
}

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

func isNumericLiteral(tok token.Token) bool {
	switch tok {
	case token.INT, token.FLOAT, token.IMAG:
		return true
	default:
		return false
	}
}

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

func isOperatorByte(b byte) bool {
	return strings.IndexByte("+-*/%&|^<>=!:.~", b) >= 0
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func verifyTokens(out []byte, want []fragToken) error {
	got, err := scanFragment(out)
	if err != nil {
		return &Error{Code: CodeNotIdentical, Message: "flattened source does not tokenize", Err: err}
	}
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
