// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitdiff

import (
	"math"
	"slices"
	"strings"
	"testing"
)

// TestParseDiffReadsTheDestinationSide covers the hunk header shapes git
// actually writes, including the two that are easy to read wrongly: an omitted
// count, which means one line, and a zero count, which means none.
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

// TestParseDiffRefusesWhatItCannotRead proves that an unreadable header is a
// failure rather than a silently smaller selection.
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

// TestUnquotePathDecodesGitsEscapes covers the paths git still quotes with
// core.quotePath off.
func TestUnquotePathDecodesGitsEscapes(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		{`plain/file.go`, `plain/file.go`},
		{`"with space.go"`, `with space.go`},
		{`"say \"hi\".go"`, `say "hi".go`},
		{`"back\\slash.go"`, `back\slash.go`},
		{`"tab\there.go"`, "tab\there.go"},
		// A three-digit octal escape per byte is how git writes anything it
		// will not print; this is one accented letter in UTF-8.
		{`"r\303\251sum\303\251.go"`, "résumé.go"},
		{`"trailing\"`, `trailing\`},
	}
	for _, c := range cases {
		if got := unquote(c.in); got != c.want {
			t.Errorf("unquote(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestTouchesIsInclusiveAtBothEnds pins the overlap rule the selection stage
// asks its question with.
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
		// A caller that hands the ends over the wrong way round is asking about
		// the same span, and is answered rather than quietly told no.
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

// TestPathsAreSorted proves the accessor imposes an order rather than handing
// out a map's.
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

// TestMergeJoinsTouchingRangesWithoutOverflowing is [Merge]'s own table, and
// the last row is why it exists as a test rather than as a line inside the
// parser's.
//
// The join is "r starts at or before one past the end of the last range", and
// the obvious way to write it — `r.First <= out[n-1].Last+1` — wraps to a
// negative number when the last range ends at math.MaxInt, at which point every
// following range compares as disjoint and the result stops being canonical.
// `r.First-1 <= out[n-1].Last` says the same thing and cannot wrap, because
// [checkLineRange] and the hunk parser both refuse a First below 1.
//
// A range that ends at math.MaxInt is not hypothetical from this side: the
// public Selection takes ranges from a caller, and "everything from line 41 on"
// is the natural way to spell a range whose end nobody knows.
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

// TestCodesAreUniqueAndInBlock holds this package inside the range it owns.
//
// GOM7701 belongs to internal/tui, which shares the GOM77xx block; this package
// starts at GOM7710 so that the two allocations can never meet.
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

// TestTheParserKnowsWhereItIsInTheDiff is the state machine stated as the two
// facts that make it one.
//
// Under `-U0` every body line begins with `+` or `-`, so an added line whose
// text starts with `++ ` arrives as `+++ ` and is spelled exactly like a file
// header. Nothing in the line tells the two apart; only the position does. A
// `+++` is a header before the first hunk of a file and body text after it, and
// a `diff --git` line is what puts the reader back before.
func TestTheParserKnowsWhereItIsInTheDiff(t *testing.T) {
	t.Parallel()

	t.Run("a target header before any hunk is a header", func(t *testing.T) {
		t.Parallel()

		// The reader starts outside every file's hunks, which is what makes
		// the first `+++` it sees a header. git always writes `diff --git`
		// ahead of one, so this states the machine's initial state rather than
		// a shape git produces -- and the machine is what the case below
		// depends on being right.
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

		// One file, two hunks, and the first hunk adds the line `++ b/evil.go`
		// -- which git writes as `+++ b/evil.go`. Read as a header it would
		// send the second hunk's lines to a file that is not in this diff.
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

// TestHunkHeadersThatAreNumbersButNotLineNumbers covers the three refusals a
// header can earn after its `+` field has been found.
//
// They are separated from the shapes in [TestParseDiffRefusesWhatItCannotRead]
// because each one is a number strconv will happily read and no file could
// have: a start too large for an int, a start of zero under a non-zero count,
// and a negative count. The first is the one worth being exact about --
// strconv.Atoi answers a value *and* an error for an overflow, and the value it
// answers is math.MaxInt, so a reader that looked only at the number would
// select a range no file has instead of refusing the header.
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

	// And the boundary on the other side: line 1 with a zero count is a pure
	// deletion, which is a header this parser reads and stores nothing for.
	got, err := parseDiff("diff --git a/x.go b/x.go\n+++ b/x.go\n@@ -1,2 +0,0 @@\n", "")
	if err != nil {
		t.Fatalf("parseDiff of a pure deletion: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("parseDiff = %v, want no entry: a deleted line is not one to mutate", got)
	}
}

// TestRelativeDropsWhatIsOutsideTheWorkspace pins the one mapping between git's
// coordinates and this run's.
//
// git is asked about the repository and answers in repository-relative paths; a
// run mutates a module that may be a subtree of it. Everything outside that
// subtree is dropped rather than carried with a `../` in front of it, because a
// path this run cannot mutate is not a path to select by. The prefix git
// reports always ends in a slash, which is what makes a sibling directory whose
// name starts with the same letters -- `internal/` against `internalise/` --
// the case a plain string prefix would get wrong.
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
		// The prefix itself, with nothing after it. There is no file at a
		// directory, so there is nothing to mutate and nothing to name.
		{"sub/", "sub/", ""},
	} {
		if got := relative(test.path, test.prefix); got != test.want {
			t.Errorf("relative(%q, %q) = %q, want %q", test.path, test.prefix, got, test.want)
		}
	}
}

// TestMergeJoinsRangesThatShareAStart is the tie [TestMergeJoinsTouchingRanges…]
// does not reach: two hunks git emitted for the same first line.
//
// It is the one ordering the sort has to decide without help from the starts,
// and the answer has to be the same set whichever way it decides it, because a
// canonical range list is what lets a selection and a diff describing one file
// compare equal.
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

// sameFiles compares two changed-line maps.
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

// TestUnquoteReadsOnlyWhatIsReallyQuoted covers the edges of the quoting rule,
// which are all about telling a quoted path from a path that merely contains a
// quotation mark.
//
// git quotes a path by wrapping it in `"` and escaping what it will not print,
// so a path is quoted only when it starts *and* ends with one and has room for
// both. Everything else is a literal path and is returned untouched: a name
// beginning with a quotation mark is a legal file name, and decoding it would
// rename a file this run is about to mutate.
func TestUnquoteReadsOnlyWhatIsReallyQuoted(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, in, want string
	}{{
		name: "an empty quoted path",
		in:   `""`,
		want: ``,
	}, {
		// One byte cannot be both the opening and the closing quote.
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

// TestUnquoteReadsAnOctalEscapeOnlyWhenItIsAWholeOne is the other half, and it
// is the half with the arithmetic in it.
//
// git writes a byte it will not print as exactly three octal digits. Two digits
// at the end of a name is not a short escape — it is a backslash followed by
// two characters — and reading it as one would consume bytes that are part of
// the path. The rule is therefore "three digits, and a value a byte can hold",
// and both halves of that are what strconv.ParseUint answers over exactly three
// bytes: a digit that is not octal and a value past 255 are the same refusal,
// and writing the first digit back is the same answer to both.
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
		// 0o400 is 256, which no byte holds. The digits are written back as
		// they were found rather than truncated to something else.
		name: "three digits past what a byte holds",
		in:   `"\400"`,
		want: `400`,
	}, {
		// One digit, and the name ends. There is no fourth byte to read and
		// nothing to guess at.
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
		// The escape is not the last thing in the name, so there is a fourth
		// byte -- and it is still not part of the escape.
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
