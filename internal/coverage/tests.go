// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage

import (
	"cmp"
	"slices"
)

type TestKey struct {
	ImportPath string
	Name       string
}

func compareTestKeys(a, b TestKey) int {
	if c := cmp.Compare(a.ImportPath, b.ImportPath); c != 0 {
		return c
	}
	return cmp.Compare(a.Name, b.Name)
}

type TestOptions struct {
	ModulePath string
	Mutants    []Mutant
	Profiles   map[TestKey]Profile
}

type TestResult struct {
	Covering  map[string][]TestKey
	Uncovered []string
	Tests     []TestKey
	Matched   int
}

func (r TestResult) CoveringOf(id string) []TestKey { return r.Covering[id] }

func MapTests(opts TestOptions) TestResult {
	tests := make([]TestKey, 0, len(opts.Profiles))
	for key := range opts.Profiles {
		tests = append(tests, key)
	}
	slices.SortFunc(tests, compareTestKeys)

	modules := modulesOf(opts.ModulePath, opts.Mutants)
	matched := make(map[string]bool)
	indexes := make([]fileIndex, len(tests))
	for i, key := range tests {
		indexes[i] = newFileIndex(opts.Profiles[key], modules, matched)
	}

	result := TestResult{
		Covering:  make(map[string][]TestKey, len(opts.Mutants)),
		Uncovered: make([]string, 0),
		Tests:     tests,
		Matched:   len(matched),
	}
	covering := make([][]TestKey, len(opts.Mutants))
	for _, group := range groupByFile(opts) {
		for i, key := range tests {
			intervals := indexes[i][group.path]
			if len(intervals) == 0 {
				continue
			}
			for _, placed := range group.mutants {
				if overlaps(intervals, placed.start, placed.end) {
					covering[placed.at] = append(covering[placed.at], key)
				}
			}
		}
	}
	for i, m := range opts.Mutants {
		if len(covering[i]) == 0 {
			result.Uncovered = append(result.Uncovered, m.ID)
			continue
		}
		result.Covering[m.ID] = covering[i]
	}
	return result
}

type fileGroup struct {
	path    string
	mutants []placedMutant
}

type placedMutant struct {
	at    int
	start int
	end   int
}

func groupByFile(opts TestOptions) []fileGroup {
	groups := make([]fileGroup, 0, 8)
	at := make(map[string]int, 8)
	for i, m := range opts.Mutants {
		path := profilePath(opts.ModulePath, m)
		index, seen := at[path]
		if !seen {
			index = len(groups)
			at[path] = index
			groups = append(groups, fileGroup{path: path})
		}
		groups[index].mutants = append(groups[index].mutants,
			placedMutant{at: i, start: m.StartLine, end: m.EndLine})
	}
	return groups
}
