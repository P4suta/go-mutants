// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

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
	if !strings.Contains(text, "internal/scan/scan.go  4 sites") {
		t.Errorf("`run --explain` no longer names the file and its count:\n%s", text)
	}
	if strings.Contains(text, "scan.go:") {
		t.Errorf("`run --explain` printed a coordinate the report does not carry:\n%s", text)
	}
}

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

const wantSkipDetail = `
suppressed sites (16)
discovery passed these over; they are never candidates, so they are in no score

array-length 2 sites
  the expression is an array length, which is part of a type and is evaluated by the compiler rather than at run time
  suppressed/suppressed.go:33:28 lt-to-le
  suppressed/suppressed.go:33:33 true-to-false

const-decl 4 sites
  the expression is inside a const declaration, where a constant has to stay constant and one edit can renumber a whole iota block
  suppressed/suppressed.go:18:12 true-to-false
  suppressed/suppressed.go:20:13 gt-to-ge
  suppressed/suppressed.go:27:18 le-to-lt
  suppressed/suppressed.go:65:18 gt-to-ge

generated 1 site
  the file says it is generated, so an edit here would measure the generator's tests and be overwritten by its next run
  generated/generated.go

package-var-init 4 sites
  the expression initialises a package-level variable, where initialisation order is a global property a per-mutant guard cannot express in v1
  suppressed/suppressed.go:41:19 lt-to-le
  suppressed/suppressed.go:44:15 true-to-false
  suppressed/suppressed.go:49:33 return-true
  suppressed/suppressed.go:49:35 eq-to-neq

type-param 5 sites
  the expression is inside a type parameter list, a constraint, or a type argument, which hold types rather than values
  generics/generics.go:24:27 true-to-false
  generics/generics.go:31:28 false-to-true
  generics/generics.go:38:27 true-to-false
  generics/generics.go:55:25 true-to-false
  generics/generics.go:55:51 false-to-true
`

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
		lineNumber, rest, _ := strings.Cut(position, ":")
		at, err := strconv.Atoi(lineNumber)
		if err != nil {
			t.Fatalf("%q does not carry a line number: %v", row, err)
		}
		column, rule, hasRule := strings.Cut(rest, " ")
		if !hasRule || rule == "" {
			t.Errorf("%q names no rule, so a reader cannot tell it from the row beside it", row)
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
