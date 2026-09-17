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

const maxIndexDepth = 16

func indexPositions(document []byte) map[string]Position {
	offsets := make(map[string]int)
	parser := &unstable.Parser{}
	parser.Reset(document)

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
			record(offsets, key, expression)
			recordValue(offsets, key, expression.Value(), 0)
		default:
		}
	}
	return resolvePositions(document, offsets)
}

func recordValue(offsets map[string]int, key string, value *unstable.Node, depth int) {
	if value == nil || !value.Valid() || depth > maxIndexDepth {
		return
	}
	switch value.Kind {
	case unstable.Array:
		element := value.Children()
		for i := 0; element.Next(); i++ {
			child := element.Node()
			childKey := key + "[" + strconv.Itoa(i) + "]"
			record(offsets, childKey, child)
			recordValue(offsets, childKey, child, depth+1)
		}
	case unstable.InlineTable:
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
		record(offsets, key, value)
	}
}

func record(offsets map[string]int, key string, node *unstable.Node) {
	if key == "" || node == nil || node.Raw.Length == 0 {
		return
	}
	key = strings.ToLower(key)
	offsets[key] = int(node.Raw.Offset)
}

func resolvePositions(document []byte, offsets map[string]int) map[string]Position {
	positions := make(map[string]Position, len(offsets))
	starts := lineStarts(document)
	for key, offset := range offsets {
		positions[key] = positionAt(starts, offset)
	}
	return positions
}

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

func positionAt(starts []int, offset int) Position {
	if offset < 0 {
		return Position{}
	}
	line, exact := slices.BinarySearch(starts, offset)
	if !exact {
		line--
	}
	return Position{Line: line + 1, Column: offset - starts[line] + 1}
}

func dottedKey(node *unstable.Node) string {
	var parts []string
	iterator := node.Key()
	for iterator.Next() {
		parts = append(parts, string(iterator.Node().Data))
	}
	return strings.Join(parts, ".")
}

func joinKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	if key == "" {
		return prefix
	}
	return prefix + "." + key
}
