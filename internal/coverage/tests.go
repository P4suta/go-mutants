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

	matched := make(map[string]bool)
	indexes := make(map[TestKey]fileIndex, len(tests))
	for _, key := range tests {
		indexes[key] = newFileIndex(opts.Profiles[key], opts.ModulePath, matched)
	}

	result := TestResult{
		Covering:  make(map[string][]TestKey, len(opts.Mutants)),
		Uncovered: make([]string, 0),
		Tests:     tests,
		Matched:   len(matched),
	}
	for _, m := range opts.Mutants {
		var covering []TestKey
		for _, key := range tests {
			if indexes[key].covers(m.Path, m.StartLine, m.EndLine) {
				covering = append(covering, key)
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
