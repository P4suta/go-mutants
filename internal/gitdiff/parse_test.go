// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitdiff

import (
	"math"
	"slices"
	"strings"
	"testing"
)

func TestParseDiffReadsTheDestinationSide(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		diff   string
		prefix string
		want   map[string][]Range
	}{
		{
			name: "a new file is every line",
			diff: strings.Join([]string{
				"diff --git a/new.go b/new.go",
				"new file mode 100644",
				"index 0000000..1111111",
				"--- /dev/null",
				"+++ b/new.go",
				"@@ -0,0 +1,3 @@",
				"+package main",
				"+",
				"+func main() {}",
			}, "\n"),
			want: map[string][]Range{"new.go": {{First: 1, Last: 3}}},
		},
		{
			name: "an omitted count is one line",
			diff: strings.Join([]string{
				"diff --git a/one.go b/one.go",
				"--- a/one.go",
				"+++ b/one.go",
				"@@ -7 +7 @@",
				"-\tx := 1",
				"+\tx := 2",
			}, "\n"),
			want: map[string][]Range{"one.go": {{First: 7, Last: 7}}},
		},
		{
			name: "a pure deletion touches nothing",
			diff: strings.Join([]string{
				"diff --git a/gone.go b/gone.go",
				"--- a/gone.go",
				"+++ b/gone.go",
				"@@ -4,3 +3,0 @@",
				"-\tone()",
				"-\ttwo()",
				"-\tthree()",
			}, "\n"),
			want: map[string][]Range{},
		},
		{
			name: "a deleted file is not a changed file",
			diff: strings.Join([]string{
				"diff --git a/dead.go b/dead.go",
				"deleted file mode 100644",
				"--- a/dead.go",
				"+++ /dev/null",
				"@@ -1,4 +0,0 @@",
				"-package dead",
			}, "\n"),
			want: map[string][]Range{},
		},
		{
			name: "adjacent hunks are merged and sorted",
			diff: strings.Join([]string{
				"diff --git a/many.go b/many.go",
				"--- a/many.go",
				"+++ b/many.go",
				"@@ -20,0 +21,2 @@",
				"+\tc()",
				"+\td()",
				"@@ -9,0 +10,1 @@",
				"+\ta()",
				"@@ -10,0 +11,1 @@",
				"+\tb()",
			}, "\n"),
			want: map[string][]Range{"many.go": {{First: 10, Last: 11}, {First: 21, Last: 22}}},
		},
		{
			name: "an added line that looks like a header is body",
			diff: strings.Join([]string{
				"diff --git a/tricky.go b/tricky.go",
				"--- a/tricky.go",
				"+++ b/tricky.go",
				"@@ -1,0 +2,2 @@",
				"+++ b/not-a-file.go",
				"+@@ -1 +1 @@",
			}, "\n"),
			want: map[string][]Range{"tricky.go": {{First: 2, Last: 3}}},
		},
		{
			name:   "a path outside the workspace is dropped",
			prefix: "module/",
			diff: strings.Join([]string{
				"diff --git a/module/in.go b/module/in.go",
				"--- a/module/in.go",
				"+++ b/module/in.go",
				"@@ -0,0 +1,1 @@",
				"+package in",
				"diff --git a/elsewhere/out.go b/elsewhere/out.go",
				"--- a/elsewhere/out.go",
				"+++ b/elsewhere/out.go",
				"@@ -0,0 +1,1 @@",
				"+package out",
			}, "\n"),
			want: map[string][]Range{"in.go": {{First: 1, Last: 1}}},
		},
		{
			name: "a binary file has no hunks",
			diff: strings.Join([]string{
				"diff --git a/logo.png b/logo.png",
				"index 1111111..2222222 100644",
				"Binary files a/logo.png and b/logo.png differ",
			}, "\n"),
			want: map[string][]Range{},
		},
		{
			name: "an empty diff changes nothing",
			diff: "",
			want: map[string][]Range{},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseDiff(c.diff, c.prefix)
			if err != nil {
				t.Fatalf("parseDiff: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("parseDiff produced %v, want %v", got, c.want)
			}
			for path, want := range c.want {
				if !slices.Equal(got[path], want) {
					t.Errorf("%s = %v, want %v", path, got[path], want)
				}
			}
		})
	}
}

func TestParseDiffRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		diff string
	}{
		{
			name: "a hunk header with no destination side",
			diff: "diff --git a/x.go b/x.go\n+++ b/x.go\n@@ -1,2 @@\n",
		},
		{
			name: "a hunk header that never closes",
			diff: "diff --git a/x.go b/x.go\n+++ b/x.go\n@@ -1,2 +3,4\n",
		},
		{
			name: "a destination line count that is not a number",
			diff: "diff --git a/x.go b/x.go\n+++ b/x.go\n@@ -1,2 +3,many @@\n",
		},
		{
			name: "a header with no b/ prefix",
			diff: "diff --git a/x.go b/x.go\n+++ x.go\n@@ -1 +1 @@\n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseDiff(c.diff, "")
			if err == nil {
				t.Fatal("the parser accepted output it cannot read")
			}
			if code := CodeOf(err); code != CodeMalformedDiff {
				t.Errorf("code = %q, want %q (%v)", code, CodeMalformedDiff, err)
			}
		})
	}
}

func TestUnquotePathDecodesGitsEscapes(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		{`plain/file.go`, `plain/file.go`},
		{`"with space.go"`, `with space.go`},
		{`"say \"hi\".go"`, `say "hi".go`},
		{`"back\\slash.go"`, `back\slash.go`},
		{`"tab\there.go"`, "tab\there.go"},
		{`"r\303\251sum\303\251.go"`, "résumé.go"},
		{`"trailing\"`, `trailing\`},
	}
	for _, c := range cases {
		if got := unquote(c.in); got != c.want {
			t.Errorf("unquote(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTouchesIsInclusiveAtBothEnds(t *testing.T) {
	t.Parallel()

	changed := Changed{Files: map[string][]Range{"a.go": {{First: 10, Last: 12}}}}
	cases := []struct {
		first, last int
		want        bool
	}{
		{8, 9, false},
		{9, 10, true},
		{10, 10, true},
		{12, 20, true},
		{13, 20, false},
		{1, 100, true},
		{12, 10, true},
	}
	for _, c := range cases {
		if got := changed.Touches("a.go", c.first, c.last); got != c.want {
			t.Errorf("Touches(a.go, %d, %d) = %v, want %v", c.first, c.last, got, c.want)
		}
	}
	if changed.Touches("other.go", 10, 12) {
		t.Error("a file with no changed lines was reported as touched")
	}
	if (Changed{}).Touches("a.go", 1, 1) {
		t.Error("the zero value touches something")
	}
}

func TestPathsAreSorted(t *testing.T) {
	t.Parallel()

	changed := Changed{Files: map[string][]Range{
		"z.go": {{First: 1, Last: 1}},
		"a.go": {{First: 1, Last: 1}},
		"m.go": {{First: 1, Last: 1}},
	}}
	if got := changed.Paths(); !slices.Equal(got, []string{"a.go", "m.go", "z.go"}) {
		t.Errorf("Paths() = %v", got)
	}
}

func TestMergeJoinsTouchingRangesWithoutOverflowing(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		in   []Range
		want []Range
	}{
		{name: "nothing", in: nil, want: []Range{}},
		{
			name: "disjoint ranges are sorted and left apart",
			in:   []Range{{First: 20, Last: 21}, {First: 1, Last: 2}},
			want: []Range{{First: 1, Last: 2}, {First: 20, Last: 21}},
		},
		{
			name: "overlapping ranges are joined",
			in:   []Range{{First: 1, Last: 10}, {First: 3, Last: 4}},
			want: []Range{{First: 1, Last: 10}},
		},
		{
			name: "adjacent ranges are joined",
			in:   []Range{{First: 5, Last: 7}, {First: 1, Last: 3}, {First: 4, Last: 4}},
			want: []Range{{First: 1, Last: 7}},
		},
		{
			name: "a range inside an unbounded one is swallowed rather than left beside it",
			in:   []Range{{First: 41, Last: math.MaxInt}, {First: 50, Last: 60}},
			want: []Range{{First: 41, Last: math.MaxInt}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := Merge(slices.Clone(test.in)); !slices.Equal(got, test.want) {
				t.Errorf("Merge(%v) = %v, want %v", test.in, got, test.want)
			}
		})
	}
}

func TestCodesAreUniqueAndInBlock(t *testing.T) {
	t.Parallel()

	codes := Codes()
	if len(codes) == 0 {
		t.Fatal("this package reports no codes at all")
	}
	seen := make(map[Code]bool, len(codes))
	for _, code := range codes {
		if seen[code] {
			t.Errorf("code %s is defined twice", code)
		}
		seen[code] = true
		if !strings.HasPrefix(string(code), "GOM771") || len(code) != len("GOM7710") {
			t.Errorf("code %s is outside the GOM771x range this package owns", code)
		}
	}
	if !slices.IsSortedFunc(codes, func(x, y Code) int { return strings.Compare(string(x), string(y)) }) {
		t.Errorf("Codes() is not in numeric order: %v", codes)
	}
}

func TestTheParserKnowsWhereItIsInTheDiff(t *testing.T) {
	t.Parallel()

	t.Run("a target header before any hunk is a header", func(t *testing.T) {
		t.Parallel()

		got, err := parseDiff("+++ b/x.go\n@@ -0,0 +1,2 @@\n+one\n+two\n", "")
		if err != nil {
			t.Fatalf("parseDiff: %v", err)
		}
		want := map[string][]Range{"x.go": {{First: 1, Last: 2}}}
		if !sameFiles(got, want) {
			t.Errorf("parseDiff = %v, want %v", got, want)
		}
	})

	t.Run("a target header inside the hunks is body text", func(t *testing.T) {
		t.Parallel()

		diff := "diff --git a/x.go b/x.go\n" +
			"+++ b/x.go\n" +
			"@@ -1,0 +1 @@\n" +
			"+++ b/evil.go\n" +
			"@@ -5,0 +5 @@\n" +
			"+ok\n"
		got, err := parseDiff(diff, "")
		if err != nil {
			t.Fatalf("parseDiff: %v", err)
		}
		want := map[string][]Range{"x.go": {{First: 1, Last: 1}, {First: 5, Last: 5}}}
		if !sameFiles(got, want) {
			t.Errorf("parseDiff = %v, want both hunks under x.go", got)
		}
	})

	t.Run("a new file puts the reader back before the hunks", func(t *testing.T) {
		t.Parallel()

		diff := "diff --git a/x.go b/x.go\n" +
			"+++ b/x.go\n" +
			"@@ -1,0 +1 @@\n" +
			"+one\n" +
			"diff --git a/y.go b/y.go\n" +
			"+++ b/y.go\n" +
			"@@ -1,0 +1,3 @@\n"
		got, err := parseDiff(diff, "")
		if err != nil {
			t.Fatalf("parseDiff: %v", err)
		}
		want := map[string][]Range{
			"x.go": {{First: 1, Last: 1}},
			"y.go": {{First: 1, Last: 3}},
		}
		if !sameFiles(got, want) {
			t.Errorf("parseDiff = %v, want %v", got, want)
		}
	})
}

func TestHunkHeadersThatAreNumbersButNotLineNumbers(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		diff string
	}{{
		name: "a start line past what an int can hold",
		diff: "diff --git a/x.go b/x.go\n+++ b/x.go\n@@ -1 +99999999999999999999 @@\n",
	}, {
		name: "a start line of zero under a count that adds lines",
		diff: "diff --git a/x.go b/x.go\n+++ b/x.go\n@@ -1 +0,2 @@\n",
	}, {
		name: "a negative start line",
		diff: "diff --git a/x.go b/x.go\n+++ b/x.go\n@@ -1 +-3,2 @@\n",
	}, {
		name: "a negative count",
		diff: "diff --git a/x.go b/x.go\n+++ b/x.go\n@@ -1 +3,-2 @@\n",
	}, {
		name: "a count past what an int can hold",
		diff: "diff --git a/x.go b/x.go\n+++ b/x.go\n@@ -1 +3,99999999999999999999 @@\n",
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseDiff(test.diff, "")
			if err == nil {
				t.Fatalf("parseDiff accepted a header no file could have produced: %v", got)
			}
			if code := CodeOf(err); code != CodeMalformedDiff {
				t.Errorf("code = %q, want %q (%v)", code, CodeMalformedDiff, err)
			}
		})
	}

	got, err := parseDiff("diff --git a/x.go b/x.go\n+++ b/x.go\n@@ -1,2 +0,0 @@\n", "")
	if err != nil {
		t.Fatalf("parseDiff of a pure deletion: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("parseDiff = %v, want no entry: a deleted line is not one to mutate", got)
	}
}

func TestRelativeDropsWhatIsOutsideTheWorkspace(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		path, prefix, want string
	}{
		{"cmd/main.go", "", "cmd/main.go"},
		{"", "", ""},
		{"", "sub/", ""},
		{"sub/a.go", "sub/", "a.go"},
		{"sub/deep/a.go", "sub/", "deep/a.go"},
		{"other/a.go", "sub/", ""},
		{"subsidiary/a.go", "sub/", ""},
		{"sub/", "sub/", ""},
	} {
		if got := relative(test.path, test.prefix); got != test.want {
			t.Errorf("relative(%q, %q) = %q, want %q", test.path, test.prefix, got, test.want)
		}
	}
}

func TestMergeJoinsRangesThatShareAStart(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		in   []Range
		want []Range
	}{{
		name: "the longer range first",
		in:   []Range{{First: 1, Last: 5}, {First: 1, Last: 3}},
		want: []Range{{First: 1, Last: 5}},
	}, {
		name: "the shorter range first",
		in:   []Range{{First: 1, Last: 3}, {First: 1, Last: 5}},
		want: []Range{{First: 1, Last: 5}},
	}, {
		name: "a third range that only the longer one reaches",
		in:   []Range{{First: 1, Last: 3}, {First: 1, Last: 5}, {First: 6, Last: 9}},
		want: []Range{{First: 1, Last: 9}},
	}, {
		name: "three that share a start",
		in:   []Range{{First: 4, Last: 4}, {First: 4, Last: 12}, {First: 4, Last: 7}},
		want: []Range{{First: 4, Last: 12}},
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := Merge(slices.Clone(test.in)); !slices.Equal(got, test.want) {
				t.Errorf("Merge(%v) = %v, want %v", test.in, got, test.want)
			}
		})
	}
}

func sameFiles(got, want map[string][]Range) bool {
	if len(got) != len(want) {
		return false
	}
	for path, ranges := range want {
		if !slices.Equal(got[path], ranges) {
			return false
		}
	}
	return true
}

func TestUnquoteReadsOnlyWhatIsReallyQuoted(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, in, want string
	}{{
		name: "an empty quoted path",
		in:   `""`,
		want: ``,
	}, {
		name: "a lone quotation mark",
		in:   `"`,
		want: `"`,
	}, {
		name: "a path that opens a quote and never closes it",
		in:   `"abc`,
		want: `"abc`,
	}, {
		name: "a path that ends with a quote it never opened",
		in:   `abc"`,
		want: `abc"`,
	}, {
		name: "a one-character path",
		in:   `x`,
		want: `x`,
	}, {
		name: "an empty path",
		in:   ``,
		want: ``,
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := unquote(test.in); got != test.want {
				t.Errorf("unquote(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestUnquoteReadsAnOctalEscapeOnlyWhenItIsAWholeOne(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, in, want string
	}{{
		name: "three digits at the top of the byte range",
		in:   `"\377"`,
		want: "\xff",
	}, {
		name: "three digits, the highest an ASCII byte",
		in:   `"\177"`,
		want: "\x7f",
	}, {
		name: "three digits that are zero",
		in:   `"\000"`,
		want: "\x00",
	}, {
		name: "three digits past what a byte holds",
		in:   `"\400"`,
		want: `400`,
	}, {
		name: "one digit at the end of the name",
		in:   `"\1"`,
		want: `1`,
	}, {
		name: "two digits at the end of the name",
		in:   `"\17"`,
		want: `17`,
	}, {
		name: "a digit followed by one that is not octal",
		in:   `"\18"`,
		want: `18`,
	}, {
		name: "two digits and a third that is not octal",
		in:   `"\178"`,
		want: `178`,
	}, {
		name: "a digit, a non-octal digit, and an octal one",
		in:   `"\1a7"`,
		want: `1a7`,
	}, {
		name: "an escape followed by three digits and a real name",
		in:   `"\303\251tude.go"`,
		want: "\xc3\xa9tude.go",
	}, {
		name: "two digits followed by a letter",
		in:   `"\17x"`,
		want: `17x`,
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := unquote(test.in); got != test.want {
				t.Errorf("unquote(%s) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}
