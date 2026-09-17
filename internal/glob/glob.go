// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package glob

import (
	"fmt"
	"strings"
)

type SyntaxError struct {
	Pattern string
	Column  int
	Message string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("invalid glob pattern %q: %s (column %d)", e.Pattern, e.Message, e.Column)
}

type elementKind uint8

const (
	kindLiteral elementKind = iota
	kindWildcard
	kindDoubleStar
)

type element struct {
	kind elementKind
	text string
}

func (e element) match(segment string) bool {
	if e.kind == kindLiteral {
		return e.text == segment
	}
	return matchWildcard(e.text, segment)
}

type Pattern struct {
	source   string
	elements []element
}

func (p Pattern) String() string {
	return p.source
}

func Compile(pattern string) (Pattern, error) {
	if pattern == "" {
		return Pattern{}, &SyntaxError{
			Pattern: pattern,
			Column:  1,
			Message: "empty pattern",
		}
	}
	if pattern[0] == '/' {
		return Pattern{}, &SyntaxError{
			Pattern: pattern,
			Column:  1,
			Message: "leading '/': patterns are relative to the module root",
		}
	}
	if pattern[len(pattern)-1] == '/' {
		return Pattern{}, &SyntaxError{
			Pattern: pattern,
			Column:  len(pattern),
			Message: `trailing '/': write "/**" to match a directory and everything under it`,
		}
	}

	parts := strings.Split(pattern, "/")
	elements := make([]element, len(parts))
	column := 1
	for i, part := range parts {
		if part == "" {
			return Pattern{}, &SyntaxError{
				Pattern: pattern,
				Column:  column,
				Message: "empty path element between two '/'",
			}
		}
		elements[i] = classify(part)
		column += len(part) + 1
	}
	return Pattern{source: pattern, elements: elements}, nil
}

func MustCompile(pattern string) Pattern {
	compiled, err := Compile(pattern)
	if err != nil {
		panic(err)
	}
	return compiled
}

func classify(part string) element {
	if part == "**" {
		return element{kind: kindDoubleStar}
	}
	if strings.ContainsAny(part, "*?") {
		return element{kind: kindWildcard, text: part}
	}
	return element{kind: kindLiteral, text: part}
}

func (p Pattern) Match(path string) bool {
	segments := strings.Split(path, "/")
	for _, segment := range segments {
		if segment == "" {
			return false
		}
	}

	count := len(segments)
	next := make([]bool, count+1)
	current := make([]bool, count+1)
	next[count] = true

	for i := len(p.elements) - 1; i >= 0; i-- {
		e := p.elements[i]
		if e.kind == kindDoubleStar {
			current[count] = next[count]
			for j := count - 1; j >= 0; j-- {
				current[j] = next[j] || current[j+1]
			}
		} else {
			current[count] = false
			for j := count - 1; j >= 0; j-- {
				current[j] = next[j+1] && e.match(segments[j])
			}
		}
		next, current = current, next
	}
	return next[0]
}

func matchWildcard(pattern, segment string) bool {
	length := len(segment)
	previous := make([]bool, length+1)
	current := make([]bool, length+1)
	previous[0] = true

	for i := 0; i < len(pattern); i++ {
		b := pattern[i]
		current[0] = b == '*' && previous[0]
		for j := 1; j <= length; j++ {
			switch b {
			case '*':
				current[j] = previous[j] || current[j-1]
			case '?':
				current[j] = previous[j-1]
			default:
				current[j] = previous[j-1] && segment[j-1] == b
			}
		}
		previous, current = current, previous
	}
	return previous[length]
}
