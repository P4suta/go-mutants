// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2/unstable"
)

const positionDocument = "version = 1\n" +
	"mutation.profile = \"balanced\"\n" +
	"\n" +
	"[mutation]\n" +
	"include = [\"a/**/*.go\", \"b.go\"]\n" +
	"\n" +
	"[[mutation.expect]]\n" +
	"id = \"aa\"\n" +
	"reason = \"first\"\n" +
	"\n" +
	"[[mutation.expect]]\n" +
	"id = \"bb\"\n" +
	"reason = \"second\"\n" +
	"\n" +
	"[report]\n" +
	"formats = [ \"json\",\n" +
	"            \"html\" ]\n" +
	"high = 80\n"

func TestIndexPositions(t *testing.T) {
	positions := indexPositions([]byte(positionDocument))

	tests := []struct {
		key    string
		line   int
		column int
	}{
		{"version", 1, 11},
		{"report.high", 18, 8},
		{"mutation.profile", 2, 20},
		{"mutation.include", 5, 1},
		{"mutation.include[0]", 5, 12},
		{"mutation.include[1]", 5, 25},
		{"mutation.expect[0].id", 8, 6},
		{"mutation.expect[0].reason", 9, 10},
		{"mutation.expect[1].id", 12, 6},
		{"mutation.expect[1].reason", 13, 10},
		{"report.formats", 16, 1},
		{"report.formats[0]", 16, 13},
		{"report.formats[1]", 17, 13},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			got, ok := positions[test.key]
			if !ok {
				t.Fatalf("no position recorded")
			}
			if got.Line != test.line || got.Column != test.column {
				t.Errorf("position = %d:%d, want %d:%d", got.Line, got.Column, test.line, test.column)
			}
		})
	}

	for key, position := range positions {
		if key != "version" && position.Line == 1 && position.Column == 1 {
			t.Errorf("%s is recorded at 1:1, which is where an empty range resolves", key)
		}
	}
}

func TestIndexPositionsInlineTables(t *testing.T) {
	document := "version = 1\n" +
		"[mutation]\n" +
		"expect = [ { id = \"aa\", reason = \"r1\" },\n" +
		"           { id = \"bb\", reason = \"r2\" } ]\n"
	positions := indexPositions([]byte(document))

	for _, test := range []struct {
		key    string
		line   int
		column int
	}{
		{"mutation.expect[0]", 3, 12},
		{"mutation.expect[0].id", 3, 19},
		{"mutation.expect[0].reason", 3, 34},
		{"mutation.expect[1]", 4, 12},
		{"mutation.expect[1].id", 4, 19},
		{"mutation.expect[1].reason", 4, 34},
	} {
		got, ok := positions[test.key]
		if !ok {
			t.Errorf("no position recorded for %s", test.key)
			continue
		}
		if got.Line != test.line || got.Column != test.column {
			t.Errorf("%s = %d:%d, want %d:%d", test.key, got.Line, got.Column, test.line, test.column)
		}
	}
}

func TestIndexPositionsUsesCanonicalKeyCase(t *testing.T) {
	positions := indexPositions([]byte("version=1\n[CAChe]\ndireCtorY=\"./A:\""))
	got, ok := positions["cache.directory"]
	if !ok {
		t.Fatal("mixed-case known key has no canonical position")
	}
	if got.Line != 3 || got.Column != 11 {
		t.Fatalf("position = %d:%d, want 3:11", got.Line, got.Column)
	}
}

func TestIndexPositionsIsTotal(t *testing.T) {
	for _, document := range []string{
		"",
		"\n\n\n",
		"# just a comment\n",
		"[mutation",
		"= 1\n",
		"a = [[[[[1]]]]]\n",
		"a = { b = { c = { d = 1 } } }\n",
		"a.b.c.d = 1\n",
		"[[a]]\n[[a]]\n[[a]]\nb = 1\n",
		"a = \"\"\"multi\nline\"\"\"\nb = 2\n",
		strings.Repeat("[[a]]\nb = 1\n", 200),
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("indexPositions(%q) panicked: %v", document, r)
				}
			}()
			indexPositions([]byte(document))
		}()
	}
}

func TestPositionAtMatchesAScanFromTheTop(t *testing.T) {
	for _, document := range []string{
		"",
		"\n",
		"\n\n\n",
		"version = 1\n",
		"version = 1",
		"version = 1\r\n[report]\r\nhigh = 80\r\n",
		"a = 1\n\n\nb = 2\n",
		positionDocument,
	} {
		starts := lineStarts([]byte(document))
		for offset := 0; offset <= len(document); offset++ {
			line, column := scanToOffset(document, offset)
			got := positionAt(starts, offset)
			if got.Line != line || got.Column != column {
				t.Fatalf("positionAt(%q, %d) = %d:%d, want %d:%d",
					document, offset, got.Line, got.Column, line, column)
			}
		}
	}
}

func scanToOffset(document string, offset int) (line, column int) {
	line, column = 1, 1
	for _, b := range []byte(document[:offset]) {
		if b == '\n' {
			line++
			column = 1
			continue
		}
		column++
	}
	return line, column
}

func BenchmarkIndexPositions(b *testing.B) {
	document := []byte("version = 1\n" + strings.Repeat("[[a]]\nb = 1\n", 16000))
	b.SetBytes(int64(len(document)))
	for b.Loop() {
		indexPositions(document)
	}
}

func TestIndexPositionsUnknownKeys(t *testing.T) {
	positions := indexPositions([]byte("version = 1\n[report]\nhigh = 80\n"))
	if _, ok := positions["report.low"]; ok {
		t.Errorf("a key that is not in the document has a position")
	}
	truncated := indexPositions([]byte("version = 1\n[report]\nhigh = 80\nlow = \n"))
	if got, ok := truncated["version"]; !ok || got.Line != 1 {
		t.Errorf("a truncated document lost the keys it did parse: %v %v", got, ok)
	}
}

func TestIndexPositionsStopsAtTheDepthBound(t *testing.T) {
	deepest := maxIndexDepth + 1

	nestedArrays := func(levels int) string {
		return "a = " + strings.Repeat("[", levels) + "1" + strings.Repeat("]", levels) + "\n"
	}
	arrayKey := func(levels int) string { return "a" + strings.Repeat("[0]", levels) }

	if _, ok := indexPositions([]byte(nestedArrays(deepest)))[arrayKey(deepest)]; !ok {
		t.Errorf("an element nested %d arrays deep has no position: the walk stopped early", deepest)
	}
	if _, ok := indexPositions([]byte(nestedArrays(deepest + 1)))[arrayKey(deepest+1)]; ok {
		t.Errorf("an element nested %d arrays deep has a position: the bound did not hold", deepest+1)
	}

	nestedTables := func(levels int) string {
		return "a = " + strings.Repeat("{ b = ", levels) + "1" + strings.Repeat(" }", levels) + "\n"
	}
	tableKey := func(levels int) string { return "a" + strings.Repeat(".b", levels) }

	if _, ok := indexPositions([]byte(nestedTables(deepest)))[tableKey(deepest)]; !ok {
		t.Errorf("a value nested %d inline tables deep has no position: the walk stopped early", deepest)
	}
	if _, ok := indexPositions([]byte(nestedTables(deepest + 1)))[tableKey(deepest+1)]; ok {
		t.Errorf("a value nested %d inline tables deep has a position: the bound did not hold", deepest+1)
	}
}

func TestRecordLeavesOutWhatItCannotLocate(t *testing.T) {
	located := &unstable.Node{Kind: unstable.String, Raw: unstable.Range{Offset: 7, Length: 3}}

	offsets := map[string]int{}
	record(offsets, "report.high", located)
	if got, ok := offsets["report.high"]; !ok || got != 7 {
		t.Fatalf("offsets[report.high] = %d, %v; want 7, true", got, ok)
	}

	record(offsets, "", located)
	if _, ok := offsets[""]; ok {
		t.Errorf("record wrote an entry under the empty key")
	}

	record(offsets, "mutation.include", &unstable.Node{Kind: unstable.Array, Raw: unstable.Range{Offset: 7}})
	if _, ok := offsets["mutation.include"]; ok {
		t.Errorf("record located a node that carries no bytes")
	}

	record(offsets, "report.low", nil)
	if _, ok := offsets["report.low"]; ok {
		t.Errorf("record located a node that is not there")
	}

	record(offsets, "Cache.Directory", located)
	if _, ok := offsets["cache.directory"]; !ok {
		t.Errorf("record did not canonicalise the key case")
	}

	if len(offsets) != 2 {
		t.Errorf("offsets = %v, want exactly the two keys that could be located", offsets)
	}
}

func TestRecordValueIgnoresANodeThatIsNotThere(t *testing.T) {
	offsets := map[string]int{}
	recordValue(offsets, "report.high", nil, 0)
	recordValue(offsets, "report.low", &unstable.Node{Raw: unstable.Range{Offset: 7, Length: 3}}, 0)
	if len(offsets) != 0 {
		t.Errorf("recordValue located a node it could not read: %v", offsets)
	}
}

func TestJoinKey(t *testing.T) {
	for _, test := range []struct{ prefix, key, want string }{
		{"", "version", "version"},
		{"mutation", "profile", "mutation.profile"},
		{"mutation.expect[0]", "id", "mutation.expect[0].id"},
		{"mutation.expect[0]", "", "mutation.expect[0]"},
		{"", "", ""},
	} {
		if got := joinKey(test.prefix, test.key); got != test.want {
			t.Errorf("joinKey(%q, %q) = %q, want %q", test.prefix, test.key, got, test.want)
		}
	}
}
