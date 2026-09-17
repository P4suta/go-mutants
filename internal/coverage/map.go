// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage

import (
	"cmp"
	"slices"
	"sort"
	"strings"
)

type Mutant struct {
	ID         string
	Path       string
	ModulePath string
	StartLine  int
	EndLine    int
}

func EndLine(startLine int, original string) int {
	return startLine + strings.Count(original, "\n")
}

type Options struct {
	ModulePath string

	Mutants []Mutant

	Profiles map[string]Profile
}

type Result struct {
	Covering map[string][]string

	Uncovered []string

	Binaries []string

	Matched int
}

func (r Result) CoveringOf(id string) []string { return r.Covering[id] }

func Map(opts Options) Result {
	binaries := make([]string, 0, len(opts.Profiles))
	for importPath := range opts.Profiles {
		binaries = append(binaries, importPath)
	}
	slices.Sort(binaries)

	modules := modulesOf(opts.ModulePath, opts.Mutants)
	matched := make(map[string]bool)
	indexes := make(map[string]fileIndex, len(binaries))
	for _, importPath := range binaries {
		indexes[importPath] = newFileIndex(opts.Profiles[importPath], modules, matched)
	}

	result := Result{
		Covering:  make(map[string][]string, len(opts.Mutants)),
		Uncovered: make([]string, 0),
		Binaries:  binaries,
		Matched:   len(matched),
	}
	for _, m := range opts.Mutants {
		path := profilePath(opts.ModulePath, m)
		var covering []string
		for _, importPath := range binaries {
			if indexes[importPath].covers(path, m.StartLine, m.EndLine) {
				covering = append(covering, importPath)
			}
		}
		if len(covering) == 0 {
			result.Uncovered = append(result.Uncovered, m.ID)
			continue
		}
		result.Covering[m.ID] = covering
	}
	return result
}

type interval struct {
	start int
	end   int
}

type fileIndex map[string][]interval

func newFileIndex(profile Profile, modules []string, matched map[string]bool) fileIndex {
	index := make(fileIndex)
	for _, block := range profile.Blocks {
		path := block.File
		if path == "" {
			continue
		}
		if underModule(path, modules) {
			matched[path] = true
		}
		if !block.Covered() {
			if _, seen := index[path]; !seen {
				index[path] = nil
			}
			continue
		}
		index[path] = append(index[path], interval{start: block.StartLine, end: block.EndLine})
	}
	for path, intervals := range index {
		index[path] = merge(intervals)
	}
	return index
}

func (f fileIndex) covers(path string, start, end int) bool {
	return overlaps(f[path], start, end)
}

func overlaps(intervals []interval, start, end int) bool {
	if len(intervals) == 0 || start > end {
		return false
	}
	i := sort.Search(len(intervals), func(i int) bool { return intervals[i].end >= start })
	return i < len(intervals) && intervals[i].start <= end
}

func merge(intervals []interval) []interval {
	if len(intervals) < 2 {
		return intervals
	}
	slices.SortFunc(intervals, func(x, y interval) int {
		return cmp.Or(cmp.Compare(x.start, y.start), cmp.Compare(x.end, y.end))
	})
	merged := make([]interval, 0, len(intervals))
	merged = append(merged, intervals[0])
	for _, next := range intervals[1:] {
		last := &merged[len(merged)-1]
		if next.start <= last.end+1 {
			last.end = max(last.end, next.end)
			continue
		}
		merged = append(merged, next)
	}
	return merged
}

func underModule(file string, modules []string) bool {
	if len(modules) == 0 {
		return true
	}
	for _, module := range modules {
		if strings.HasPrefix(file, module+"/") {
			return true
		}
	}
	return false
}

func modulesOf(runModule string, mutants []Mutant) []string {
	var modules []string
	if runModule != "" {
		modules = append(modules, runModule)
	}
	for _, m := range mutants {
		if m.ModulePath != "" && !slices.Contains(modules, m.ModulePath) {
			modules = append(modules, m.ModulePath)
		}
	}
	return modules
}

func profilePath(runModule string, m Mutant) string {
	module := m.ModulePath
	if module == "" {
		module = runModule
	}
	if module == "" {
		return m.Path
	}
	return module + "/" + m.Path
}
