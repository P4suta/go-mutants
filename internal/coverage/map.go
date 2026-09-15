// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage

import (
	"slices"
	"sort"
	"strings"
)

// A Mutant is one located mutant, reduced to what the mapping needs.
//
// It is deliberately not internal/discover's Located or internal/mutation's
// Mutant. This package is pure and the mapping rule is the part worth testing
// as a table; taking four fields rather than a catalogue is what lets a test
// state a case in one line, and what keeps a coverage bug from being reachable
// only through a full run.
type Mutant struct {
	// ID is the full activation identity. It is the key of every answer this
	// package gives and is never interpreted.
	ID string
	// Path is the module-relative source path with forward slashes, as
	// internal/discover reports it — "internal/alpha/alpha.go".
	Path string
	// ModulePath is the module Path is relative to, and is empty outside a
	// workspace, where [Options.ModulePath] is the one answer for every mutant.
	//
	// A workspace run has several, and the profile spells its files under
	// whichever module each belongs to: two modules can each hold an `app.go`,
	// and stripping one module's prefix off the other's file would make them
	// one file. See ADR 0012.
	ModulePath string
	// StartLine and EndLine are the 1-based, inclusive line interval the
	// mutant's span covers. A single-line edit has StartLine == EndLine; see
	// [EndLine] for how a caller derives the end from the original bytes.
	StartLine int
	EndLine   int
}

// EndLine returns the last line a span touches, given the line it starts on and
// the exact bytes it covers.
//
// It is a count of newlines and nothing more, and that is exact rather than
// approximate: [mutation.Candidate] documents Original as precisely the bytes
// the span covers, and Candidate.Validate refuses a candidate whose original
// text and span length disagree. So the newlines in the original text are
// exactly the line breaks inside the span, and there is no need to re-read the
// source to find out where the span ends.
func EndLine(startLine int, original string) int {
	return startLine + strings.Count(original, "\n")
}

// Options is everything [Map] needs.
type Options struct {
	// ModulePath is the module the mutants live in, as go.mod declares it. It
	// is what turns a profile's "example.com/m/internal/alpha/alpha.go" into
	// the module-relative "internal/alpha/alpha.go" a mutant is located by.
	//
	// Empty means the profile already spells its files module-relatively, which
	// is what a hand-written test fixture does, or that each mutant names its
	// own module, which is what a workspace run does. A single-module run always
	// names the module: a mismatch would leave every file unmatched and every
	// mutant uncovered, which is why [Result.Matched] exists for the caller to
	// check.
	ModulePath string

	// Mutants are the located mutants to decide about. The order is preserved
	// in [Result.Uncovered].
	Mutants []Mutant

	// Profiles are the parsed coverage profiles, keyed by the import path of
	// the test binary each was collected from. A binary that ran but covered
	// nothing belongs here with an empty profile, not omitted: "this binary
	// covers no mutant" and "this binary was never profiled" are different
	// statements, and only the first is something the mapping can act on.
	Profiles map[string]Profile
}

// A Result is what the mapping decided.
type Result struct {
	// Covering maps a mutant's id onto the sorted import paths of the test
	// binaries that cover it. A mutant covered by nothing has no entry rather
	// than an empty one, so a lookup answers the question directly.
	Covering map[string][]string

	// Uncovered are the ids no binary covers, in the order the mutants were
	// given. These are the mutants a run does not execute at all.
	Uncovered []string

	// Binaries are the import paths considered, sorted. It is what the report's
	// `coverage.binaries` states, and it is taken from the profiles rather than
	// counted by the caller so that the number in the document is the number
	// the mapping actually used.
	Binaries []string

	// Matched is how many distinct files in the profiles resolved to a path
	// under [Options.ModulePath].
	//
	// It is the mapping's own sanity check, and it exists because the failure it
	// catches is invisible: a module path that does not match what the toolchain
	// wrote produces a perfectly well formed answer in which every mutant is
	// uncovered, which a run would then report as a workspace with no test
	// coverage at all. A caller that sees zero here, with a non-empty set of
	// profiles, should fail open rather than believe it.
	Matched int
}

// CoveringOf returns the binaries covering a mutant, or nil.
func (r Result) CoveringOf(id string) []string { return r.Covering[id] }

// Map decides which test binaries cover each mutant.
//
// The rule is one sentence, and the package documentation argues for every
// clause of it: a binary covers a mutant when the binary's profile holds a
// block with a non-zero count, in the mutant's file, whose line interval
// overlaps the mutant's. Two intervals [a,b] and [c,d] overlap when a <= d and
// c <= b — the boundaries are inclusive, so a mutant on the last line of a
// covered block is covered.
//
// The output is deterministic: the covering lists are sorted by import path,
// [Result.Uncovered] keeps the caller's mutant order, and [Result.Binaries] is
// sorted. Nothing here iterates a map into a result.
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
		// Spelled once and not once per binary: how a profile names a mutant's
		// file is a fact about the mutant.
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
		// Already in sorted order: binaries is sorted and the loop above walks
		// it in that order. Sorting again here would be a second opinion about
		// a property the loop already has.
		result.Covering[m.ID] = covering
	}
	return result
}

// An interval is one inclusive line range that was reached.
type interval struct {
	start int
	end   int
}

// A fileIndex answers "did this binary reach any line in [start,end] of this
// file" in logarithmic time.
//
// The intervals of a file are sorted by start line and merged, so a lookup is
// one binary search rather than a scan of every block. That matters at the size
// these documents really are: a mid-sized module profiled per test binary is
// tens of thousands of blocks, asked about once per mutant per binary, and a
// linear scan would make the mapping cost more than the executions it saves.
type fileIndex map[string][]interval

// newFileIndex builds the index for one binary's profile, recording in matched
// every file the profile named.
//
// The index is keyed on the profile's own spelling of a file —
// "example.com/m/internal/alpha/alpha.go" — rather than on a module-relative
// path, and a mutant is looked up by the same spelling reassembled from its
// module and its path. That is what lets one profile carry two modules of a
// workspace: `app.go` in each of them is two files there and two keys here,
// where stripping a module prefix would have made them one.
//
// Uncovered blocks are dropped rather than stored with a flag: the only
// question ever asked is whether a *covered* block overlaps, so a file whose
// every block has a zero count indexes to an empty list — present, and covering
// nothing, which is exactly the fact the mapping needs.
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
			// The file is still recorded above, so that "profiled and never
			// reached" stays distinguishable from "never profiled".
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

// covers reports whether the file's covered intervals overlap [start,end].
func (f fileIndex) covers(path string, start, end int) bool {
	return overlaps(f[path], start, end)
}

// overlaps is [fileIndex.covers] with the file already found.
//
// The split is what lets a caller placing many mutants of one file find that
// file once rather than once per mutant: the lookup is a fact about the file
// and the search is a fact about the span, and only the second of them is a
// fact about the mutant. See [MapTests], where the difference is the catalogue
// times the suite.
func overlaps(intervals []interval, start, end int) bool {
	if len(intervals) == 0 || start > end {
		return false
	}
	// The first interval that could possibly reach start: intervals are sorted
	// and disjoint, so anything earlier ends before start does.
	i := sort.Search(len(intervals), func(i int) bool { return intervals[i].end >= start })
	return i < len(intervals) && intervals[i].start <= end
}

// merge sorts intervals by start line and joins the ones that touch or overlap,
// so that the search above can assume they are disjoint and ordered.
//
// Adjacent intervals are joined as well as overlapping ones — [3,5] and [6,9]
// become [3,9] — because the answer this structure gives is a yes or no about
// overlap, and two adjacent ranges answer it identically to one joined range
// while costing an extra comparison on every lookup.
func merge(intervals []interval) []interval {
	if len(intervals) < 2 {
		return intervals
	}
	slices.SortFunc(intervals, func(x, y interval) int {
		if c := x.start - y.start; c != 0 {
			return c
		}
		return x.end - y.end
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

// underModule reports whether a profile's file belongs to one of the modules
// the mutants are in, which is what [Result.Matched] counts.
//
// No modules at all is the hand-written fixture case, where a profile already
// spells its files module-relatively and every file it names is one of theirs.
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

// modulesOf is every module the run's mutants are in: the run's own, and each
// mutant's when it names one.
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

// profilePath is how a profile spells one mutant's file: under the module the
// mutant belongs to, which is its own when it names one and the run's otherwise.
//
// Neither, and the path is taken as it stands — which is what a hand-written
// test fixture writes, and the one case where a profile's files are already
// module-relative.
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
