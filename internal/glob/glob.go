// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package glob implements the path matching language that decides which files
// go-mutants mutates.
//
// It lives in-tree on purpose. Third-party globbers disagree about what "**"
// means: whether it may match zero directories, whether it is special inside a
// larger path element, whether a trailing "**" also matches the directory it
// names. Every one of those disagreements changes which mutants a run
// produces, and a mutant catalog has to be a property of the pattern and the
// tree alone, never of which library version happened to resolve into the
// module graph. So the semantics are pinned here, documented case by case, and
// fuzzed against a naive reference matcher.
//
// # Syntax
//
// A pattern is split on '/' into elements, and a candidate path is split the
// same way. '/' is the only separator on every platform, including Windows;
// callers normalize '\' to '/' before they get here. A '\' that survives that
// normalization is an ordinary literal byte, neither a separator nor an
// escape.
//
// An element of exactly "**" matches zero or more whole path elements. Inside
// any other element:
//
//   - '*' matches a run, possibly empty, of non-separator bytes;
//   - '?' matches exactly one non-separator byte;
//   - every other byte matches only itself.
//
// Matching is case sensitive and byte oriented on every platform: "A.go" never
// matches "a.go", and '?' consumes one byte rather than one rune, so a
// two-byte character needs two of them. Byte orientation is what keeps a
// pattern's meaning independent of any locale or Unicode table version.
//
// The language is deliberately small. There is no brace expansion, no
// character class, and no escape sequence. Because there is no escape
// sequence, a literal '*' or '?' in a file name cannot be demanded: the
// pattern "*" does match the path "*", but it matches every other single
// element path too, and nothing can insist on the asterisk itself. A tree that
// needs that has a bigger problem than pattern syntax.
//
// # Decided edge cases
//
// All of these follow from the rules above. Each is spelled out because a
// different globber may well have chosen otherwise:
//
//   - "**" is special only as a complete element. "a**b" is an ordinary
//     element holding two adjacent '*' wildcards, so it behaves exactly like
//     "a*b", and "**.go" behaves like "*.go". Neither crosses a separator.
//   - "**/*.go" matches "a.go", because "**" may match zero elements, and it
//     matches "x/y/a.go" for the same reason it matches one or two.
//   - "vendor/**" matches the bare path "vendor" as well as everything below
//     it, again because "**" may match zero elements. That is what someone
//     excluding a directory tree means, and it matches gitignore intuition. It
//     does not match "vendorx".
//   - A pattern with no "**" element must match the path element for element:
//     "a/*" does not match "a/b/c" and "*" does not match "a/b".
//   - '*' and '?' never match a separator, so a single-element pattern can
//     never match a multi-element path.
//   - A leading dot is not special: "*" matches ".hidden", and "**" matches
//     ".git/config".
//
// # Rejected input
//
// [Compile] rejects the empty pattern, a leading '/', a trailing '/', and an
// empty element in the middle such as "a//b", each with a [SyntaxError]
// carrying the column of the offending byte.
//
// [Pattern.Match] is total: it never reports an error and never panics. A path
// that is empty, or that holds an empty element for the same reasons a pattern
// would be rejected, simply matches nothing.
//
// Matching nothing is not a safety net, and this package does not claim it is
// one. False is the harmless answer for an include pattern, but it is the
// wrong direction for an exclude pattern: "vendor/**" does not exclude
// "vendor/x/", so a malformed candidate slips past the very exclusion that was
// written to catch it. Whether such a path is mutated in the end is decided by
// the caller's selection rule — whether includes are mandatory, what an empty
// include set means — which is a property of the caller and not of this
// package. So callers validate candidates where they are produced: a tree
// walker that emits a trailing '/' or an empty element has a bug worth
// reporting, and a false from Match is an answer about the pattern, never
// protection against a malformed path.
package glob

import (
	"fmt"
	"strings"
)

// A SyntaxError reports a pattern that [Compile] refused, annotated with the
// position of the byte that made it invalid so the CLI can underline it. Every
// error [Compile] returns has this concrete type, reachable with errors.As.
type SyntaxError struct {
	// Pattern is the pattern exactly as it was handed to Compile.
	Pattern string
	// Column is the 1-based byte position of the offending byte. A pattern is
	// always a single line, so a column alone locates the problem. It is 1 for
	// the empty pattern, which has no byte to point at.
	Column int
	// Message states the problem without repeating the pattern, so a caller
	// that already shows the pattern can print the message on its own.
	Message string
}

// Error implements the error interface.
func (e *SyntaxError) Error() string {
	return fmt.Sprintf("invalid glob pattern %q: %s (column %d)", e.Pattern, e.Message, e.Column)
}

// elementKind distinguishes the three shapes an element can take once the
// pattern is compiled. Classifying once means Match never re-inspects pattern
// bytes to find out which matcher to run.
type elementKind uint8

const (
	// kindLiteral holds no wildcard at all and compares as a whole string.
	kindLiteral elementKind = iota
	// kindWildcard holds at least one '*' or '?' and runs the byte matcher.
	kindWildcard
	// kindDoubleStar is the element "**" exactly, and matches zero or more
	// whole path elements.
	kindDoubleStar
)

// An element is one '/'-separated piece of a compiled pattern.
type element struct {
	kind elementKind
	// text is the element's bytes for kindLiteral and kindWildcard. It is
	// empty and unused for kindDoubleStar, whose behavior needs no bytes.
	text string
}

// match reports whether a non-"**" element matches one path element. The
// caller guarantees that segment holds no separator, which is why the wildcard
// matcher never has to refuse a byte.
func (e element) match(segment string) bool {
	if e.kind == kindLiteral {
		return e.text == segment
	}
	return matchWildcard(e.text, segment)
}

// A Pattern is a compiled matcher. Compiling separates the cost of parsing
// from the cost of matching, which matters because a run matches every
// discovered file against every include and exclude pattern.
//
// A Pattern is immutable once compiled and safe for concurrent use. The zero
// Pattern is valid and matches nothing, since a pattern with no elements can
// only match a path with no elements and every path has at least one.
type Pattern struct {
	source   string
	elements []element
}

// String returns the pattern text [Compile] was given, so a Pattern can be
// echoed back in diagnostics without the caller keeping the string alongside
// it. The zero Pattern reports "", which Compile would have rejected.
func (p Pattern) String() string {
	return p.source
}

// Compile parses a pattern into a [Pattern]. The returned error, when there is
// one, is always a *[SyntaxError].
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
	// column tracks the 1-based position of the first byte of parts[i]. The
	// leading and trailing cases are already gone, so an empty part here can
	// only be the "//" in the middle of a pattern.
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

// MustCompile is [Compile] for patterns fixed at authoring time, such as the
// built-in exclusions and test tables. It panics rather than returning an
// error, because a constant pattern that does not compile is a bug in this
// repository and not a condition a caller could handle.
func MustCompile(pattern string) Pattern {
	compiled, err := Compile(pattern)
	if err != nil {
		panic(err)
	}
	return compiled
}

// classify decides once which matcher an element needs. Only the whole element
// "**" is the directory-crossing wildcard; "a**b" and "**.go" are ordinary
// wildcard elements whose adjacent stars collapse, which is exactly what the
// package documentation promises.
func classify(part string) element {
	if part == "**" {
		return element{kind: kindDoubleStar}
	}
	if strings.ContainsAny(part, "*?") {
		return element{kind: kindWildcard, text: part}
	}
	return element{kind: kindLiteral, text: part}
}

// Match reports whether path matches the pattern. path uses '/' as its only
// separator; an empty path, or one holding an empty element, matches nothing.
func (p Pattern) Match(path string) bool {
	segments := strings.Split(path, "/")
	for _, segment := range segments {
		// strings.Split never returns an empty slice, so the empty path
		// arrives here as a single empty element and is rejected with the
		// genuinely malformed ones.
		if segment == "" {
			return false
		}
	}

	// next[j] answers "do the elements from i+1 onward match the path from
	// element j onward", and current[j] answers the same for i. Sweeping i
	// backwards over two rows is what bounds the whole matcher at
	// O(len(pattern) * len(path)); the obvious recursive reading of "**"
	// instead explores an exponential number of splits on a pattern such as
	// "**/**/**/*a".
	count := len(segments)
	next := make([]bool, count+1)
	current := make([]bool, count+1)
	// The row past the last element: a spent pattern matches only a spent path.
	next[count] = true

	for i := len(p.elements) - 1; i >= 0; i-- {
		e := p.elements[i]
		if e.kind == kindDoubleStar {
			// "**" either steps past itself, consuming nothing, or swallows
			// one more element and stays. Reading current[j+1] from a cell
			// already written in this same backwards sweep is what turns that
			// choice into a constant-time step.
			current[count] = next[count]
			for j := count - 1; j >= 0; j-- {
				current[j] = next[j] || current[j+1]
			}
		} else {
			// Any other element consumes exactly one path element, which is
			// what makes a "**"-free pattern match element for element.
			current[count] = false
			for j := count - 1; j >= 0; j-- {
				current[j] = next[j+1] && e.match(segments[j])
			}
		}
		next, current = current, next
	}
	return next[0]
}

// matchWildcard reports whether one wildcard element matches one path element.
// It is the same two-row dynamic program as [Pattern.Match] one level down,
// with current[j] answering "does pattern[:i+1] match segment[:j]". Using a
// table here rather than the usual greedy scan with backtracking keeps a
// pattern like "a*a*a*a*a*b" linear in the product of the two lengths instead
// of exponential in the number of stars.
func matchWildcard(pattern, segment string) bool {
	length := len(segment)
	previous := make([]bool, length+1)
	current := make([]bool, length+1)
	// Before any pattern byte is consumed, only the empty prefix matches.
	previous[0] = true

	for i := 0; i < len(pattern); i++ {
		b := pattern[i]
		// Only a star can still match the empty prefix of the segment.
		current[0] = b == '*' && previous[0]
		for j := 1; j <= length; j++ {
			switch b {
			case '*':
				// Match nothing more (previous[j]) or one further byte
				// (current[j-1]). A path element holds no separator, so there
				// is no byte a star has to refuse.
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
