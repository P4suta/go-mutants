// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestSelectionNormalisesAndRejectsRanges is the whole of what a caller's
// [Selection] goes through before a catalogue is narrowed by it.
//
// Normalisation is not tidiness. [Catalog.Selection] hands the value back, a
// consumer diffs two of them to ask whether two runs selected the same lines,
// and the intersection rule reads the ranges once per mutant — so a selection
// that arrived as "lines 5-7, 1-3 and 4" and one that arrived as "lines 1-7"
// have to become the same value or the answer to "did anything change?" is
// about the order somebody appended their hunks in.
//
// The refusals are the other half. Every one of them names a request that
// *looks* like a narrowing and would silently select nothing: a path spelled
// with backslashes, a path that escapes the module, a range starting at line
// zero, a range that runs backwards. Failing them closed is the rule
// `--changed` follows for the same reason — a selection nobody can satisfy
// reports a perfect score for a run that measured nothing.
func TestSelectionNormalisesAndRejectsRanges(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		in   *Selection
		want *Selection
	}{
		{
			name: "nothing selected is nothing normalised",
			in:   nil,
			want: nil,
		},
		{
			name: "a selection with no paths is a selection of nothing",
			in:   &Selection{},
			want: &Selection{Lines: map[string][]LineRange{}},
		},
		{
			name: "a path is cleaned",
			in:   &Selection{Lines: map[string][]LineRange{"./a/../a/x.go": {{First: 3, Last: 4}}}},
			want: &Selection{Lines: map[string][]LineRange{"a/x.go": {{First: 3, Last: 4}}}},
		},
		{
			name: "two spellings of one path become one entry",
			in: &Selection{Lines: map[string][]LineRange{
				"./x.go": {{First: 9, Last: 9}},
				"x.go":   {{First: 1, Last: 2}},
			}},
			want: &Selection{Lines: map[string][]LineRange{
				"x.go": {{First: 1, Last: 2}, {First: 9, Last: 9}},
			}},
		},
		{
			name: "ranges are sorted",
			in: &Selection{Lines: map[string][]LineRange{
				"x.go": {{First: 20, Last: 21}, {First: 1, Last: 2}},
			}},
			want: &Selection{Lines: map[string][]LineRange{
				"x.go": {{First: 1, Last: 2}, {First: 20, Last: 21}},
			}},
		},
		{
			name: "overlapping ranges are merged",
			in: &Selection{Lines: map[string][]LineRange{
				"x.go": {{First: 1, Last: 10}, {First: 3, Last: 4}},
			}},
			want: &Selection{Lines: map[string][]LineRange{
				"x.go": {{First: 1, Last: 10}},
			}},
		},
		{
			name: "adjacent ranges are merged",
			in: &Selection{Lines: map[string][]LineRange{
				"x.go": {{First: 5, Last: 7}, {First: 1, Last: 3}, {First: 4, Last: 4}},
			}},
			want: &Selection{Lines: map[string][]LineRange{
				"x.go": {{First: 1, Last: 7}},
			}},
		},
		{
			name: "a path with no ranges selects nothing and is dropped",
			in: &Selection{Lines: map[string][]LineRange{
				"x.go": nil,
				"y.go": {{First: 2, Last: 2}},
			}},
			want: &Selection{Lines: map[string][]LineRange{
				"y.go": {{First: 2, Last: 2}},
			}},
		},
		{
			name: "a path naming no source file is kept, and selects nothing",
			in: &Selection{Lines: map[string][]LineRange{
				"docs/README.md": {{First: 1, Last: 400}},
			}},
			want: &Selection{Lines: map[string][]LineRange{
				"docs/README.md": {{First: 1, Last: 400}},
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normaliseSelection(test.in)
			if err != nil {
				t.Fatalf("normaliseSelection(%+v) = %v, want a normalised selection", test.in, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("normaliseSelection(%+v) = %+v, want %+v", test.in, got, test.want)
			}
		})
	}

	for _, test := range []struct {
		name string
		in   *Selection
		says string
	}{
		{
			name: "an empty path",
			in:   &Selection{Lines: map[string][]LineRange{"": {{First: 1, Last: 1}}}},
			says: `""`,
		},
		{
			name: "an absolute path",
			in:   &Selection{Lines: map[string][]LineRange{"/tmp/x.go": {{First: 1, Last: 1}}}},
			says: `"/tmp/x.go"`,
		},
		{
			name: "a backslash-separated path",
			in:   &Selection{Lines: map[string][]LineRange{`a\x.go`: {{First: 1, Last: 1}}}},
			says: `"a\\x.go"`,
		},
		{
			name: "a path that escapes the module",
			in:   &Selection{Lines: map[string][]LineRange{"../x.go": {{First: 1, Last: 1}}}},
			says: `"../x.go"`,
		},
		{
			name: "a path that escapes the module after cleaning",
			in:   &Selection{Lines: map[string][]LineRange{"a/../../x.go": {{First: 1, Last: 1}}}},
			says: `"a/../../x.go"`,
		},
		{
			name: "a path that is the module root",
			in:   &Selection{Lines: map[string][]LineRange{".": {{First: 1, Last: 1}}}},
			says: `"."`,
		},
		{
			name: "a first line below one",
			in:   &Selection{Lines: map[string][]LineRange{"x.go": {{First: 0, Last: 4}}}},
			says: `"x.go"`,
		},
		{
			name: "a negative first line",
			in:   &Selection{Lines: map[string][]LineRange{"x.go": {{First: -3, Last: -1}}}},
			says: `"x.go"`,
		},
		{
			name: "a range that runs backwards",
			in:   &Selection{Lines: map[string][]LineRange{"x.go": {{First: 9, Last: 3}}}},
			says: `"x.go"`,
		},
	} {
		t.Run("rejects "+test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normaliseSelection(test.in)
			if err == nil {
				t.Fatalf("normaliseSelection(%+v) = %+v, want a refusal", test.in, got)
			}
			if got != nil {
				t.Errorf("normaliseSelection(%+v) returned %+v beside its refusal; a"+
					" half-normalised selection is one a caller could act on", test.in, got)
			}
			if !errors.Is(err, ErrInvalidSelection) {
				t.Errorf("normaliseSelection(%+v) = %v, which errors.Is does not match"+
					" ErrInvalidSelection; a consumer resolving user input has to be able"+
					" to tell this from an engine failure", test.in, err)
			}
			if !strings.Contains(err.Error(), test.says) {
				t.Errorf("normaliseSelection(%+v) = %q, which does not name %s; the caller has"+
					" to be told which entry it wrote wrong", test.in, err, test.says)
			}
		})
	}
}

// TestSelectionMarksMutantsWhoseLinesAreTouched is the intersection rule, over
// a catalogue built by hand so that the multi-line cases are actually present.
//
// The rule is `[Line, EndLine] ∩ [First, Last] ≠ ∅` per path, and it is the one
// `go-mutants run --changed` applies to a diff. The rows that matter are the
// ones a Line-only comparison would get wrong: a range touching the *last* line
// of a three-line condition and not its first selects that condition, because a
// multi-line expression edited anywhere is an edited expression.
func TestSelectionMarksMutantsWhoseLinesAreTouched(t *testing.T) {
	t.Parallel()

	// One single-line mutant on line 10, one spanning lines 10 to 12, and one
	// in another file entirely.
	mutants := func() []Mutant {
		return []Mutant{
			{ID: "single", Path: "a.go", Line: 10, EndLine: 10, Original: "=="},
			{ID: "spanning", Path: "a.go", Line: 10, EndLine: 12, Original: "a ||\n\tb ||\n\tc"},
			{ID: "elsewhere", Path: "b.go", Line: 10, EndLine: 10, Original: "<"},
			{ID: "nowhere", Path: "", Line: 0, EndLine: 0, Original: "+"},
		}
	}

	for _, test := range []struct {
		name      string
		selection *Selection
		want      map[string]bool
	}{
		{
			name:      "no selection selects everything",
			selection: nil,
			want:      map[string]bool{"single": true, "spanning": true, "elsewhere": true, "nowhere": true},
		},
		{
			name:      "a selection naming nothing selects nothing that has a place",
			selection: &Selection{Lines: map[string][]LineRange{}},
			want:      map[string]bool{"single": false, "spanning": false, "elsewhere": false, "nowhere": true},
		},
		{
			name:      "a range on the first line selects both mutants there",
			selection: &Selection{Lines: map[string][]LineRange{"a.go": {{First: 10, Last: 10}}}},
			want:      map[string]bool{"single": true, "spanning": true, "elsewhere": false, "nowhere": true},
		},
		{
			name:      "a range on the last line alone selects only the multi-line mutant",
			selection: &Selection{Lines: map[string][]LineRange{"a.go": {{First: 12, Last: 12}}}},
			want:      map[string]bool{"single": false, "spanning": true, "elsewhere": false, "nowhere": true},
		},
		{
			name:      "a range inside the span selects the multi-line mutant",
			selection: &Selection{Lines: map[string][]LineRange{"a.go": {{First: 11, Last: 11}}}},
			want:      map[string]bool{"single": false, "spanning": true, "elsewhere": false, "nowhere": true},
		},
		{
			name:      "a range spanning the whole file selects everything in it",
			selection: &Selection{Lines: map[string][]LineRange{"a.go": {{First: 1, Last: 400}}}},
			want:      map[string]bool{"single": true, "spanning": true, "elsewhere": false, "nowhere": true},
		},
		{
			name:      "a disjoint range selects nothing",
			selection: &Selection{Lines: map[string][]LineRange{"a.go": {{First: 13, Last: 20}}}},
			want:      map[string]bool{"single": false, "spanning": false, "elsewhere": false, "nowhere": true},
		},
		{
			name:      "a range just above the span selects nothing",
			selection: &Selection{Lines: map[string][]LineRange{"a.go": {{First: 1, Last: 9}}}},
			want:      map[string]bool{"single": false, "spanning": false, "elsewhere": false, "nowhere": true},
		},
		{
			name: "a path that names no mutant selects nothing",
			selection: &Selection{Lines: map[string][]LineRange{
				"c.go": {{First: 1, Last: 400}},
			}},
			want: map[string]bool{"single": false, "spanning": false, "elsewhere": false, "nowhere": true},
		},
		{
			name: "each path is intersected with its own ranges",
			selection: &Selection{Lines: map[string][]LineRange{
				"a.go": {{First: 12, Last: 12}},
				"b.go": {{First: 10, Last: 10}},
			}},
			want: map[string]bool{"single": false, "spanning": true, "elsewhere": true, "nowhere": true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := mutants()
			applySelection(got, test.selection)
			for _, mutant := range got {
				if want := test.want[mutant.ID]; mutant.Selected != want {
					t.Errorf("mutant %s covering lines %d-%d of %q is Selected=%t, want %t",
						mutant.ID, mutant.Line, mutant.EndLine, mutant.Path, mutant.Selected, want)
				}
			}
		})
	}
}

// TestSelectionInACatalogueIsACopyNobodyElseHolds is the same promise
// [Session.Catalog] makes about the rest of the value, for the one field that
// is a pointer to a map of slices.
//
// A caller may keep and edit the catalogue it is handed. If the selection
// travelled by reference, editing that copy would rewrite what the *session*
// says it selected — and the session's own answer is what a second call to
// Catalog would report and what a consumer diffs a later run against.
func TestSelectionInACatalogueIsACopyNobodyElseHolds(t *testing.T) {
	t.Parallel()

	original := &Selection{Lines: map[string][]LineRange{"x.go": {{First: 1, Last: 2}}}}
	copied := cloneSelection(original)
	if !reflect.DeepEqual(copied, original) {
		t.Fatalf("cloneSelection() = %+v, want %+v", copied, original)
	}
	copied.Lines["x.go"][0].Last = 999
	copied.Lines["y.go"] = []LineRange{{First: 4, Last: 4}}
	if original.Lines["x.go"][0].Last != 2 {
		t.Error("editing the copy's ranges rewrote the original's")
	}
	if _, added := original.Lines["y.go"]; added {
		t.Error("adding a path to the copy added it to the original")
	}
	if cloneSelection(nil) != nil {
		t.Error("cloneSelection(nil) invented a selection; nil means everything is selected")
	}

	// And the same claim through [cloneCatalog], which is what [Session.Catalog]
	// actually calls. The clone of the field is one line there and deleting it
	// leaves every other assertion in this package passing, because nothing else
	// hands out two catalogues from one session and edits the first.
	held := Catalog{Selection: &Selection{Lines: map[string][]LineRange{"x.go": {{First: 1, Last: 2}}}}}
	handed := cloneCatalog(held)
	handed.Selection.Lines["x.go"][0].Last = 999
	handed.Selection.Lines["y.go"] = []LineRange{{First: 4, Last: 4}}
	if held.Selection.Lines["x.go"][0].Last != 2 {
		t.Error("editing the catalogue a caller was handed rewrote the session's own ranges")
	}
	if _, added := held.Selection.Lines["y.go"]; added {
		t.Error("adding a path to the catalogue a caller was handed added it to the session's own")
	}
}
