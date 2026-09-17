// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage

import (
	"slices"
	"testing"
)

const (
	indexModule   = "example.com/m"
	indexFile     = "internal/core/core.go"
	indexProfiled = indexModule + "/" + indexFile
)

func TestNewFileIndexRecordsAFileItNeverReached(t *testing.T) {
	t.Parallel()

	matched := make(map[string]bool)
	index := newFileIndex(Profile{
		Mode: "set",
		Blocks: []Block{
			{File: indexProfiled, StartLine: 3, StartCol: 1, EndLine: 5, EndCol: 1, NumStmt: 1, Count: 0},
			{File: indexProfiled, StartLine: 8, StartCol: 1, EndLine: 9, EndCol: 1, NumStmt: 1, Count: 0},
		},
	}, []string{indexModule}, matched)

	if !matched[indexProfiled] {
		t.Errorf("matched = %v, want the profiled file recorded as %q", matched, indexProfiled)
	}
	intervals, present := index[indexProfiled]
	if !present {
		t.Fatalf("index = %v, want an entry for the file the profile named", index)
	}
	if len(intervals) != 0 {
		t.Errorf("index[%q] = %v, want no covered interval", indexProfiled, intervals)
	}
	if index.covers(indexProfiled, 4, 4) {
		t.Errorf("covers(%q, 4, 4) = true for a file whose every block has a count of zero", indexProfiled)
	}
}

func TestNewFileIndexKeepsACoveredFileApartFromAnUnreachedOne(t *testing.T) {
	t.Parallel()

	matched := make(map[string]bool)
	index := newFileIndex(Profile{
		Mode: "set",
		Blocks: []Block{
			{File: indexProfiled, StartLine: 3, StartCol: 1, EndLine: 5, EndCol: 1, NumStmt: 1, Count: 1},
			{File: "other.example/pkg/a.go", StartLine: 1, StartCol: 1, EndLine: 9, EndCol: 1, NumStmt: 1, Count: 1},
		},
	}, []string{indexModule}, matched)

	if len(matched) != 1 || !matched[indexProfiled] {
		t.Errorf("matched = %v, want only the file inside the module", matched)
	}
	if got, want := index[indexProfiled], []interval{{start: 3, end: 5}}; !slices.Equal(got, want) {
		t.Errorf("index[%q] = %v, want %v", indexProfiled, got, want)
	}
}

func TestMergeJoinsWhatCoversAnswersIdenticallyAbout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []interval
		want []interval
	}{
		{
			name: "nothing",
			in:   nil,
			want: nil,
		},
		{
			name: "one interval is already merged",
			in:   []interval{{start: 3, end: 5}},
			want: []interval{{start: 3, end: 5}},
		},
		{
			name: "adjacent intervals are joined",
			in:   []interval{{start: 3, end: 5}, {start: 6, end: 9}},
			want: []interval{{start: 3, end: 9}},
		},
		{
			name: "intervals with a line between them are left apart",
			in:   []interval{{start: 3, end: 5}, {start: 7, end: 9}},
			want: []interval{{start: 3, end: 5}, {start: 7, end: 9}},
		},
		{
			name: "overlapping intervals are joined",
			in:   []interval{{start: 3, end: 7}, {start: 5, end: 9}},
			want: []interval{{start: 3, end: 9}},
		},
		{
			name: "touching intervals are joined",
			in:   []interval{{start: 3, end: 5}, {start: 5, end: 9}},
			want: []interval{{start: 3, end: 9}},
		},
		{
			name: "a nested interval does not shorten the one holding it",
			in:   []interval{{start: 3, end: 20}, {start: 5, end: 9}},
			want: []interval{{start: 3, end: 20}},
		},
		{
			name: "intervals that start on one line join into the further one",
			in:   []interval{{start: 3, end: 9}, {start: 3, end: 5}},
			want: []interval{{start: 3, end: 9}},
		},
		{
			name: "unsorted intervals are sorted before they are joined",
			in:   []interval{{start: 20, end: 25}, {start: 3, end: 5}, {start: 6, end: 9}},
			want: []interval{{start: 3, end: 9}, {start: 20, end: 25}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := merge(slices.Clone(test.in)); !slices.Equal(got, test.want) {
				t.Errorf("merge(%v) = %v, want %v", test.in, got, test.want)
			}
		})
	}
}

func TestProfilePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		runModule string
		mutant    Mutant
		want      string
	}{
		{
			name:      "the run's module",
			runModule: indexModule,
			mutant:    Mutant{Path: indexFile},
			want:      indexProfiled,
		},
		{
			name:      "a mutant of a workspace names its own",
			runModule: "",
			mutant:    Mutant{Path: indexFile, ModulePath: indexModule},
			want:      indexProfiled,
		},
		{
			name:      "a mutant's own module wins over the run's",
			runModule: "other.example/m",
			mutant:    Mutant{Path: indexFile, ModulePath: indexModule},
			want:      indexProfiled,
		},
		{
			name:   "neither, and the path stands",
			mutant: Mutant{Path: indexFile},
			want:   indexFile,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := profilePath(test.runModule, test.mutant); got != test.want {
				t.Errorf("profilePath(%q, %+v) = %q, want %q",
					test.runModule, test.mutant, got, test.want)
			}
		})
	}
}

func TestCompareTestKeysOrdersByImportPathThenName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b TestKey
		want int
	}{
		{
			name: "the import path decides before the name is looked at",
			a:    TestKey{ImportPath: "example.com/m/a", Name: "TestZ"},
			b:    TestKey{ImportPath: "example.com/m/b", Name: "TestA"},
			want: -1,
		},
		{
			name: "the name decides within one binary",
			a:    TestKey{ImportPath: "example.com/m/a", Name: "TestA"},
			b:    TestKey{ImportPath: "example.com/m/a", Name: "TestB"},
			want: -1,
		},
		{
			name: "equal keys compare equal",
			a:    TestKey{ImportPath: "example.com/m/a", Name: "TestA"},
			b:    TestKey{ImportPath: "example.com/m/a", Name: "TestA"},
			want: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := compareTestKeys(test.a, test.b); got != test.want {
				t.Errorf("compareTestKeys(%v, %v) = %d, want %d", test.a, test.b, got, test.want)
			}
			if got := compareTestKeys(test.b, test.a); got != -test.want {
				t.Errorf("compareTestKeys(%v, %v) = %d, want %d: the order is not antisymmetric", test.b, test.a, got, -test.want)
			}
		})
	}
}

func TestModulesOfIsEveryModuleTheMutantsAreIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		runModule string
		mutants   []Mutant
		want      []string
	}{
		{
			name:      "a run over one module names it once",
			runModule: indexModule,
			mutants:   []Mutant{{Path: indexFile}, {Path: "other.go"}},
			want:      []string{indexModule},
		},
		{
			name: "a workspace names each module once, in the order it meets them",
			mutants: []Mutant{
				{Path: indexFile, ModulePath: "example.com/b"},
				{Path: indexFile, ModulePath: "example.com/a"},
				{Path: "other.go", ModulePath: "example.com/b"},
			},
			want: []string{"example.com/b", "example.com/a"},
		},
		{
			name:      "the run's module and the mutants' together",
			runModule: indexModule,
			mutants:   []Mutant{{Path: indexFile, ModulePath: "example.com/a"}},
			want:      []string{indexModule, "example.com/a"},
		},
		{
			name:    "neither",
			mutants: []Mutant{{Path: indexFile}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := modulesOf(test.runModule, test.mutants); !slices.Equal(got, test.want) {
				t.Errorf("modulesOf(%q, %+v) = %v, want %v",
					test.runModule, test.mutants, got, test.want)
			}
		})
	}
}

func TestUnderModuleIsTheRuleMatchedCounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		file    string
		modules []string
		want    bool
	}{
		{name: "inside the one module", file: indexProfiled, modules: []string{indexModule}, want: true},
		{name: "inside the second of two", file: indexProfiled,
			modules: []string{"other.example/m", indexModule}, want: true},
		{name: "outside every module", file: "other.example/pkg/a.go",
			modules: []string{indexModule}},
		{
			name: "a module whose name is a prefix of another's",
			file: indexModule + "m/a.go", modules: []string{indexModule},
		},
		{name: "no modules at all", file: "anything.go", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := underModule(test.file, test.modules); got != test.want {
				t.Errorf("underModule(%q, %v) = %t, want %t",
					test.file, test.modules, got, test.want)
			}
		})
	}
}
