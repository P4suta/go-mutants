// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
)

var samplePath = filepath.Join("testdata", "textfmt.sample.txt")

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
			name:     "blank lines",
			document: "\nmode: set\n\nexample.com/m/a.go:1.1,2.2 1 1\n\n",
			mode:     "set",
			blocks:   1,
		},
		{
			name:     "carriage returns",
			document: "mode: set\r\nexample.com/m/a.go:1.1,2.2 1 1\r\n",
			mode:     "set",
			blocks:   1,
		},
		{
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
			name:     "three fields but no comma between the positions",
			document: "mode: set\nexample.com/m/a.go:1.1 2 1\n",
			mentions: "no comma between the positions",
		},
		{
			name:     "the closing line is not a number",
			document: "mode: set\nexample.com/m/a.go:1.1,y.2 1 1\n",
			mentions: "is not a line number",
		},
		{
			name:     "the closing column is not a number",
			document: "mode: set\nexample.com/m/a.go:1.1,2.y 1 1\n",
			mentions: "is not a column number",
		},
		{
			name:     "the statement count is not a number",
			document: "mode: set\nexample.com/m/a.go:1.1,2.2 x 1\n",
			mentions: "is not a statement count",
		},
		{
			name:     "the closing line does not fit in an int",
			document: "mode: set\nexample.com/m/a.go:1.1,99999999999999999999.2 1 1\n",
			mentions: "is not a line number",
		},
		{
			name:     "the closing column does not fit in an int",
			document: "mode: set\nexample.com/m/a.go:1.1,2.99999999999999999999 1 1\n",
			mentions: "is not a column number",
		},
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
			if strings.ContainsAny(err.Error(), "\n\r") {
				t.Errorf("the error is not one line: %q", err.Error())
			}
		})
	}
}

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

func TestParseTextfmtNamesTheSeparatorItCouldNotFind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		document string
	}{
		{name: "no colon at all", document: "mode: set\n1.1,2.2 1 1\n"},
		{name: "a colon with nothing after it", document: "mode: set\nexample.com/m/a.go:\n"},
		{name: "a colon with nothing before it", document: "mode: set\n:1.1,2.2 1 1\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := coverage.ParseTextfmt(strings.NewReader(test.document))
			if err == nil {
				t.Fatalf("ParseTextfmt(%q) succeeded", test.document)
			}
			if !strings.Contains(err.Error(), "no file separator") {
				t.Errorf("error does not name the missing separator: %v", err)
			}
		})
	}
}

func TestParseTextfmtBlamesTheDocumentRatherThanALineForAnEmptyProfile(t *testing.T) {
	t.Parallel()

	_, err := coverage.ParseTextfmt(strings.NewReader(""))
	if err == nil {
		t.Fatalf("ParseTextfmt(\"\") succeeded")
	}
	want := string(coverage.CodeMalformedProfile) +
		`: the coverage profile is empty: not even a "mode: <name>" header`
	if got := err.Error(); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestParseTextfmtReportsAReaderThatFailed(t *testing.T) {
	t.Parallel()

	_, err := coverage.ParseTextfmt(&failingReader{prefix: "mode: set\nexample.com/m/a.go:1.1,2.2 1 1\n"})
	if err == nil {
		t.Fatalf("a profile that could not be read was accepted")
	}
	if code := coverage.CodeOf(err); code != coverage.CodeMalformedProfile {
		t.Errorf("code = %q, want %q (%v)", code, coverage.CodeMalformedProfile, err)
	}
	if !errors.Is(err, errUnrelated) {
		t.Errorf("the reader's own failure is not reachable through the error: %v", err)
	}
	if !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("error does not say the profile could not be read: %v", err)
	}
}

type failingReader struct {
	prefix string
	read   int
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.read < len(r.prefix) {
		n := copy(p, r.prefix[r.read:])
		r.read += n
		return n, nil
	}
	return 0, errUnrelated
}
