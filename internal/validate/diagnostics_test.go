// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

const (
	windowsRoot = `C:\Users\dev\AppData\Local\Temp\go-mutants-snap-1234`
	posixRoot   = "/tmp/go-mutants-snap-1234"
)

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

func TestNormalizePathPutsEverySpellingIntoOneCoordinateSystem(t *testing.T) {
	t.Parallel()

	const root = "/snap/tree"
	for _, test := range []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "a relative path", raw: "pkg/file.go", want: "pkg/file.go", ok: true},
		{name: "a dot-slash path", raw: "./pkg/file.go", want: "pkg/file.go", ok: true},
		{name: "several dot-slashes", raw: "././pkg/file.go", want: "pkg/file.go", ok: true},
		{name: "backslashes", raw: `.\pkg\file.go`, want: "pkg/file.go", ok: true},
		{name: "surrounding space", raw: "  pkg/file.go  ", want: "pkg/file.go", ok: true},
		{name: "an absolute path inside the root", raw: "/snap/tree/pkg/file.go", want: "pkg/file.go", ok: true},
		{name: "a root with a trailing slash", raw: "/snap/tree/pkg/file.go", want: "pkg/file.go", ok: true},
		{name: "an uncleaned path", raw: "pkg/./sub/../file.go", want: "pkg/file.go", ok: true},

		{name: "nothing at all", raw: "", ok: false},
		{name: "only space", raw: "   ", ok: false},
		{name: "only dot-slashes", raw: "./././", ok: false},
		{name: "the root itself", raw: "/snap/tree", ok: false},
		{name: "an absolute path outside the root", raw: "/elsewhere/file.go", ok: false},
		{name: "a sibling whose name starts the same", raw: "/snap/treeish/file.go", ok: false},
		{name: "a path that climbs out", raw: "../file.go", ok: false},
		{name: "a path that cleans to a climb", raw: "pkg/../../file.go", ok: false},
		{name: "a path that cleans to the directory", raw: "pkg/..", ok: false},
		{name: "the standard library", raw: "/usr/local/go/src/fmt/print.go", ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, ok := normalizePath(test.raw, root)
			if ok != test.ok {
				t.Fatalf("normalizePath(%q) = %q, %v, want ok=%v", test.raw, got, ok, test.ok)
			}
			if got != test.want {
				t.Errorf("normalizePath(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}

	if got, ok := normalizePath("/snap/tree/pkg/file.go", "/snap/tree/"); !ok || got != "pkg/file.go" {
		t.Errorf("normalizePath under a root with a trailing slash = %q, %v", got, ok)
	}
	if got, ok := normalizePath("/anything/file.go", ""); ok {
		t.Errorf("normalizePath with no root = %q, want nothing placed", got)
	}
}

func TestUnderRootIsAWholeElementPrefixAndNotAStringOne(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		p, root, want string
		ok            bool
	}{
		{p: "/a/b/c.go", root: "/a/b", want: "c.go", ok: true},
		{p: "/a/b/deep/c.go", root: "/a/b", want: "deep/c.go", ok: true},
		{p: "/a/b", root: "/a/b", ok: false},
		{p: "/a/bc", root: "/a/b", ok: false},
		{p: "/a/bc/d.go", root: "/a/b", ok: false},
		{p: "/x/y.go", root: "/a/b", ok: false},
		{p: "/a/b/c.go", root: "", ok: false},
		{p: "", root: "/a/b", ok: false},
	} {
		got, ok := underRoot(test.p, test.root)
		if ok != test.ok || got != test.want {
			t.Errorf("underRoot(%q, %q) = %q, %v, want %q, %v", test.p, test.root, got, ok, test.want, test.ok)
		}
	}
}

func TestEqualPathOnIsCaseInsensitiveWhereThePlatformIs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		goos, a, b string
		want       bool
	}{
		{goos: "linux", a: "/snap/tree", b: "/snap/tree", want: true},
		{goos: "linux", a: "/snap/Tree", b: "/snap/tree", want: false},
		{goos: "darwin", a: "/snap/Tree", b: "/snap/tree", want: false},
		{goos: "windows", a: "/snap/Tree", b: "/snap/tree", want: true},
		{goos: "windows", a: "/snap/one", b: "/snap/two", want: false},
		{goos: "linux", a: "C:/Users/x", b: "c:/users/x", want: true},
		{goos: "linux", a: "c:/users/x", b: "C:/Users/x", want: true},
		{goos: "linux", a: "C:/Users/x", b: "C:/Users/y", want: false},
	} {
		if got := equalPathOn(test.goos, test.a, test.b); got != test.want {
			t.Errorf("equalPathOn(%q, %q, %q) = %v, want %v", test.goos, test.a, test.b, got, test.want)
		}
	}

	if equalPath("/a", "/a") != equalPathOn(runtime.GOOS, "/a", "/a") {
		t.Error("equalPath and equalPathOn disagree about this platform")
	}
}

func TestHasVolumeIsADriveLetterAndAColon(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		p    string
		want bool
	}{
		{p: "C:", want: true},
		{p: "C:/Users", want: true},
		{p: "c:/users", want: true},
		{p: "a:", want: true},
		{p: "z:", want: true},
		{p: "A:", want: true},
		{p: "Z:", want: true},
		{p: "`:", want: false},
		{p: "{:", want: false},
		{p: "@:", want: false},
		{p: "[:", want: false},
		{p: "0:", want: false},
		{p: "CC", want: false},
		{p: "C", want: false},
		{p: "", want: false},
		{p: "/C:", want: false},
	} {
		if got := hasVolume(test.p); got != test.want {
			t.Errorf("hasVolume(%q) = %v, want %v", test.p, got, test.want)
		}
	}
}

func TestIsAbsolutePathAnswersForBothPlatformsSpellings(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		p    string
		want bool
	}{
		{p: "/a/b", want: true},
		{p: "/", want: true},
		{p: "C:", want: true},
		{p: "C:/Users/x", want: true},
		{p: "C:x", want: false},
		{p: "pkg/file.go", want: false},
		{p: "./pkg/file.go", want: false},
		{p: "", want: false},
	} {
		if got := isAbsolutePath(test.p); got != test.want {
			t.Errorf("isAbsolutePath(%q) = %v, want %v", test.p, got, test.want)
		}
	}
}

func TestAbsIsTheDistanceAndNeverTheSign(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ in, want int }{
		{in: 0, want: 0},
		{in: 1, want: 1},
		{in: -1, want: 1},
		{in: 42, want: 42},
		{in: -42, want: 42},
	} {
		if got := abs(test.in); got != test.want {
			t.Errorf("abs(%d) = %d, want %d", test.in, got, test.want)
		}
	}
}

func TestChooseDiagnosticPrefersTheNearestWhenNoneIsInside(t *testing.T) {
	t.Parallel()

	inside := func(line int, text string) diagnostic {
		return diagnostic{Path: "pkg/a.go", Line: line, Text: text, Inside: true}
	}

	t.Run("the nearest above and the nearest below at equal distance", func(t *testing.T) {
		t.Parallel()

		got := chooseDiagnostic([]diagnostic{
			inside(8, "above"),
			inside(12, "below"),
		}, "pkg/a.go", 10, 10)
		if got != "above" {
			t.Errorf("chooseDiagnostic = %q, want the first of two equally near", got)
		}
	})

	t.Run("a strictly nearer line later in the list", func(t *testing.T) {
		t.Parallel()

		got := chooseDiagnostic([]diagnostic{
			inside(1, "far"),
			inside(9, "near"),
		}, "pkg/a.go", 10, 10)
		if got != "near" {
			t.Errorf("chooseDiagnostic = %q, want the nearer line", got)
		}
	})

	t.Run("the boundaries of the range are inside it", func(t *testing.T) {
		t.Parallel()

		for _, line := range []int{10, 12} {
			got := chooseDiagnostic([]diagnostic{inside(line, "in")}, "pkg/a.go", 10, 12)
			if got != "in" {
				t.Errorf("a diagnostic on line %d of [10,12] = %q, want it counted as inside", line, got)
			}
		}
		for _, line := range []int{9, 13} {
			got := chooseDiagnostic([]diagnostic{inside(line, "out"), inside(11, "in")}, "pkg/a.go", 10, 12)
			if got != "in" {
				t.Errorf("a diagnostic on line %d of [10,12] = %q, want only the inside one", line, got)
			}
		}
	})

	t.Run("a diagnostic outside the snapshot is not a candidate at all", func(t *testing.T) {
		t.Parallel()

		outside := diagnostic{Path: "pkg/a.go", Line: 10, Text: "stdlib", Inside: false}
		got := chooseDiagnostic([]diagnostic{outside}, "pkg/a.go", 10, 10)
		if got != "stdlib" {
			t.Errorf("chooseDiagnostic = %q, want the last tier: the first line of the build", got)
		}
	})

	t.Run("another file's diagnostics are not this mutant's", func(t *testing.T) {
		t.Parallel()

		got := chooseDiagnostic([]diagnostic{
			{Path: "pkg/b.go", Line: 10, Text: "elsewhere", Inside: true},
			inside(99, "here"),
		}, "pkg/a.go", 10, 10)
		if got != "here" {
			t.Errorf("chooseDiagnostic = %q, want this file's own", got)
		}
	})

	t.Run("nothing at all", func(t *testing.T) {
		t.Parallel()

		if got := chooseDiagnostic(nil, "pkg/a.go", 1, 1); got != "" {
			t.Errorf("chooseDiagnostic of nothing = %q, want nothing", got)
		}
	})
}
