// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"slices"
	"strings"
	"testing"
)

// windowsRoot and posixRoot are the two spellings a snapshot root comes in.
//
// Both are exercised on every platform on purpose. The parser reads whatever
// the host's toolchain prints, but the shapes it has to survive — a drive
// letter, backslash separators, a `.\` prefix — are properties of the output
// rather than of the machine reading it, and a Windows-only test of them is a
// test that only runs where the bug was going to be found anyway.
const (
	windowsRoot = `C:\Users\dev\AppData\Local\Temp\go-mutants-snap-1234`
	posixRoot   = "/tmp/go-mutants-snap-1234"
)

// TestParseDiagnostics reads real compiler output shapes into located
// messages.
//
// Several rows below are transcripts from the era when a guard around a named
// boolean type was itself the compile error, and they name a `flag.go` the
// fixture no longer has. They are kept deliberately: what this parser has to
// survive is the *shape* of a message — a `.\` prefix, a drive letter, a
// subdirectory printed without one — and the body is incidental to that, so a
// corpus of shapes the compiler has really printed is worth more than one
// narrowed to the failures today's fixture happens to produce. Nothing here is
// a claim about what fixtures/rejectable now contains; see its README for why
// the named boolean stopped being a trap.
func TestParseDiagnostics(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		root   string
		output string
		want   []diagnostic
	}{
		{
			name: "the shape a build in the snapshot prints on Windows",
			root: windowsRoot,
			output: "# fixture.example/rejectable\r\n" +
				".\\compare.go:16:9: cannot use (__gm.M[5] && (a != b) || !(__gm.M[5]) && (a == b)) " +
				"(value of type bool) as Flag value in return statement\r\n" +
				".\\flag.go:8:9: cannot use guard (value of type bool) as Flag value in return statement\r\n",
			want: []diagnostic{
				{Path: "compare.go", Inside: true, Line: 16, Column: 9},
				{Path: "flag.go", Inside: true, Line: 8, Column: 9},
			},
		},
		{
			name:   "the same build on a POSIX host",
			root:   posixRoot,
			output: "# fixture.example/rejectable\n./pkg/flag.go:8:9: cannot use guard as Flag value\n",
			want:   []diagnostic{{Path: "pkg/flag.go", Inside: true, Line: 8, Column: 9}},
		},
		{
			// A package in a subdirectory, which the go tool prints *without*
			// the leading "./" it gives a file in the working directory. This
			// is a transcript of real output rather than a guess: getting it
			// wrong would leave the file unattributed, and the search would
			// fall back to bisecting every undecided file in the module — the
			// right answer at many times the cost.
			name:   "a package in a subdirectory",
			root:   windowsRoot,
			output: "# fixture.example/rejectable/deep\ndeep\\deep.go:8:9: cannot use guard as Flag value\n",
			want:   []diagnostic{{Path: "deep/deep.go", Inside: true, Line: 8, Column: 9}},
		},
		{
			name:   "an absolute Windows path under the root",
			root:   windowsRoot,
			output: windowsRoot + "\\pkg\\deep\\file.go:12:5: undefined: x\n",
			want:   []diagnostic{{Path: "pkg/deep/file.go", Inside: true, Line: 12, Column: 5}},
		},
		{
			// A case-insensitive filesystem prints a path in whatever case the
			// caller handed it, and a temporary directory reaches the compiler
			// through several of them. A miss here would look like a file
			// outside the snapshot and would never be blamed on the mutant that
			// broke it.
			name:   "an absolute Windows path in another case",
			root:   windowsRoot,
			output: strings.ToLower(windowsRoot) + "\\pkg\\file.go:1:1: undefined: x\n",
			want:   []diagnostic{{Path: "pkg/file.go", Inside: true, Line: 1, Column: 1}},
		},
		{
			name:   "an absolute POSIX path under the root",
			root:   posixRoot,
			output: posixRoot + "/pkg/file.go:3:4: undefined: x\n",
			want:   []diagnostic{{Path: "pkg/file.go", Inside: true, Line: 3, Column: 4}},
		},
		{
			// The root is a prefix of this path as a *string* and not as a
			// path. A comparison that forgot the separator would pull a file
			// from a neighbouring directory into the snapshot's coordinates and
			// bisect a file that is not there.
			name:   "a sibling directory whose name starts with the root",
			root:   posixRoot,
			output: posixRoot + "-other/pkg/file.go:3:4: undefined: x\n",
			want:   []diagnostic{{Path: posixRoot + "-other/pkg/file.go", Line: 3, Column: 4}},
		},
		{
			name:   "a file outside the snapshot",
			root:   windowsRoot,
			output: `C:\Go\src\fmt\print.go:88:2: undefined: x` + "\n",
			want:   []diagnostic{{Path: `C:\Go\src\fmt\print.go`, Line: 88, Column: 2}},
		},
		{
			name:   "a relative path that climbs out of the snapshot",
			root:   posixRoot,
			output: "../elsewhere/file.go:1:1: undefined: x\n",
			want:   []diagnostic{{Path: "../elsewhere/file.go", Line: 1, Column: 1}},
		},
		{
			name:   "a diagnostic with no column",
			root:   posixRoot,
			output: "./go.mod:5: unknown directive: nonsense\n",
			want:   []diagnostic{{Path: "go.mod", Inside: true, Line: 5}},
		},
		{
			// The message carries colons and digits of its own, which is the
			// case a left-to-right split on ":" gets wrong.
			name:   "a message full of colons",
			root:   posixRoot,
			output: "./a.go:7:2: cannot use m (map[string]int) as map[string]string: 1:2 is not 3:4\n",
			want:   []diagnostic{{Path: "a.go", Inside: true, Line: 7, Column: 2}},
		},
		{
			name: "a multi-line error",
			root: posixRoot,
			output: "./a.go:9:12: cannot use s (variable of type S) as I value: missing method M\n" +
				"\t\thave M(int)\n" +
				"\t\twant M(string)\n" +
				"./b.go:3:1: undefined: y\n",
			want: []diagnostic{
				{Path: "a.go", Inside: true, Line: 9, Column: 12},
				{Path: "b.go", Inside: true, Line: 3, Column: 1},
			},
		},
		{
			// Both spellings of the compiler giving up have been printed by
			// released toolchains. The located one is a diagnostic like any
			// other; the bare one is not, and must not be folded into the
			// message above it.
			name: "too many errors, located and bare",
			root: posixRoot,
			output: "./a.go:1:1: undefined: a\n" +
				"./a.go:2:1: too many errors\n" +
				"too many errors\n",
			want: []diagnostic{
				{Path: "a.go", Inside: true, Line: 1, Column: 1},
				{Path: "a.go", Inside: true, Line: 2, Column: 1},
			},
		},
		{
			name:   "output with nothing located in it",
			root:   posixRoot,
			output: "go: downloading example.com/x v1.2.3\ngo: module lookup disabled by GOPROXY=off\n",
			want:   nil,
		},
		{
			name:   "no output at all",
			root:   posixRoot,
			output: "",
			want:   nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got := parseDiagnostics(c.output, c.root)
			if len(got) != len(c.want) {
				t.Fatalf("parsed %d diagnostics, want %d:\n%+v", len(got), len(c.want), got)
			}
			for i, want := range c.want {
				if got[i].Path != want.Path || got[i].Inside != want.Inside ||
					got[i].Line != want.Line || got[i].Column != want.Column {
					t.Errorf("diagnostic %d = {%q inside=%v %d:%d}, want {%q inside=%v %d:%d}",
						i, got[i].Path, got[i].Inside, got[i].Line, got[i].Column,
						want.Path, want.Inside, want.Line, want.Column)
				}
			}
		})
	}
}

// TestParseDiagnosticsKeepsContinuationLines proves a multi-line error reaches
// a rejection whole.
//
// The "have"/"want" lines under a type error are usually the only part that
// says what the compiler actually wanted, so a parser that kept the first line
// and dropped the rest would produce rejections that name a problem without
// describing it.
func TestParseDiagnosticsKeepsContinuationLines(t *testing.T) {
	t.Parallel()

	output := "./a.go:9:12: cannot use s (variable of type S) as I value: missing method M\n" +
		"\t\thave M(int)\n" +
		"\t\twant M(string)\n"
	got := parseDiagnostics(output, posixRoot)
	if len(got) != 1 {
		t.Fatalf("parsed %d diagnostics, want 1: %+v", len(got), got)
	}
	for _, needle := range []string{"missing method M", "have M(int)", "want M(string)"} {
		if !strings.Contains(got[0].Text, needle) {
			t.Errorf("the diagnostic's text does not carry %q:\n%s", needle, got[0].Text)
		}
	}
}

// TestBlamedPaths pins the order and the deduplication of the file list a
// failing build produces, and that a file outside the snapshot never reaches
// it.
func TestBlamedPaths(t *testing.T) {
	t.Parallel()

	output := "# fixture.example/x\n" +
		"./b.go:1:1: undefined: x\n" +
		"./a.go:2:1: undefined: y\n" +
		"./b.go:3:1: undefined: z\n" +
		`C:\Go\src\fmt\print.go:1:1: undefined: w` + "\n"
	got := blamedPaths(parseDiagnostics(output, posixRoot))
	if want := []string{"b.go", "a.go"}; !slices.Equal(got, want) {
		t.Errorf("blamedPaths = %v, want %v", got, want)
	}
}

// TestChooseDiagnostic pins which lines a rejection carries.
//
// The tiers matter in the order they are written. Line preservation means the
// guard that failed sits on the line the candidate sat on, so the exact match
// is the common case and the right answer; the fallbacks exist because a type
// checker sometimes reports at the enclosing statement, and a rejection with no
// explanation at all is the one outcome that is never acceptable.
func TestChooseDiagnostic(t *testing.T) {
	t.Parallel()

	output := "./a.go:5:9: cannot use guard as Flag value\n" +
		"./a.go:5:20: and another thing about line five\n" +
		"./a.go:40:1: something far away\n" +
		"./b.go:1:1: about another file entirely\n"
	diags := parseDiagnostics(output, posixRoot)

	cases := []struct {
		name            string
		file            string
		start, end      int
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:            "every line inside the candidate's own span",
			file:            "a.go",
			start:           5,
			end:             5,
			wantContains:    []string{"cannot use guard", "and another thing"},
			wantNotContains: []string{"far away", "another file"},
		},
		{
			name:            "the nearest line about the same file",
			file:            "a.go",
			start:           38,
			end:             38,
			wantContains:    []string{"far away"},
			wantNotContains: []string{"another file"},
		},
		{
			name:         "the first line of all, for a file nothing named",
			file:         "c.go",
			start:        1,
			end:          1,
			wantContains: []string{"cannot use guard"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := chooseDiagnostic(diags, c.file, c.start, c.end)
			for _, needle := range c.wantContains {
				if !strings.Contains(got, needle) {
					t.Errorf("chooseDiagnostic = %q, which does not carry %q", got, needle)
				}
			}
			for _, needle := range c.wantNotContains {
				if strings.Contains(got, needle) {
					t.Errorf("chooseDiagnostic = %q, which should not carry %q", got, needle)
				}
			}
		})
	}

	if got := chooseDiagnostic(nil, "a.go", 1, 1); got != "" {
		t.Errorf("chooseDiagnostic over no diagnostics = %q, want the empty string", got)
	}
}
