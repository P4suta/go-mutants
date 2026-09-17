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

type LineRange struct {
	First, Last int
}

type Selection struct {
	Lines map[string][]LineRange
}

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
