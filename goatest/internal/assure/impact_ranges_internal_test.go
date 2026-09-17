// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package assure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	gomutants "github.com/P4suta/go-mutants"
	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

func scriptedDiff(t *testing.T, output string, err error) {
	t.Helper()
	previous := gitNamesOutput
	t.Cleanup(func() { gitNamesOutput = previous })
	gitNamesOutput = func(context.Context, string, []string) ([]byte, error) {
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
`, nil)
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
	scriptedDiff(t, "+++ b/value.go\n@@ -12,3 +13,0 @@\n", nil)
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
			scriptedDiff(t, test.output, test.err)
			if _, ok := changedLineRanges(t.Context(), root, "", []string{"value.go"}); ok {
				t.Fatal("a diff that could not be trusted was accepted")
			}
		})
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
}

func writeTrackedFixture(t *testing.T, root, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte("package value\n"), filemode.ReadableFile); err != nil {
		t.Fatal(err)
	}
}
