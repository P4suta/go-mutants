// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package gomutants

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/P4suta/go-mutants/internal/gitdiff"
)

// A LineRange is an inclusive, 1-based run of lines in one file: First is the
// first line it covers and Last the last, and First is never above Last.
//
// It is the unit a diff hunk, an editor's changed region and a review comment
// all come in, so a caller narrowing a catalogue by any of those hands them
// over as they are rather than expanding them into line numbers.
type LineRange struct {
	First, Last int
}

// A Selection is the lines a caller cares about, by file.
//
// Lines maps a module-relative, '/'-separated path onto the ranges selected in
// it. A path the map does not name selects nothing; a path it names that the
// module does not hold selects nothing either, which is what lets a selection
// be built straight out of a diff without filtering the deletions, the
// documents and the testdata out of it first.
//
// Paths are compared **exactly** against [Mutant.Path], byte for byte after
// cleaning: same case, same Unicode normalisation form. So `Clamp.go` selects
// nothing in a module whose file is `clamp.go`, even on a filesystem that would
// open either — and a path decomposed by macOS's own APIs selects nothing
// against a catalogue that spells it composed. This is `--changed`'s behaviour
// and not a separate rule: a diff's paths are the repository's, a catalogue's
// are the module's, and both come from the same bytes on disk. A consumer
// deriving paths from anything else — a user's argument, an editor, a
// case-folding lookup — passes the spelling the tree uses.
//
// A nil *Selection is not an empty one. Nil means no narrowing was asked for
// and every mutant is selected; a non-nil Selection naming no path at all
// selects nothing, which is a request the engine honours rather than a mistake
// it corrects.
type Selection struct {
	Lines map[string][]LineRange
}

// normaliseSelection turns a caller's selection into the canonical value
// [Catalog.Selection] hands back, or refuses it with [ErrInvalidSelection].
//
// Canonical means three things, and each of them is a question a consumer asks
// of the value rather than housekeeping. Paths are [path.Clean]ed, so the two
// spellings of one file become one entry and cannot select two different sets.
// Ranges are sorted and merged, so "5-7, 1-3, 4" and "1-7" are the same value
// and a consumer diffing this run's selection against the last one is comparing
// what was selected instead of the order somebody appended their hunks in. And
// a path named with no ranges at all never reaches the result, because a file
// with no lines selected and a file nobody named select exactly the same
// mutants — none — and two values that mean the same thing must not compare
// unequal.
//
// The refusals are the entries that look like a narrowing and would select
// nothing: see [ErrInvalidSelection] for why they fail closed. Nothing is
// returned beside one, so there is no half-normalised value for a caller to act
// on.
//
// The result never aliases the caller's map or its slices: the value ends up in
// a Session, which outlives the options struct it came from.
func normaliseSelection(selection *Selection) (*Selection, error) {
	if selection == nil {
		return nil, nil
	}
	lines := make(map[string][]LineRange, len(selection.Lines))
	for _, raw := range slices.Sorted(maps.Keys(selection.Lines)) {
		cleaned, err := selectionPath(raw)
		if err != nil {
			return nil, err
		}
		for _, r := range selection.Lines[raw] {
			if err = checkLineRange(raw, r); err != nil {
				return nil, err
			}
			lines[cleaned] = append(lines[cleaned], r)
		}
	}
	for cleaned, ranges := range lines {
		lines[cleaned] = fromDiffRanges(gitdiff.Merge(toDiffRanges(ranges)))
	}
	return &Selection{Lines: lines}, nil
}

// selectionPath cleans one selection path and refuses one that is not a path
// inside the module.
//
// The rules are [Mutant.Path]'s, because that is what the path is compared
// against: module-relative, '/'-separated, no element that escapes. Backslashes
// are refused rather than translated — a Windows caller building a path with
// filepath.Join gets a clear refusal here instead of a selection that silently
// matches nothing, and translating would make `a\b.go`, which is a legal file
// name on every other platform, mean two different files on two machines.
func selectionPath(raw string) (string, error) {
	if raw == "" {
		return "", invalidSelectionPath(raw, "a selection path names no file")
	}
	if strings.Contains(raw, `\`) {
		return "", invalidSelectionPath(raw, "a selection path is '/'-separated, and this one holds a backslash")
	}
	if strings.HasPrefix(raw, "/") {
		return "", invalidSelectionPath(raw, "a selection path is module-relative, and this one is absolute")
	}
	cleaned := path.Clean(raw)
	if cleaned == "." {
		return "", invalidSelectionPath(raw, "a selection path names a file, and this one names the module root")
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", invalidSelectionPath(raw, "a selection path stays inside the module, and this one escapes it")
	}
	return cleaned, nil
}

// checkLineRange refuses a range that names no lines.
//
// Both refusals are a caller having computed something rather than typed it: a
// zero First is an off-by-one against a 0-based editor, and a Last below First
// is two numbers that arrived the wrong way round. Selecting the empty set for
// either would report a score for a run that measured nothing.
func checkLineRange(raw string, r LineRange) error {
	if r.First < 1 {
		return invalidSelectionRange(raw, r, "a line range is 1-based, and this one starts below line 1")
	}
	if r.Last < r.First {
		return invalidSelectionRange(raw, r, "a line range ends at or after it starts, and this one runs backwards")
	}
	return nil
}

func invalidSelectionPath(raw, why string) error {
	return fmt.Errorf("gomutants: prepare selection path %s: %w: %s", strconv.Quote(raw), ErrInvalidSelection, why)
}

func invalidSelectionRange(raw string, r LineRange, why string) error {
	return fmt.Errorf("gomutants: prepare selection range %d-%d in %s: %w: %s",
		r.First, r.Last, strconv.Quote(raw), ErrInvalidSelection, why)
}

// applySelection sets [Mutant.Selected] on every mutant, from a selection that
// has already been through [normaliseSelection].
//
// The rule is `[Line, EndLine] ∩ [First, Last] ≠ ∅`, per path, and it is not a
// second implementation of it. The ranges are handed to [gitdiff.Changed] — the
// value `go-mutants run --changed` intersects a diff with — and Touches is the
// same method internal/engine's own selection stage calls, over a span both
// reach through [endLine] and internal/coverage behind it. Two spellings of "is
// this mutant on one of those lines" would agree until one of them met a
// multi-line condition or a range that ends exactly where a span begins.
//
// A mutant carrying no place is selected rather than dropped, which is the
// choice `--changed` makes and for the same reason. The catalogue's own
// invariant is that every mutant has Line >= 1, so this cannot happen; being
// wrong about that costs one execution here and would silently take a mutant
// out of the consumer's run there.
func applySelection(mutants []Mutant, selection *Selection) {
	if selection == nil {
		for i := range mutants {
			mutants[i].Selected = true
		}
		return
	}
	changed := gitdiff.Changed{Files: make(map[string][]gitdiff.Range, len(selection.Lines))}
	for file, ranges := range selection.Lines {
		changed.Files[file] = toDiffRanges(ranges)
	}
	for i := range mutants {
		m := &mutants[i]
		m.Selected = m.Path == "" || m.Line < 1 || changed.Touches(m.Path, m.Line, m.EndLine)
	}
}

// cloneSelection deep-copies a selection, keeping nil as nil.
//
// [Session.Catalog] promises a value a caller may keep and edit, and a
// selection is the one field of a [Catalog] that is a pointer to a map of
// slices — three levels of sharing, any of which would let a caller's edit
// rewrite what the session says it selected.
func cloneSelection(selection *Selection) *Selection {
	if selection == nil {
		return nil
	}
	lines := make(map[string][]LineRange, len(selection.Lines))
	for file, ranges := range selection.Lines {
		lines[file] = slices.Clone(ranges)
	}
	return &Selection{Lines: lines}
}

// toDiffRanges and fromDiffRanges convert between the public range and the one
// internal/gitdiff canonicalises and intersects. The two types are the same
// pair of numbers with the same meaning, and the conversion exists so that the
// public API owes nothing to an internal package's shape.
func toDiffRanges(ranges []LineRange) []gitdiff.Range {
	out := make([]gitdiff.Range, len(ranges))
	for i, r := range ranges {
		out[i] = gitdiff.Range{First: r.First, Last: r.Last}
	}
	return out
}

func fromDiffRanges(ranges []gitdiff.Range) []LineRange {
	out := make([]LineRange, len(ranges))
	for i, r := range ranges {
		out[i] = LineRange{First: r.First, Last: r.Last}
	}
	return out
}
