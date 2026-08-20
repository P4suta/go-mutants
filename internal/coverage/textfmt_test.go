// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
)

// samplePath is a real `go tool covdata textfmt` document, produced by the
// toolchain this project pins and pinned here in turn.
//
// It is committed rather than generated at test time so that the parser is held
// against the format go1.26 actually writes, on a machine with no toolchain and
// in a run with no build. TestSampleMatchesTheToolchain, in the integration
// tests, is the other half: it generates a fresh document and checks that this
// one still describes the same shape, so a format change is caught rather than
// tested around.
var samplePath = filepath.Join("testdata", "textfmt.sample.txt")

// TestParseTextfmtReadsARealProfile pins every field of every record in the
// committed sample.
//
// The sample is the `cov.example/exp/a` test binary's profile from a two-package
// module. Two facts in it are the mapping's whole raw material, and both are
// asserted here rather than assumed downstream: the last three blocks of `a.go`
// are present with a count of zero — statements the binary linked and never
// reached — and package `b` is absent from the document altogether, because
// this binary never linked it.
func TestParseTextfmtReadsARealProfile(t *testing.T) {
	t.Parallel()

	file, err := os.Open(samplePath)
	if err != nil {
		t.Fatalf("opening the sample profile: %v", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("closing the sample profile: %v", closeErr)
		}
	}()

	profile, err := coverage.ParseTextfmt(file)
	if err != nil {
		t.Fatalf("ParseTextfmt: %v", err)
	}
	if profile.Mode != "set" {
		t.Errorf("mode = %q, want %q", profile.Mode, "set")
	}

	want := []coverage.Block{
		{File: "cov.example/exp/a/a.go", StartLine: 3, StartCol: 31, EndLine: 4, EndCol: 12, NumStmt: 1, Count: 1},
		{File: "cov.example/exp/a/a.go", StartLine: 4, StartCol: 12, EndLine: 5, EndCol: 13, NumStmt: 1, Count: 1},
		{File: "cov.example/exp/a/a.go", StartLine: 5, StartCol: 13, EndLine: 7, EndCol: 4, NumStmt: 1, Count: 1},
		{File: "cov.example/exp/a/a.go", StartLine: 8, StartCol: 3, EndLine: 8, EndCol: 16, NumStmt: 1, Count: 0},
		{File: "cov.example/exp/a/a.go", StartLine: 10, StartCol: 2, EndLine: 10, EndCol: 15, NumStmt: 1, Count: 0},
		{File: "cov.example/exp/a/a.go", StartLine: 13, StartCol: 30, EndLine: 15, EndCol: 2, NumStmt: 1, Count: 0},
	}
	if len(profile.Blocks) != len(want) {
		t.Fatalf("parsed %d blocks, want %d: %+v", len(profile.Blocks), len(want), profile.Blocks)
	}
	for i, got := range profile.Blocks {
		if got != want[i] {
			t.Errorf("block %d = %+v, want %+v", i, got, want[i])
		}
		if got.Covered() != (want[i].Count > 0) {
			t.Errorf("block %d: Covered() = %t for count %d", i, got.Covered(), got.Count)
		}
	}
}

// TestParseTextfmtAccepts covers the shapes a valid document can take that the
// sample does not happen to.
func TestParseTextfmtAccepts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		document string
		mode     string
		blocks   int
	}{
		{
			name:     "no blocks at all",
			document: "mode: set\n",
			mode:     "set",
			blocks:   0,
		},
		{
			name:     "atomic mode with a count above one",
			document: "mode: atomic\nexample.com/m/a.go:1.1,2.2 3 47\n",
			mode:     "atomic",
			blocks:   1,
		},
		{
			name:     "count mode",
			document: "mode: count\nexample.com/m/a.go:1.1,2.2 1 0\n",
			mode:     "count",
			blocks:   1,
		},
		{
			// A trailing newline produces one, and a document concatenated by
			// hand from two runs produces several. Neither is a reason to
			// refuse a document the toolchain will happily read.
			name:     "blank lines",
			document: "\nmode: set\n\nexample.com/m/a.go:1.1,2.2 1 1\n\n",
			mode:     "set",
			blocks:   1,
		},
		{
			// Windows line endings, which is what a profile copied through a
			// text-mode pipe on this platform looks like.
			name:     "carriage returns",
			document: "mode: set\r\nexample.com/m/a.go:1.1,2.2 1 1\r\n",
			mode:     "set",
			blocks:   1,
		},
		{
			// A zero-statement block is legal and appears for a function whose
			// body the compiler elided.
			name:     "zero statements",
			document: "mode: set\nexample.com/m/a.go:1.1,2.2 0 1\n",
			mode:     "set",
			blocks:   1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			profile, err := coverage.ParseTextfmt(strings.NewReader(test.document))
			if err != nil {
				t.Fatalf("ParseTextfmt(%q): %v", test.document, err)
			}
			if profile.Mode != test.mode {
				t.Errorf("mode = %q, want %q", profile.Mode, test.mode)
			}
			if len(profile.Blocks) != test.blocks {
				t.Errorf("parsed %d blocks, want %d: %+v", len(profile.Blocks), test.blocks, profile.Blocks)
			}
		})
	}
}

// TestParseTextfmtRefuses walks the ways a document can fail to be one.
//
// Every case is refused rather than skipped, and that is the decision worth
// testing: a parser that silently dropped an unreadable line would hand the
// mapping a profile missing exactly the blocks it could not read, and the
// mutants on those lines would be reported as uncovered survivors without
// anybody being told why.
func TestParseTextfmtRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		document string
		mentions string
	}{
		{name: "empty", document: "", mentions: "empty"},
		{name: "blank", document: "\n\n", mentions: "empty"},
		{name: "no mode line", document: "example.com/m/a.go:1.1,2.2 1 1\n", mentions: "mode"},
		{name: "empty mode", document: "mode: \n", mentions: "mode"},
		{name: "no file separator", document: "mode: set\n1.1,2.2 1 1\n", mentions: "line 2"},
		{name: "nothing after the colon", document: "mode: set\nexample.com/m/a.go:\n", mentions: "line 2"},
		{name: "missing the count", document: "mode: set\nexample.com/m/a.go:1.1,2.2 1\n", mentions: "line 2"},
		{name: "extra field", document: "mode: set\nexample.com/m/a.go:1.1,2.2 1 1 1\n", mentions: "line 2"},
		{name: "no comma", document: "mode: set\nexample.com/m/a.go:1.1 2.2 1 1\n", mentions: "line 2"},
		{name: "no column", document: "mode: set\nexample.com/m/a.go:1,2 1 1\n", mentions: "line 2"},
		{name: "line is not a number", document: "mode: set\nexample.com/m/a.go:x.1,2.2 1 1\n", mentions: "line 2"},
		{name: "column is not a number", document: "mode: set\nexample.com/m/a.go:1.x,2.2 1 1\n", mentions: "line 2"},
		{name: "zero line", document: "mode: set\nexample.com/m/a.go:0.1,2.2 1 1\n", mentions: "line 2"},
		{name: "zero column", document: "mode: set\nexample.com/m/a.go:1.0,2.2 1 1\n", mentions: "line 2"},
		{name: "negative count", document: "mode: set\nexample.com/m/a.go:1.1,2.2 1 -1\n", mentions: "line 2"},
		{name: "count is not a number", document: "mode: set\nexample.com/m/a.go:1.1,2.2 1 many\n", mentions: "line 2"},
		{name: "ends before it starts", document: "mode: set\nexample.com/m/a.go:9.1,2.2 1 1\n", mentions: "before it starts"},
		{
			name:     "an html page where a profile should be",
			document: "<!doctype html>\n<title>404</title>\n",
			mentions: "mode",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := coverage.ParseTextfmt(strings.NewReader(test.document))
			if err == nil {
				t.Fatalf("ParseTextfmt(%q) succeeded", test.document)
			}
			if code := coverage.CodeOf(err); code != coverage.CodeMalformedProfile {
				t.Errorf("code = %q, want %q (%v)", code, coverage.CodeMalformedProfile, err)
			}
			if !strings.Contains(err.Error(), test.mentions) {
				t.Errorf("error does not mention %q: %v", test.mentions, err)
			}
			// One line, because internal/engine folds this into a warning and
			// the report stores a warning as one string.
			if strings.ContainsAny(err.Error(), "\n\r") {
				t.Errorf("the error is not one line: %q", err.Error())
			}
		})
	}
}

// TestParseTextfmtKeepsTheLastColonAsTheSeparator is the one parsing rule that
// is a choice rather than a reading of the format.
//
// The Go toolchain's own parser uses a greedy match for the file name, so the
// coordinates begin after the *last* colon. Nothing in a Go import path can
// contain one today, which is exactly why a reader that disagreed with the
// writer would go unnoticed until the day something did.
func TestParseTextfmtKeepsTheLastColonAsTheSeparator(t *testing.T) {
	t.Parallel()

	profile, err := coverage.ParseTextfmt(strings.NewReader(
		"mode: set\nexample.com/m/odd:name/a.go:12.3,14.5 2 1\n"))
	if err != nil {
		t.Fatalf("ParseTextfmt: %v", err)
	}
	if len(profile.Blocks) != 1 {
		t.Fatalf("parsed %d blocks, want 1", len(profile.Blocks))
	}
	if got := profile.Blocks[0].File; got != "example.com/m/odd:name/a.go" {
		t.Errorf("file = %q, want the whole name up to the last colon", got)
	}
	if got := profile.Blocks[0].StartLine; got != 12 {
		t.Errorf("start line = %d, want 12", got)
	}
}
