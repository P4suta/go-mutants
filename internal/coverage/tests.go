// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage

import (
	"cmp"
	"slices"
)

// TestKey names one test function of one test binary: the import path of the
// package the binary was built from, and the top-level test, example or fuzz
// target name as `-test.list` prints it.
//
// A subtest is not a key. The unit a binary can be asked to run in isolation
// is what `-test.run='^Name$'` selects, and a subtest runs only with its
// parent — so a mutant reached from inside `TestX/case_3` is a mutant `TestX`
// covers, and `TestX` is what runs against it.
type TestKey struct {
	ImportPath string
	Name       string
}

// compareTestKeys is the one order every list of keys in this package uses:
// import path first, then name, both by bytes.
func compareTestKeys(a, b TestKey) int {
	if c := cmp.Compare(a.ImportPath, b.ImportPath); c != 0 {
		return c
	}
	return cmp.Compare(a.Name, b.Name)
}

// TestOptions is what [MapTests] needs: the same mutants and module path
// [Options] carries, and a profile per test rather than per binary.
type TestOptions struct {
	// ModulePath is the module the profiles' file names are relative to; see
	// [Options.ModulePath].
	ModulePath string
	// Mutants are the mutants to place; see [Options.Mutants].
	Mutants []Mutant
	// Profiles holds one profile per test — the coverage that test alone
	// produced, collected by running its binary with `-test.run='^Name$'` and
	// a coverage directory of its own.
	Profiles map[TestKey]Profile
}

// TestResult is [Result] keyed by test rather than by binary.
type TestResult struct {
	// Covering maps a mutant's id to the tests whose profiles reach its
	// lines, in [compareTestKeys] order. A mutant no test reaches is absent.
	Covering map[string][]TestKey
	// Uncovered lists, in input order, the mutants no test reaches.
	Uncovered []string
	// Tests lists every key in Profiles, in [compareTestKeys] order.
	Tests []TestKey
	// Matched is how many distinct module files the profiles named; see
	// [Result.Matched].
	Matched int
}

// CoveringOf reports the tests covering the mutant with id, or nil.
func (r TestResult) CoveringOf(id string) []TestKey { return r.Covering[id] }

// MapTests decides, for every mutant, which tests cover it.
//
// It is [Map] with a finer key and the same rule — lines only, a zero count is
// not coverage, an absent file is not coverage — so that the two answers
// agree wherever they are both asked: fold a mutant's covering tests back to
// their binaries and the list is what [Map] would give for the union of those
// tests' profiles. That agreement is pinned by the package's tests, because
// the point of the finer key is to run *less* against each mutant without
// ever measuring it against a test that could not have caught it or skipping
// one that could.
func MapTests(opts TestOptions) TestResult {
	tests := make([]TestKey, 0, len(opts.Profiles))
	for key := range opts.Profiles {
		tests = append(tests, key)
	}
	slices.SortFunc(tests, compareTestKeys)

	modules := modulesOf(opts.ModulePath, opts.Mutants)
	matched := make(map[string]bool)
	// A slice beside tests rather than a map keyed on one: the index of a test
	// is wanted once per file per test, and a two-string key hashed that many
	// times is a cost with no question behind it.
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

// A fileGroup is the mutants of one file, under the name a profile spells that
// file with, and where each of them sits in the caller's own list.
type fileGroup struct {
	path    string
	mutants []placedMutant
}

// A placedMutant is one mutant reduced to what the overlap search reads: its
// line interval, and the position its answer belongs in.
type placedMutant struct {
	at    int
	start int
	end   int
}

// groupByFile gathers the mutants by the file a profile would name them under,
// in the order the files first appear.
//
// It is what turns the mapping from a product into a sum of two of them. How a
// profile spells a mutant's file is a fact about the mutant, and finding that
// file in a profile is a fact about the file; asked inside the innermost loop
// they were both facts about a *pair*, which on a real run is the catalogue
// times the suite — four thousand mutants against six hundred tests is two and
// a half million strings built and thrown away, and as many lookups of a long
// path, to answer a question a few hundred files' worth already settles.
//
// The first-appearance order is what keeps the result a function of the input
// alone: iterating the map would order the groups differently from run to run,
// and although the answer does not depend on the order — each mutant's own
// covering list is appended in test order whichever group it is in — a
// deterministic walk is what makes that true by construction rather than by
// inspection.
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
