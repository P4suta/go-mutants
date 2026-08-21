// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gitdiff

import (
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
