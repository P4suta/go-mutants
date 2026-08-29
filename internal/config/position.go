// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"bytes"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2/unstable"
)

// maxIndexDepth bounds how deep the position walk descends into nested arrays
// and inline tables. The schema is three levels deep at its worst
// (`mutation.expect[i].id`), so anything beyond this is a document that
// already failed to decode; the bound only keeps a pathological input from
// turning a diagnostic aid into a stack overflow.
const maxIndexDepth = 16

// indexPositions records where every key and every array element of a TOML
// document lives, keyed by the same dotted-and-indexed path the validators use
// to name a setting: "report.low", "mutation.include[2]",
// "mutation.expect[0].reason".
//
// Why a second pass over the document at all: the decoder reports a position
// only for input it refuses, and every check in this package's validator runs
// on input the decoder was perfectly happy with. A glob that does not compile,
// a threshold of 120, an operator that no longer exists — each is a legal TOML
// value in a legal place, so the only way to underline the offending line is
// to have written down where each value was.
//
// The walk collects byte offsets and converts them to lines and columns once,
// at the end, against a table of line starts. Doing it the other way round —
// asking the parser to shape each node as it is reached — costs a rescan from
// the top of the document per key, which is quadratic in file size and was
// measured at nine seconds for a 192 KiB file and minutes for a megabyte. A
// diagnostic aid may not be the slowest thing in the program.
//
// The walk is deliberately total and silent. It runs after the document has
// already decoded successfully, so a parse error here cannot be new
// information; if one somehow occurs, the keys found so far are kept and the
// rest simply have no position, which degrades an error message and nothing
// else. A missing position is always survivable, which is why this never
// returns an error.
func indexPositions(document []byte) map[string]Position {
	offsets := make(map[string]int)
	parser := &unstable.Parser{}
	parser.Reset(document)

	// prefix is the current table scope, already rendered with its array index
	// when the scope is an array-of-tables element. arrayTables counts how many
	// times each `[[header]]` has been seen so that the third `[[a.b]]` is
	// "a.b[2]" without the decoder having to tell us.
	prefix := ""
	arrayTables := make(map[string]int)

	for parser.NextExpression() {
		expression := parser.Expression()
		switch expression.Kind {
		case unstable.Table:
			prefix = dottedKey(expression)
		case unstable.ArrayTable:
			header := dottedKey(expression)
			n := arrayTables[header]
			arrayTables[header] = n + 1
			prefix = header + "[" + strconv.Itoa(n) + "]"
		case unstable.KeyValue:
			key := joinKey(prefix, dottedKey(expression))
			// The key's own position is the fallback for every value whose
			// node carries no bytes of its own, which for this parser means
			// every array: the array node's range is empty and only its
			// elements are located.
			record(offsets, key, expression)
			recordValue(offsets, key, expression.Value(), 0)
		default:
			// Comments and anything a future TOML revision adds are not keys
			// and have nothing to contribute to a key index.
		}
	}
	return resolvePositions(document, offsets)
}

// recordValue writes the offset of one value and, for containers, of
// everything inside it.
func recordValue(offsets map[string]int, key string, value *unstable.Node, depth int) {
	if value == nil || !value.Valid() || depth > maxIndexDepth {
		return
	}
	switch value.Kind {
	case unstable.Array:
		// The array itself has no bytes to point at, so key keeps the position
		// its own name was written at, which is the line a "this array is
		// wrong" message wants anyway.
		element := value.Children()
		for i := 0; element.Next(); i++ {
			child := element.Node()
			childKey := key + "[" + strconv.Itoa(i) + "]"
			record(offsets, childKey, child)
			recordValue(offsets, childKey, child, depth+1)
		}
	case unstable.InlineTable:
		// An inline table's children are its key-value pairs, so this is the
		// same work the top-level loop does, one scope down. It is what keeps
		// `expect = [{ id = "…" }]` as diagnosable as the `[[mutation.expect]]`
		// spelling of the same thing.
		pair := value.Children()
		for pair.Next() {
			node := pair.Node()
			if node.Kind != unstable.KeyValue {
				continue
			}
			childKey := joinKey(key, dottedKey(node))
			record(offsets, childKey, node)
			recordValue(offsets, childKey, node.Value(), depth+1)
		}
	default:
		// A scalar knows exactly where it is, and pointing at the value rather
		// than at its key is what puts the caret under the 120 in
		// `high = 120`.
		record(offsets, key, value)
	}
}

// record stores where a node begins, leaving the ones that begin nowhere out
// so that a fallback already written for a key is never overwritten with
// nothing. Absence from the map is what "no position is known" means here,
// which is why offset zero — the first key of the document — needs no sentinel
// to survive it.
//
// The zero-length guard is the whole point. A Table header and an Array both
// carry an empty range in this parser, and an empty range starts at offset
// zero, which resolves to line 1, column 1 — a position that is wrong rather
// than absent, and that would send every array diagnostic to the top of the
// file.
func record(offsets map[string]int, key string, node *unstable.Node) {
	if key == "" || node == nil || node.Raw.Length == 0 {
		return
	}
	// go-toml matches decoded struct fields case-insensitively, while the
	// validators name those fields with the schema's canonical lower-case
	// paths. Index the same canonical spelling so a legal mixed-case spelling
	// still points at the value that was actually written.
	key = strings.ToLower(key)
	offsets[key] = int(node.Raw.Offset)
}

// resolvePositions turns the collected byte offsets into lines and columns in
// one pass over the document plus one binary search per key.
func resolvePositions(document []byte, offsets map[string]int) map[string]Position {
	positions := make(map[string]Position, len(offsets))
	starts := lineStarts(document)
	for key, offset := range offsets {
		positions[key] = positionAt(starts, offset)
	}
	return positions
}

// lineStarts returns the byte offset at which each line of the document
// begins, starting with zero for the first line.
//
// Lines are split on "\n" alone, which is what the decoder does too: a
// carriage return is an ordinary byte belonging to the line it ends. That is
// not a rounding of the truth but the property that keeps the positions this
// package reports identical to the ones go-toml reports for the same file,
// under CRLF as much as under LF.
func lineStarts(document []byte) []int {
	starts := make([]int, 1, bytes.Count(document, []byte{'\n'})+1)
	for offset := 0; ; {
		i := bytes.IndexByte(document[offset:], '\n')
		if i < 0 {
			return starts
		}
		offset += i + 1
		starts = append(starts, offset)
	}
}

// positionAt locates a byte offset in a document indexed by [lineStarts].
//
// Every offset the walk collects came out of a range the parser cut from this
// same document, so the search always lands inside the table. The one
// impossible input is guarded anyway, because this is the code that turns a
// bad message into a good one and it may never be the thing that panics.
func positionAt(starts []int, offset int) Position {
	if offset < 0 {
		return Position{}
	}
	// BinarySearch answers with the index the offset would be inserted at, so
	// an offset that is not itself a line start belongs to the line before it.
	// Since the table always starts at zero and the offset is not negative,
	// the index it yields is never zero in that case.
	line, exact := slices.BinarySearch(starts, offset)
	if !exact {
		line--
	}
	return Position{Line: line + 1, Column: offset - starts[line] + 1}
}

// dottedKey renders a node's key parts as a dotted path. A Table's key parts
// are the header it declares, a KeyValue's are the name on its left-hand side,
// dotted keys included.
func dottedKey(node *unstable.Node) string {
	var parts []string
	iterator := node.Key()
	for iterator.Next() {
		parts = append(parts, string(iterator.Node().Data))
	}
	return strings.Join(parts, ".")
}

// joinKey appends a key to a scope, tolerating an empty scope for the
// top-level keys that precede any table header.
func joinKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	if key == "" {
		return prefix
	}
	return prefix + "." + key
}
