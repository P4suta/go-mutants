// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const (
	untrackedFixtureLineCount = 2
	overflowingDecimalDigits  = 100
)

func scriptedDiff(t *testing.T, output string, err error, tracked ...string) {
	t.Helper()
	previous := gitNamesOutput
	t.Cleanup(func() { gitNamesOutput = previous })
	gitNamesOutput = func(_ context.Context, _ string, arguments []string) ([]byte, error) {
		if len(arguments) != 0 && arguments[0] == "ls-files" {
			return []byte(strings.Join(tracked, "\x00")), nil
		}
		return []byte(output), err
	}
}

func TestChangedLineRangesReadsTheNewSideOfEveryHunk(t *testing.T) {
	root := t.TempDir()
	writeTrackedFixture(t, root, "value.go")
	scriptedDiff(t, `diff --git a/value.go b/value.go
--- a/value.go
+++ b/value.go
@@ -12,0 +13,4 @@ func Value() int {
+	one
+	two
+	three
+	four
@@ -40 +44 @@ func Other() int {
-	old
+	new
`, nil, "value.go")
	ranges, ok := changedLineRanges(t.Context(), root, "origin/main", []string{"value.go"})
	if !ok {
		t.Fatal("the diff was refused")
	}
	want := map[string][]gomutants.LineRange{"value.go": {{First: 13, Last: 16}, {First: 44, Last: 44}}}
	if !reflect.DeepEqual(ranges, want) {
		t.Fatalf("ranges = %+v, want %+v", ranges, want)
	}
}

func TestChangedLineRangesIgnoresAHunkThatOnlyDeletes(t *testing.T) {
	root := t.TempDir()
	writeTrackedFixture(t, root, "value.go")
	scriptedDiff(t, "+++ b/value.go\n@@ -12,3 +13,0 @@\n", nil, "value.go")
	ranges, ok := changedLineRanges(t.Context(), root, "", []string{"value.go"})
	if !ok {
		t.Fatal("the diff was refused")
	}
	if len(ranges) != 0 {
		t.Fatalf("ranges = %+v, want none", ranges)
	}
}

func TestChangedLineRangesFailsClosed(t *testing.T) {
	root := t.TempDir()
	writeTrackedFixture(t, root, "value.go")
	for _, test := range []struct {
		name   string
		output string
		err    error
	}{
		{name: "git failed", err: errors.New("git exploded")},
		{name: "a hunk before any file", output: "@@ -1 +1 @@\n"},
		{name: "a file the caller did not name", output: "+++ b/other.go\n@@ -1 +1 @@\n"},
		{name: "an unreadable hunk header", output: "+++ b/value.go\n@@ -1 +0 @@\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			scriptedDiff(t, test.output, test.err, "value.go")
			if ranges, ok := changedLineRanges(t.Context(), root, "", []string{"value.go"}); ok || ranges != nil {
				t.Fatalf("a diff that could not be trusted was accepted: (%+v, %t)", ranges, ok)
			}
		})
	}
}

func TestChangedLineRangesFailsClosedWhenAnUntrackedFileCannotBeRead(t *testing.T) {
	root := t.TempDir()
	writeTrackedFixture(t, root, "untracked.go")
	scriptedDiff(t, "", nil)
	ranges, ok := changedLineRangesWithLineCount(t.Context(), root, "", []string{"untracked.go"}, func(string) (int, bool) {
		return 1, false
	})
	if ok || ranges != nil {
		t.Fatalf("unreadable untracked file = (%+v, %t)", ranges, ok)
	}
}

func TestChangedLineRangesReturnsTheOnlyUntrackedFileWithoutAskingForADiff(t *testing.T) {
	root := t.TempDir()
	writeTrackedFixture(t, root, "untracked.go")
	scriptedDiff(t, "", errors.New("unexpected diff"))
	ranges, ok := changedLineRangesWithLineCount(t.Context(), root, "", []string{"untracked.go"}, func(string) (int, bool) {
		return untrackedFixtureLineCount, true
	})
	want := map[string][]gomutants.LineRange{"untracked.go": {{First: 1, Last: 2}}}
	if !ok || !reflect.DeepEqual(ranges, want) {
		t.Fatalf("untracked-only ranges = (%+v, %t), want %+v", ranges, ok, want)
	}
}

func TestChangedLineRangesRejectsACanceledDiffEvenWhenGitReturnsOutput(t *testing.T) {
	root := t.TempDir()
	writeTrackedFixture(t, root, "value.go")
	ctx, cancel := context.WithCancel(t.Context())
	previous := gitNamesOutput
	t.Cleanup(func() { gitNamesOutput = previous })
	gitNamesOutput = func(_ context.Context, _ string, arguments []string) ([]byte, error) {
		if len(arguments) != 0 && arguments[0] == "ls-files" {
			return []byte("value.go"), nil
		}
		cancel()
		return []byte("+++ b/value.go\n@@ -1 +1 @@\n"), nil
	}
	ranges, ok := changedLineRanges(ctx, root, "", []string{"value.go"})
	if ok || ranges != nil {
		t.Fatalf("canceled diff = (%+v, %t)", ranges, ok)
	}
}

func TestMutationSelectionRefusesToNarrowWhenATestChanged(t *testing.T) {
	t.Parallel()
	ranges := map[string][]gomutants.LineRange{"value.go": {{First: 1, Last: 2}}}
	narrowed := mutationSelection(impactSelection{
		changed: []string{"value.go", "value_test.go"}, ranges: ranges,
	})
	if narrowed != nil {
		t.Fatalf("selection = %+v, want none when a test changed", narrowed)
	}
	narrowed = mutationSelection(impactSelection{changed: []string{"value.go"}, ranges: ranges})
	if narrowed == nil || !reflect.DeepEqual(narrowed.Lines, ranges) {
		t.Fatalf("selection = %+v, want the changed lines", narrowed)
	}
}

func TestMutationSelectionNarrowsNothingWithoutRanges(t *testing.T) {
	t.Parallel()
	if narrowed := mutationSelection(impactSelection{broad: true}); narrowed != nil {
		t.Fatalf("selection = %+v, want none for a broad scope", narrowed)
	}
	if narrowed := mutationSelection(impactSelection{changed: []string{"value.go"}}); narrowed != nil {
		t.Fatalf("selection = %+v, want none when the diff was not read", narrowed)
	}
	if narrowed := mutationSelection(impactSelection{
		changed: []string{"value.go"}, broad: true,
		ranges: map[string][]gomutants.LineRange{"value.go": {{First: 1, Last: 1}}},
	}); narrowed != nil {
		t.Fatalf("selection = %+v, want none for a broad scope with diagnostic ranges", narrowed)
	}
}

func writeTrackedFixture(t *testing.T, root, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte("package value\n"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
}

func TestAHunkSpanReadsTheCountTheHeaderGivesOrTheOneItImplies(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		header string
		first  int
		last   int
		read   bool
	}{
		{name: "a hunk of one line", header: "@@ -1 +12 @@", first: 12, last: 12, read: true},
		{name: "a hunk of three lines", header: "@@ -1 +12,3 @@", first: 12, last: 14, read: true},
		{name: "a hunk that only deletes", header: "@@ -1 +12,0 @@", first: 12, last: 11, read: true},
		{name: "a hunk that opens the file", header: "@@ -0,0 +1,2 @@", first: 1, last: 2, read: true},
		{name: "a hunk before the first line", header: "@@ -1 +0 @@"},
		{name: "a hunk of a negative count", header: "@@ -1 +12,-1 @@"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			match := hunkHeader.FindStringSubmatch(test.header)
			if match == nil {
				if test.read {
					t.Fatalf("the header %q was not recognised at all", test.header)
				}
				return
			}
			span, read := hunkSpan(match)
			if read != test.read {
				t.Fatalf("hunkSpan(%q) = (%+v, %t), want %t", test.header, span, read, test.read)
			}
			if !read {
				if span != (gomutants.LineRange{}) {
					t.Errorf("a header it refused answered with %+v, want no range at all", span)
				}
				return
			}
			if span.First != test.first || span.Last != test.last {
				t.Fatalf("hunkSpan(%q) = %+v, want %d to %d", test.header, span, test.first, test.last)
			}
		})
	}
}

func TestAFileLineCountCountsTheLastLineWhetherOrNotItEnds(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		contents string
		want     int
		read     bool
	}{
		{name: "a file of one ended line", contents: "one\n", want: 1, read: true},
		{name: "a file of one unended line", contents: "one", want: 1, read: true},
		{name: "a file of three ended lines", contents: "one\ntwo\nthree\n", want: 3, read: true},
		{name: "a file of three where the last does not end", contents: "one\ntwo\nthree", want: 3, read: true},
		{name: "a file of nothing at all", read: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "value.go")
			if err := os.WriteFile(path, []byte(test.contents), filemode.ReadableFile); err != nil {
				t.Fatal(err)
			}
			lines, read := fileLineCount(path)
			if read != test.read || lines != test.want {
				t.Fatalf("fileLineCount = (%d, %t), want (%d, %t)", lines, read, test.want, test.read)
			}
		})
	}
	if lines, read := fileLineCount(filepath.Join(t.TempDir(), "absent.go")); read || lines != 0 {
		t.Fatalf("a file that is not there counted (%d, %t), want (0, false)", lines, read)
	}
}

func TestChangedLineRangesCountsEveryLineOfAFileGitDoesNotTrack(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "added.go"),
		[]byte("package added\n\nfunc Added() int { return 1 }\n"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	writeTrackedFixture(t, root, "value.go")
	scriptedDiff(t, "+++ b/value.go\n@@ -1 +1 @@\n", nil, "value.go")

	ranges, ok := changedLineRanges(t.Context(), root, "", []string{"added.go", "value.go"})
	if !ok {
		t.Fatal("a changeset holding a file git does not track was refused")
	}
	added, selected := ranges["added.go"]
	if !selected {
		t.Fatalf("the ranges name %v, want every line of the file git does not track", ranges)
	}
	want := []gomutants.LineRange{{First: 1, Last: 3}}
	if !reflect.DeepEqual(added, want) {
		t.Fatalf("the untracked file selected %+v, want %+v", added, want)
	}
	if _, diffed := ranges["value.go"]; !diffed {
		t.Errorf("the ranges name %v, want the tracked file the diff described too", ranges)
	}
}

func TestChangedLineRangesSelectsNothingOfAnEmptyUntrackedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "empty.go"), nil, filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
	scriptedDiff(t, "", nil)

	ranges, ok := changedLineRanges(t.Context(), root, "", []string{"empty.go"})
	if !ok {
		t.Fatal("a changeset holding an empty untracked file was refused")
	}
	if len(ranges) != 0 {
		t.Fatalf("the ranges name %v, want nothing for a file with no line in it", ranges)
	}
}

func TestChangedLineRangesFailsClosedWhenGitCannotListWhatItTracks(t *testing.T) {
	root := t.TempDir()
	writeTrackedFixture(t, root, "value.go")
	previous := gitNamesOutput
	t.Cleanup(func() { gitNamesOutput = previous })
	gitNamesOutput = func(_ context.Context, _ string, arguments []string) ([]byte, error) {
		if len(arguments) != 0 && arguments[0] == "ls-files" {
			return nil, errors.New("git exploded")
		}
		return nil, nil
	}

	if ranges, ok := changedLineRanges(t.Context(), root, "", []string{"value.go"}); ok || ranges != nil {
		t.Fatalf("a changeset git could not classify was accepted: (%+v, %t)", ranges, ok)
	}
}

func TestMutationSelectionKeepsOnlyTheGoSourceItWasGiven(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		ranges map[string][]gomutants.LineRange
		want   []string
	}{
		{
			name:   "one Go source file",
			ranges: map[string][]gomutants.LineRange{"value.go": {{First: 1, Last: 2}}},
			want:   []string{"value.go"},
		},
		{
			name: "a Go source file beside a test and a document",
			ranges: map[string][]gomutants.LineRange{
				"value.go": {{First: 1, Last: 2}}, "value_test.go": {{First: 1, Last: 2}},
				"README.md": {{First: 1, Last: 2}},
			},
			want: []string{"value.go"},
		},
		{
			name:   "nothing but a document",
			ranges: map[string][]gomutants.LineRange{"README.md": {{First: 1, Last: 2}}},
		},
		{
			name:   "nothing but a test",
			ranges: map[string][]gomutants.LineRange{"other_test.go": {{First: 1, Last: 2}}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			narrowed := mutationSelection(impactSelection{changed: []string{"value.go"}, ranges: test.ranges})
			if len(test.want) == 0 {
				if narrowed != nil {
					t.Fatalf("selection = %+v, want none when nothing it can mutate changed", narrowed)
				}
				return
			}
			if narrowed == nil {
				t.Fatal("selection = none, want the Go source that changed")
			}
			if len(narrowed.Lines) != len(test.want) {
				t.Fatalf("selection names %d files, want %d: %+v", len(narrowed.Lines), len(test.want), narrowed.Lines)
			}
			for _, path := range test.want {
				if _, named := narrowed.Lines[path]; !named {
					t.Errorf("selection does not name %q: %+v", path, narrowed.Lines)
				}
			}
		})
	}
}

func TestChangedLineRangesAsksGitAboutTheReferenceItWasGivenOrTheHead(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		reference string
		want      string
	}{
		{name: "a reference it was given", reference: "origin/main", want: "origin/main"},
		{name: "no reference at all", want: "HEAD"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTrackedFixture(t, root, "value.go")
			var asked []string
			previous := gitNamesOutput
			t.Cleanup(func() { gitNamesOutput = previous })
			gitNamesOutput = func(_ context.Context, _ string, arguments []string) ([]byte, error) {
				if len(arguments) != 0 && arguments[0] == "ls-files" {
					return []byte("value.go"), nil
				}
				asked = arguments
				return []byte("+++ b/value.go\n@@ -1 +1 @@\n"), nil
			}
			if _, ok := changedLineRanges(t.Context(), root, test.reference, []string{"value.go"}); !ok {
				t.Fatal("the diff was refused")
			}
			if !slices.Contains(asked, test.want) {
				t.Fatalf("git was asked %q, want it to name %q", asked, test.want)
			}
		})
	}
}

func TestAHunkSpanRefusesACountThatIsNoNumberAndAcceptsOneOfNone(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		first string
		count string
		read  bool
	}{
		{name: "a first line and a count", first: "12", count: "3", read: true},
		{name: "a first line and no count at all", first: "12", read: true},
		{name: "a count of none", first: "12", count: "0", read: true},
		{name: "a first line that is no number", first: "twelve", count: "3"},
		{name: "a first line that overflows", first: strings.Repeat("9", overflowingDecimalDigits), count: "3"},
		{name: "a count that is no number", first: "12", count: "three"},
		{name: "a count below zero", first: "12", count: "-1"},
		{name: "a first line below one", first: "0", count: "3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			span, read := hunkSpan([]string{"", test.first, test.count})
			if read != test.read {
				t.Fatalf("%s read=%t, want %t (%+v)", test.name, read, test.read, span)
			}
		})
	}
}

func TestSplitTrackedChangesPreservesItsThreeFailureShapes(t *testing.T) {
	root := t.TempDir()
	missingTracked, missingUntracked, listed := splitTrackedChanges(t.Context(), root, []string{"absent.go"})
	if !listed || missingTracked != nil || missingUntracked != nil {
		t.Fatalf("absent changes = (%v, %v, %t)", missingTracked, missingUntracked, listed)
	}

	writeTrackedFixture(t, root, "value.go")
	previous := gitNamesOutput
	t.Cleanup(func() { gitNamesOutput = previous })
	for _, test := range []struct {
		name   string
		output []byte
		err    error
	}{
		{name: "listing failure", err: errors.New("git failed")},
		{name: "unsafe listing", output: []byte("../value.go\x00")},
	} {
		t.Run(test.name, func(t *testing.T) {
			gitNamesOutput = func(context.Context, string, []string) ([]byte, error) {
				return test.output, test.err
			}
			tracked, untracked, ok := splitTrackedChanges(t.Context(), root, []string{"value.go"})
			if ok || tracked != nil || untracked != nil {
				t.Fatalf("failed split = (%v, %v, %t)", tracked, untracked, ok)
			}
		})
	}
}
