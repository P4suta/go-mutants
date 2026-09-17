// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func writeCatalog(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(document), filemode.ReadableFile); err != nil {
		t.Fatalf("write the catalog: %v", err)
	}
	return path
}

func TestReadCatalogReadsWhatTheLayerNeedsAndIgnoresTheRest(t *testing.T) {
	t.Parallel()

	path := writeCatalog(t, `{
	  "document_type": "go-mutants/catalog",
	  "schema_version": 1,
	  "tool_version": "0.0.0-fixture",
	  "workspace": {"module_path": "example.com/audited"},
	  "mutants": [
	    {"id": "`+firstMutant+`", "display_id": "`+firstDisplay+`", "path": "`+subjectPath+`",
	     "rule": "or-to-and", "line": 20, "column": 4, "original": "||", "replacement": "&&",
	     "branch": {"direction": "decreasing", "body_start_line": 20, "body_start_column": 15,
	                "body_end_line": 22, "body_end_column": 3, "note": "a field this audit does not read"}},
	    {"id": "`+secondMutant+`", "path": "`+subjectPath+`", "line": 30, "column": 4}
	  ],
	  "skips": [{"path": "vendor/other.go", "reason": "excluded"}]
	}`)

	catalog, err := readCatalog(path)
	if err != nil {
		t.Fatalf("read the catalog: %v", err)
	}
	proved, listed := catalog.lookup(firstMutant)
	if !listed {
		t.Fatalf("the catalog does not list %s", firstMutant)
	}
	if proved.Path != subjectPath || proved.Line != 20 || proved.Column != 4 {
		t.Errorf("the catalog placed the mutant at %s:%d:%d, want %s:20:4",
			proved.Path, proved.Line, proved.Column, subjectPath)
	}
	if proved.Branch == nil {
		t.Fatal("the catalog carried no proof for a mutant that has one")
	}
	if *proved.Branch != *gatedBody(20, 15, 22, 3) {
		t.Errorf("the proof names the body %+v, want the span the document carried", *proved.Branch)
	}
	unproved, listed := catalog.lookup(secondMutant)
	if !listed {
		t.Fatalf("the catalog does not list %s", secondMutant)
	}
	if unproved.Branch != nil {
		t.Errorf("a mutant the document gave no proof for carries %+v", *unproved.Branch)
	}
	if _, listed := catalog.lookup(thirdMutant); listed {
		t.Error("the catalog lists a mutant the document never named")
	}
}

func TestReadCatalogRefusesADocumentItCannotBeSureOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		document string
		want     []string
	}{
		{
			name:     "another kind of document",
			document: `{"document_type": "go-mutants/report", "schema_version": 1, "mutants": []}`,
			want:     []string{"go-mutants/report", "go-mutants/catalog"},
		},
		{
			name:     "a schema this audit does not read",
			document: `{"document_type": "go-mutants/catalog", "schema_version": 2, "mutants": []}`,
			want:     []string{"2", "1"},
		},
		{
			name:     "a document that names neither",
			document: `{"mutants": []}`,
			want:     []string{"go-mutants/catalog"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := writeCatalog(t, testCase.document)

			_, err := readCatalog(path)
			if err == nil {
				t.Fatalf("readCatalog accepted %s", testCase.document)
			}
			for _, want := range append(testCase.want, path) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error is %q, want it to name %q", err, want)
				}
			}
		})
	}
}

func TestReadCatalogReportsADocumentItCannotRead(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent.json")
	switch _, err := readCatalog(missing); {
	case err == nil:
		t.Error("a missing catalog was accepted")
	case !strings.Contains(err.Error(), missing):
		t.Errorf("the error is %q, want it to name the file it could not read", err)
	case !errors.Is(err, fs.ErrNotExist):
		t.Errorf("the error is %q, want it to say the file is not there", err)
	}

	broken := writeCatalog(t, `{"document_type": "go-mutants/catalog",`)
	var syntax *json.SyntaxError
	switch _, err := readCatalog(broken); {
	case err == nil:
		t.Error("a truncated catalog was accepted")
	case !strings.Contains(err.Error(), broken):
		t.Errorf("the error is %q, want it to name the file it could not read", err)
	case !errors.As(err, &syntax) && !errors.Is(err, io.ErrUnexpectedEOF):
		t.Errorf("the error is %q, want it to say the document is not JSON", err)
	}
}

func TestALookupWithoutACatalogListsNothing(t *testing.T) {
	t.Parallel()

	var absent *mutantCatalog
	if _, listed := absent.lookup(firstMutant); listed {
		t.Error("a run audited without a catalog listed a mutant")
	}
}

const (
	proofMutantLine   = 4
	proofMutantColumn = 2
	proofBodyStart    = 5
	proofBodyColumn   = 3
	proofBodyEnd      = 9
	proofBodyEndCol   = 1
	insideBodyLine    = 6

	firstBodyStartColumn = 2
	firstBodyEndColumn   = 3
)

func provingMutant(change func(*catalogMutant)) catalogMutant {
	listed := catalogMutant{
		ID: "m-1", Line: proofMutantLine, Column: proofMutantColumn,
		Branch: &branchProof{
			BodyStartLine: proofBodyStart, BodyStartColumn: proofBodyColumn,
			BodyEndLine: proofBodyEnd, BodyEndColumn: proofBodyEndCol,
		},
	}
	if change != nil {
		change(&listed)
	}
	return listed
}

func TestAListedMutantProvesABranchOnlyWhenItsPositionsMakeSense(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*catalogMutant)
		proves bool
	}{
		{name: "a branch the mutant opens", proves: true},
		{
			name: "a branch that opens at the very first position",
			change: func(m *catalogMutant) {
				m.Line, m.Column = 1, 1
				m.Branch.BodyStartLine, m.Branch.BodyStartColumn = 1, firstBodyStartColumn
				m.Branch.BodyEndLine, m.Branch.BodyEndColumn = 1, firstBodyEndColumn
			},
			proves: true,
		},
		{
			name: "a body that opens in the first column of its line",
			change: func(m *catalogMutant) {
				m.Line, m.Column = proofBodyStart-1, proofMutantColumn
				m.Branch.BodyStartColumn = 1
			},
			proves: true,
		},
		{name: "no branch at all", change: func(m *catalogMutant) { m.Branch = nil }},
		{name: "a body that starts on line zero", change: func(m *catalogMutant) { m.Branch.BodyStartLine = 0 }},
		{name: "a body that starts at column zero", change: func(m *catalogMutant) { m.Branch.BodyStartColumn = 0 }},
		{name: "a body that ends on line zero", change: func(m *catalogMutant) { m.Branch.BodyEndLine = 0 }},
		{name: "a body that ends at column zero", change: func(m *catalogMutant) { m.Branch.BodyEndColumn = 0 }},
		{name: "a mutant on line zero", change: func(m *catalogMutant) { m.Line = 0 }},
		{name: "a mutant at column zero", change: func(m *catalogMutant) { m.Column = 0 }},
		{
			name:   "a body that ends before it starts",
			change: func(m *catalogMutant) { m.Branch.BodyEndLine = m.Branch.BodyStartLine - 1 },
		},
		{
			name: "a body that ends before it starts by a column",
			change: func(m *catalogMutant) {
				m.Branch.BodyEndLine, m.Branch.BodyEndColumn = proofBodyStart, proofMutantColumn
			},
		},
		{
			name:   "a mutant inside the body it is said to open",
			change: func(m *catalogMutant) { m.Line, m.Column = insideBodyLine, proofBodyEndCol },
		},
		{
			name:   "a mutant exactly where the body starts",
			change: func(m *catalogMutant) { m.Line, m.Column = proofBodyStart, proofBodyColumn },
		},
		{
			name:   "a mutant a column before the body starts",
			change: func(m *catalogMutant) { m.Line, m.Column = proofBodyStart, proofMutantColumn },
			proves: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			listed := provingMutant(test.change)
			proof := listed.proves()
			if (proof != nil) != test.proves {
				t.Fatalf("proves() = %+v, want a proof: %t", proof, test.proves)
			}
			if proof != nil && *proof != *listed.Branch {
				t.Fatalf("proof = %+v, want the branch it listed %+v", *proof, *listed.Branch)
			}
		})
	}
}

func TestABranchHoldsExactlyThePositionsBetweenItsEnds(t *testing.T) {
	t.Parallel()
	body := branchProof{BodyStartLine: 5, BodyStartColumn: 3, BodyEndLine: 9, BodyEndColumn: 4}
	for _, test := range []struct {
		name   string
		line   int
		column int
		holds  bool
	}{
		{name: "the first position", line: 5, column: 3, holds: true},
		{name: "the last position", line: 9, column: 4, holds: true},
		{name: "inside", line: 7, column: 1, holds: true},
		{name: "a column before the start", line: 5, column: 2},
		{name: "a line before the start", line: 4, column: 99},
		{name: "a column after the end", line: 9, column: 5},
		{name: "a line after the end", line: 10, column: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := body.holds(test.line, test.column); got != test.holds {
				t.Fatalf("holds(%d, %d) = %t, want %t", test.line, test.column, got, test.holds)
			}
		})
	}
}

func TestPositionBeforeComparesTheLineFirstAndThenTheColumn(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                   string
		line, column           int
		otherLine, otherColumn int
		before                 bool
	}{
		{name: "an earlier line", line: 4, column: 99, otherLine: 5, otherColumn: 1, before: true},
		{name: "a later line", line: 6, column: 1, otherLine: 5, otherColumn: 99},
		{name: "the same line, an earlier column", line: 5, column: 1, otherLine: 5, otherColumn: 2, before: true},
		{name: "the same line, a later column", line: 5, column: 3, otherLine: 5, otherColumn: 2},
		{name: "the same position", line: 5, column: 2, otherLine: 5, otherColumn: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := positionBefore(test.line, test.column, test.otherLine, test.otherColumn)
			if got != test.before {
				t.Fatalf("positionBefore(%d, %d, %d, %d) = %t, want %t",
					test.line, test.column, test.otherLine, test.otherColumn, got, test.before)
			}
		})
	}
}
