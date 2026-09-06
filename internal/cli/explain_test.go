// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The half of `--explain` that needs no toolchain: how the two commands render
// what discovery decided, given rows to render.
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/discover"
	"github.com/P4suta/go-mutants/internal/report"
	"github.com/P4suta/go-mutants/internal/testkit"
)

// TestRunExplainSaysWhereCoordinatesLive is the one line that keeps the two
// commands honest about the difference between them.
//
// A run report carries the aggregate and nothing else — that is a deliberate
// decision about a document other tools read, argued in internal/discover — so
// `run --explain` can only name the file. Leaving it at that would read as "the
// coordinates do not exist", which stopped being true: `list --explain` over
// the same workspace prints every one of them, and this is where a reader is
// told so.
func TestRunExplainSaysWhereCoordinatesLive(t *testing.T) {
	var out bytes.Buffer
	r := &report.Report{Skips: []report.Skip{
		{Path: "internal/scan/scan.go", Reason: "const-decl", Count: 4},
	}}
	if err := explainRun(&out, false, r); err != nil {
		t.Fatalf("explainRun: %v", err)
	}

	text := out.String()
	if !strings.Contains(text, "list --explain") {
		t.Errorf("`run --explain` does not say where the coordinates are:\n%s", text)
	}
	// The rows themselves stay per file: the report has no sites in it, and a
	// coordinate invented here would be a coordinate nothing measured.
	if !strings.Contains(text, "internal/scan/scan.go  4 sites") {
		t.Errorf("`run --explain` no longer names the file and its count:\n%s", text)
	}
	if strings.Contains(text, "scan.go:") {
		t.Errorf("`run --explain` printed a coordinate the report does not carry:\n%s", text)
	}
}

// TestListExplainPrintsAWholeFileSkipWithoutACoordinate pins the shape of the
// row a file that was never opened gets.
//
// `path:0:0` would be a position, and there is no position: the file was
// excluded, generated or cgo, so discovery never looked inside it. The bare
// path is the honest row, and the count line above it already says how many
// files a reason accounted for.
func TestListExplainPrintsAWholeFileSkipWithoutACoordinate(t *testing.T) {
	var out bytes.Buffer
	skips := []catalogSkip{
		{Path: "gen/gen.go", Reason: "generated", Count: 1},
		{Path: "scan/scan.go", Reason: "const-decl", Count: 1},
	}
	sites := []discover.SkipSite{
		{Path: "gen/gen.go", Reason: discover.SkipGenerated},
		{Path: "scan/scan.go", Reason: discover.SkipConstDecl, Line: 12, Column: 7},
	}
	if err := explainListing(&out, false, skips, sites); err != nil {
		t.Fatalf("explainListing: %v", err)
	}

	text := out.String()
	if !strings.Contains(text, "\n  gen/gen.go\n") {
		t.Errorf("the generated file is not listed as a bare path:\n%s", text)
	}
	if strings.Contains(text, "gen/gen.go:") {
		t.Errorf("a file that was never opened was given a coordinate:\n%s", text)
	}
	if !strings.Contains(text, "\n  scan/scan.go:12:7\n") {
		t.Errorf("the suppressed expression carries no coordinate:\n%s", text)
	}
}

// wantSkipDetail is every row `list --explain` writes underneath a listing of
// fixtures/discovery, byte for byte.
//
// It is spelled out here rather than kept as a golden file for one reason: the
// repository's golden ledger names the packages `mise run golden-update` can
// regenerate, and the run that produces this output needs a real toolchain
// behind the integration tag, which that task does not carry. A literal is the
// same pin — an exact comparison of the whole section — read in the file that
// asserts it. TestListExplainPrintsSkipCoordinates compares a real run against
// it; the test below proves the rows name what they claim to.
//
// The coordinates are the fixture's own and can be checked with an editor,
// which is the point: line 33 holds two suppressed expressions inside one array
// length, line 49 holds three inside one package-level initialiser — a
// comparison and the two constants a return could be rewritten to — and the
// generated file has no coordinate at all, because it was never opened.
const wantSkipDetail = `
suppressed sites (21)
discovery passed these over; they are never candidates, so they are in no score

array-length 2 sites
  the expression is an array length, which is part of a type and is evaluated by the compiler rather than at run time
  suppressed/suppressed.go:33:28
  suppressed/suppressed.go:33:33

case-label 4 sites
  the expression labels a switch case or a select clause, which v1 leaves alone; the bodies underneath them are mutated
  suppressed/suppressed.go:76:9
  suppressed/suppressed.go:80:10
  suppressed/suppressed.go:80:13
  suppressed/suppressed.go:97:16

const-decl 4 sites
  the expression is inside a const declaration, where a constant has to stay constant and one edit can renumber a whole iota block
  suppressed/suppressed.go:18:12
  suppressed/suppressed.go:20:13
  suppressed/suppressed.go:27:18
  suppressed/suppressed.go:65:18

generated 1 site
  the file says it is generated, so an edit here would measure the generator's tests and be overwritten by its next run
  generated/generated.go

package-var-init 5 sites
  the expression initialises a package-level variable, where initialisation order is a global property a per-mutant guard cannot express in v1
  suppressed/suppressed.go:41:19
  suppressed/suppressed.go:44:15
  suppressed/suppressed.go:49:33
  suppressed/suppressed.go:49:33
  suppressed/suppressed.go:49:35

type-param 5 sites
  the expression is inside a type parameter list, a constraint, or a type argument, which hold types rather than values
  generics/generics.go:24:27
  generics/generics.go:31:28
  generics/generics.go:38:27
  generics/generics.go:55:25
  generics/generics.go:55:51
`

// TestListExplainCoordinatesLandOnTheirOwnFixtureLines reads the fixture back
// and checks that every coordinate in [wantSkipDetail] names an expression in
// it.
//
// The literal pins the rows; this pins that they are true. The byte at the
// column is what does it: a bound on the line's width would be satisfied by
// every coordinate one to the right of where discovery meant, which is exactly
// the drift a table of numbers cannot see on its own. A suppressed edit starts
// at an operator or at the first byte of an expression, so whitespace there is
// a coordinate that has stopped naming a site.
//
// It needs no toolchain and reads the fixture where it lies, so it stays in the
// fast tier: this is the guard that would catch a coordinate change, and it
// should not wait for the integration run to say so.
func TestListExplainCoordinatesLandOnTheirOwnFixtureLines(t *testing.T) {
	root := testkit.Fixture(t, "discovery")

	rows := 0
	for _, text := range strings.Split(wantSkipDetail, "\n") {
		row, ok := strings.CutPrefix(text, "  ")
		if !ok || !strings.Contains(row, ".go:") {
			continue
		}
		rows++
		path, position, _ := strings.Cut(row, ".go:")
		source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path+".go")))
		if err != nil {
			t.Fatalf("reading the fixture file %q names: %v", row, err)
		}
		lineNumber, column, _ := strings.Cut(position, ":")
		at, err := strconv.Atoi(lineNumber)
		if err != nil {
			t.Fatalf("%q does not carry a line number: %v", row, err)
		}
		col, err := strconv.Atoi(column)
		if err != nil {
			t.Fatalf("%q does not carry a column: %v", row, err)
		}
		lines := strings.Split(string(source), "\n")
		if at < 1 || at > len(lines) {
			t.Errorf("%s names line %d of a file with %d lines", row, at, len(lines))
			continue
		}
		line := strings.TrimSuffix(lines[at-1], "\r")
		if col < 1 || col > len(line) {
			t.Errorf("%s names column %d of a line %d bytes long: %q", row, col, len(line), line)
			continue
		}
		if c := line[col-1]; c == ' ' || c == '\t' {
			t.Errorf("%s points at whitespace rather than at a suppressed expression: %q", row, line)
		}
	}
	if rows == 0 {
		t.Fatal("the expected detail holds no coordinates, so this test proved nothing")
	}
}
