// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// The three invariants of the mapping that [Map] cannot be asked about.
//
// Everything else this package promises is a question about Map's answer, and
// map_test.go asks it from outside the package, which is where a test of a pure
// function belongs. These three are not: the index records a file it never
// reached and nothing downstream reads that record back, merge joins two
// adjacent ranges into one and covers answers a query about the joined range
// exactly as it answers one about the pair, and relativeTo refuses a name that
// is not a path. Each is a documented promise of the code below, each is what
// makes the structure above it cheap or honest, and none of them changes an
// answer Map gives — so a test that only went through Map would pin none of
// them, and a mutation run says so by leaving the lines alive.
package coverage

import (
	"slices"
	"testing"
)

// The two spellings the mapping has to keep apart, as in map_test.go: a profile
// names a file by import path, and a mutant is located by a module-relative
// path.
const (
	indexModule   = "example.com/m"
	indexFile     = "internal/core/core.go"
	indexProfiled = indexModule + "/" + indexFile
)

// TestNewFileIndexRecordsAFileItNeverReached pins the difference between
// "profiled and never reached" and "never profiled".
//
// A file whose every block has a count of zero is indexed to an empty list —
// present, and covering nothing — rather than left out, and it is recorded in
// matched. Both facts are invisible through [Map], because a present-and-empty
// entry and a missing one make covers answer false alike and matched is only
// ever counted. They are the two halves of the fact [Result.Matched] exists to
// report: a run whose profiles line up with the module and reach nothing is a
// suite with no coverage, and a run whose profiles line up with nothing is a
// module path that does not match what the toolchain wrote. Only the second is
// a reason to distrust the mapping, and only this record tells them apart.
func TestNewFileIndexRecordsAFileItNeverReached(t *testing.T) {
	t.Parallel()

	matched := make(map[string]bool)
	index := newFileIndex(Profile{
		Mode: "set",
		Blocks: []Block{
			{File: indexProfiled, StartLine: 3, StartCol: 1, EndLine: 5, EndCol: 1, NumStmt: 1, Count: 0},
			{File: indexProfiled, StartLine: 8, StartCol: 1, EndLine: 9, EndCol: 1, NumStmt: 1, Count: 0},
		},
	}, indexModule, matched)

	if !matched[indexFile] {
		t.Errorf("matched = %v, want the profiled file recorded as %q", matched, indexFile)
	}
	intervals, present := index[indexFile]
	if !present {
		t.Fatalf("index = %v, want an entry for the file the profile named", index)
	}
	if len(intervals) != 0 {
		t.Errorf("index[%q] = %v, want no covered interval", indexFile, intervals)
	}
	if index.covers(indexFile, 4, 4) {
		t.Errorf("covers(%q, 4, 4) = true for a file whose every block has a count of zero", indexFile)
	}
}

// TestNewFileIndexKeepsACoveredFileApartFromAnUnreachedOne is the other half:
// a file with one covered block indexes to that block, and a name from outside
// the module is recorded nowhere at all.
func TestNewFileIndexKeepsACoveredFileApartFromAnUnreachedOne(t *testing.T) {
	t.Parallel()

	matched := make(map[string]bool)
	index := newFileIndex(Profile{
		Mode: "set",
		Blocks: []Block{
			{File: indexProfiled, StartLine: 3, StartCol: 1, EndLine: 5, EndCol: 1, NumStmt: 1, Count: 1},
			{File: "other.example/pkg/a.go", StartLine: 1, StartCol: 1, EndLine: 9, EndCol: 1, NumStmt: 1, Count: 1},
		},
	}, indexModule, matched)

	if len(matched) != 1 || !matched[indexFile] {
		t.Errorf("matched = %v, want only the file inside the module", matched)
	}
	if got, want := index[indexFile], []interval{{start: 3, end: 5}}; !slices.Equal(got, want) {
		t.Errorf("index[%q] = %v, want %v", indexFile, got, want)
	}
}

// TestMergeJoinsWhatCoversAnswersIdenticallyAbout pins merge's output rather
// than the answers it leads to, because the joining is invisible from outside.
//
// Two adjacent ranges and one joined range answer every overlap query the same
// way — that is exactly the argument merge's own comment makes for joining them
// — so [Map] cannot tell the two apart and neither can any test written through
// it. What the joining buys is a shorter list to binary-search on every lookup,
// and this is the test that says the list is actually shorter.
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
			// The case merge exists for and the one covers cannot be asked
			// about: [3,5] and [6,9] leave no line between them, so the pair
			// and the join answer every query alike.
			name: "adjacent intervals are joined",
			in:   []interval{{start: 3, end: 5}, {start: 6, end: 9}},
			want: []interval{{start: 3, end: 9}},
		},
		{
			// One line apart is the boundary on the other side: line 6 is
			// covered by nothing, so the two ranges stay two.
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
			// Two blocks of one file can open on the same line — a one-line
			// `if` and the statement it guards do. They overlap by definition,
			// so the join keeps the further end whichever of them the sort put
			// first; the secondary sort key is there to make the sort a
			// function rather than to change this answer.
			name: "intervals that start on one line join into the further one",
			in:   []interval{{start: 3, end: 9}, {start: 3, end: 5}},
			want: []interval{{start: 3, end: 9}},
		},
		{
			// The blocks arrive in whatever order the profile listed them, and
			// the binary search below needs them sorted and disjoint.
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

// TestRelativeTo pins the one place a profile's spelling is turned into a
// mutant's.
//
// The empty module path is the hand-written fixture case, where a profile
// already spells its files module-relatively — and the empty *file* is not a
// path in either spelling, so it is refused rather than passed through as one.
func TestRelativeTo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		modulePath string
		file       string
		want       string
		wantOK     bool
	}{
		{
			name:       "a file inside the module",
			modulePath: indexModule,
			file:       indexProfiled,
			want:       indexFile,
			wantOK:     true,
		},
		{
			name:       "a file outside the module",
			modulePath: indexModule,
			file:       "other.example/pkg/a.go",
			wantOK:     false,
		},
		{
			// The prefix has to be a whole path element: "example.com/mine" is
			// not inside "example.com/m".
			name:       "a module whose path is a prefix of another",
			modulePath: indexModule,
			file:       "example.com/mine/a.go",
			wantOK:     false,
		},
		{
			name:       "the module path with nothing after it",
			modulePath: indexModule,
			file:       indexModule + "/",
			wantOK:     false,
		},
		{
			name:       "no module path takes the name at face value",
			modulePath: "",
			file:       indexFile,
			want:       indexFile,
			wantOK:     true,
		},
		{
			name:       "no module path still refuses an unnamed file",
			modulePath: "",
			file:       "",
			wantOK:     false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, ok := relativeTo(test.modulePath, test.file)
			if ok != test.wantOK {
				t.Fatalf("relativeTo(%q, %q) ok = %t, want %t", test.modulePath, test.file, ok, test.wantOK)
			}
			if ok && got != test.want {
				t.Errorf("relativeTo(%q, %q) = %q, want %q", test.modulePath, test.file, got, test.want)
			}
		})
	}
}

// TestCompareTestKeysOrdersByImportPathThenName pins the one order every list
// of keys uses, field by field, because the callers sort sets that come out of
// a map: a comparator that answered 0 too often would leave those lists in
// iteration order, which is a different order on every run and a flake in
// every test that reads one.
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
