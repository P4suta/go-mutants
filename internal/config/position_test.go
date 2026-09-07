// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2/unstable"
)

// The document below is written with explicit \n and numbered in the comment,
// because every assertion in this file is a line and a column and both would
// move under a CRLF checkout.
//
//	 1  version = 1
//	 2  mutation.profile = "balanced"
//	 3
//	 4  [mutation]
//	 5  include = ["a/**/*.go", "b.go"]
//	 6
//	 7  [[mutation.expect]]
//	 8  id = "aa"
//	 9  reason = "first"
//	10
//	11  [[mutation.expect]]
//	12  id = "bb"
//	13  reason = "second"
//	14
//	15  [report]
//	16  formats = [ "json",
//	17              "html" ]
//	18  high = 80
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
		// A scalar is located at its value, so the caret lands under what is
		// wrong rather than under the name of the thing that is wrong.
		{"version", 1, 11},
		{"report.high", 18, 8},
		// A dotted key before any table header keeps its full path.
		{"mutation.profile", 2, 20},
		// An array has no bytes of its own in this parser. Its key position is
		// the fallback, and the thing that must not happen is 1:1.
		{"mutation.include", 5, 1},
		{"mutation.include[0]", 5, 12},
		{"mutation.include[1]", 5, 25},
		// Repeated tables are numbered by occurrence, which is how a ledger
		// row is named in a diagnostic.
		{"mutation.expect[0].id", 8, 6},
		{"mutation.expect[0].reason", 9, 10},
		{"mutation.expect[1].id", 12, 6},
		{"mutation.expect[1].reason", 13, 10},
		// An element of a multi-line array is located on its own line.
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

	// Nothing may be silently parked at the top of the file: that is what an
	// unguarded empty range would produce, and it would be a wrong position
	// rather than an absent one.
	for key, position := range positions {
		if key != "version" && position.Line == 1 && position.Column == 1 {
			t.Errorf("%s is recorded at 1:1, which is where an empty range resolves", key)
		}
	}
}

// The inline spelling of a repeated table is as diagnosable as the block one,
// so that a generated configuration is not second-class.
func TestIndexPositionsInlineTables(t *testing.T) {
	//  1  version = 1
	//  2  [mutation]
	//  3  expect = [ { id = "aa", reason = "r1" },
	//  4             { id = "bb", reason = "r2" } ]
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
		// The element itself is located at its opening brace, and each of its
		// values at the value, exactly as in the block spelling.
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

// The walk is a diagnostic aid, not a parser. It must survive anything without
// panicking, because it runs on documents the decoder has already accepted and
// a crash here would replace a good error message with a stack trace.
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

// Every line and column this package prints comes out of positionAt, so it has
// to answer exactly what a scan from the top of the file would answer — for
// every offset in the file, not for the handful a document happens to use.
//
// The three places an off-by-one could hide are all covered by walking the
// whole range: offset zero, an offset that is itself the start of a line
// (where the binary search reports an exact hit and must not step back), and
// the offset one past the last byte.
func TestPositionAtMatchesAScanFromTheTop(t *testing.T) {
	for _, document := range []string{
		"",
		"\n",
		"\n\n\n",
		"version = 1\n",
		"version = 1",
		// A CRLF file: the carriage return belongs to the line it ends and
		// occupies a column, which is what the decoder's own counting does.
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

// scanToOffset counts a line and a column the way go-toml counts them: a line
// ends at "\n" and a column is a byte offset into one.
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

// BenchmarkIndexPositions is here to keep the cost of the position walk
// visible. It is linear in the size of the document, and the version that was
// not — one rescan from the top of the file per key — took nine seconds on the
// largest input below.
func BenchmarkIndexPositions(b *testing.B) {
	document := []byte("version = 1\n" + strings.Repeat("[[a]]\nb = 1\n", 16000))
	b.SetBytes(int64(len(document)))
	for b.Loop() {
		indexPositions(document)
	}
}

// A key that was never written has no position, and a document that failed to
// parse contributes whatever it managed before giving up rather than
// pretending to know more.
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

// maxIndexDepth is a bound on a diagnostic aid rather than on the schema, so
// nothing else in this repository can be relied on to reach it: the deepest
// path a valid configuration produces is `mutation.expect[i].id`, three levels
// down. What the bound is for is a document that decoded but nests further
// than anybody meant to, and the only way to see it hold is to write one.
//
// The two documents below are one level apart on purpose. A walk that stopped
// a level early would leave a value the schema could still name without a
// position, and a walk whose depth never grew would not stop at all — and a
// diagnostic aid that recurses without a bound is the stack overflow this
// constant exists to prevent.
func TestIndexPositionsStopsAtTheDepthBound(t *testing.T) {
	// recordValue is entered at depth 0 for the value of a top-level key, so
	// the deepest container it still walks is the one at level
	// maxIndexDepth+1, and a value nested one level below that has no
	// position at all.
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

	// Inline tables are walked by the other half of recordValue and are
	// bounded by the same constant, which is what keeps
	// `expect = [{ id = "…" }]` as diagnosable as the block spelling without
	// making it the way in past the bound.
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

// record is the only place a position is written down, and its guards are
// each about a wrong answer rather than a missing one: a key nobody can name,
// and a node whose empty range would resolve to line 1, column 1 and send
// every array diagnostic to the top of the file.
//
// None of this is reachable through indexPositions, which is the point. The
// walk never hands record an empty key, and the guards are what make that
// safe to keep believing — so they are asserted here, one call at a time.
func TestRecordLeavesOutWhatItCannotLocate(t *testing.T) {
	located := &unstable.Node{Kind: unstable.String, Raw: unstable.Range{Offset: 7, Length: 3}}

	offsets := map[string]int{}
	record(offsets, "report.high", located)
	if got, ok := offsets["report.high"]; !ok || got != 7 {
		t.Fatalf("offsets[report.high] = %d, %v; want 7, true", got, ok)
	}

	// A value nobody can name by key is a value no diagnostic can ask for.
	record(offsets, "", located)
	if _, ok := offsets[""]; ok {
		t.Errorf("record wrote an entry under the empty key")
	}

	// An array carries an empty range in this parser, and an empty range
	// starts at offset zero: a position that is wrong rather than absent.
	record(offsets, "mutation.include", &unstable.Node{Kind: unstable.Array, Raw: unstable.Range{Offset: 7}})
	if _, ok := offsets["mutation.include"]; ok {
		t.Errorf("record located a node that carries no bytes")
	}

	// A missing node is survivable, because a missing position only ever
	// degrades a message.
	record(offsets, "report.low", nil)
	if _, ok := offsets["report.low"]; ok {
		t.Errorf("record located a node that is not there")
	}

	// go-toml matches struct fields case-insensitively, so the index has to
	// hold the canonical spelling the validators name.
	record(offsets, "Cache.Directory", located)
	if _, ok := offsets["cache.directory"]; !ok {
		t.Errorf("record did not canonicalise the key case")
	}

	if len(offsets) != 2 {
		t.Errorf("offsets = %v, want exactly the two keys that could be located", offsets)
	}
}

// recordValue's own guard is about a node the parser never filled in. Its Kind
// is Invalid, so it has neither children to walk nor a value to point at, and
// walking it anyway would file its empty range under a key.
func TestRecordValueIgnoresANodeThatIsNotThere(t *testing.T) {
	offsets := map[string]int{}
	recordValue(offsets, "report.high", nil, 0)
	recordValue(offsets, "report.low", &unstable.Node{Raw: unstable.Range{Offset: 7, Length: 3}}, 0)
	if len(offsets) != 0 {
		t.Errorf("recordValue located a node it could not read: %v", offsets)
	}
}

// joinKey is what makes a scope and a name into the path a validator uses. The
// empty-key case is the one indexPositions never produces and the one a
// caller would produce first: an array-of-tables scope with nothing written
// under it yet is named by the scope.
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
