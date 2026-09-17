// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
)

const (
	module = "example.com/m"
	core   = module + "/internal/core"
	edge   = module + "/internal/edge"
)

const (
	coreFile     = "internal/core/core.go"
	edgeFile     = "internal/edge/edge.go"
	coreProfiled = module + "/" + coreFile
	edgeProfiled = module + "/" + edgeFile
)

func profile(blocks ...coverage.Block) coverage.Profile {
	return coverage.Profile{Mode: "set", Blocks: blocks}
}

func block(file string, startLine, endLine, count int) coverage.Block {
	return coverage.Block{
		File:      file,
		StartLine: startLine,
		StartCol:  1,
		EndLine:   endLine,
		EndCol:    1,
		NumStmt:   1,
		Count:     count,
	}
}

func mutant(id, path string, line int) coverage.Mutant {
	return coverage.Mutant{ID: id, Path: path, StartLine: line, EndLine: line}
}

func spans(id, path string, start, end int) coverage.Mutant {
	return coverage.Mutant{ID: id, Path: path, StartLine: start, EndLine: end}
}

func TestMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mutants  []coverage.Mutant
		profiles map[string]coverage.Profile
		want     map[string][]string
	}{
		{
			name:    "a covered line is covered by the binary that reached it",
			mutants: []coverage.Mutant{mutant("a", coreFile, 10)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 9, 11, 1)),
			},
			want: map[string][]string{"a": {core}},
		},
		{
			name:    "a block with a zero count covers nothing",
			mutants: []coverage.Mutant{mutant("a", coreFile, 10)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 9, 11, 0)),
			},
			want: nil,
		},
		{
			name:    "only the binary that reaches the line covers it",
			mutants: []coverage.Mutant{mutant("a", coreFile, 20)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 20, 20, 0)),
				edge: profile(block(coreProfiled, 20, 20, 1)),
			},
			want: map[string][]string{"a": {edge}},
		},
		{
			name:    "two binaries can both cover a mutant, and are reported sorted",
			mutants: []coverage.Mutant{mutant("a", coreFile, 20)},
			profiles: map[string]coverage.Profile{
				edge: profile(block(coreProfiled, 20, 20, 1)),
				core: profile(block(coreProfiled, 20, 20, 1)),
			},
			want: map[string][]string{"a": {core, edge}},
		},
		{
			name:    "a file absent from every profile is uncovered",
			mutants: []coverage.Mutant{mutant("a", edgeFile, 7)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 1, 50, 1)),
			},
			want: nil,
		},
		{
			name:    "a file absent from one profile is still covered by the other",
			mutants: []coverage.Mutant{mutant("a", edgeFile, 7)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 1, 50, 1)),
				edge: profile(block(edgeProfiled, 7, 7, 1)),
			},
			want: map[string][]string{"a": {edge}},
		},
		{
			name:    "the first line of a covered block is inside it",
			mutants: []coverage.Mutant{mutant("a", coreFile, 9)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 9, 11, 1)),
			},
			want: map[string][]string{"a": {core}},
		},
		{
			name:    "the last line of a covered block is inside it",
			mutants: []coverage.Mutant{mutant("a", coreFile, 11)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 9, 11, 1)),
			},
			want: map[string][]string{"a": {core}},
		},
		{
			name:    "the line before a covered block is outside it",
			mutants: []coverage.Mutant{mutant("a", coreFile, 8)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 9, 11, 1)),
			},
			want: nil,
		},
		{
			name:    "the line after a covered block is outside it",
			mutants: []coverage.Mutant{mutant("a", coreFile, 12)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 9, 11, 1)),
			},
			want: nil,
		},
		{
			name:    "a multi-line mutant overlapping a covered block at its start",
			mutants: []coverage.Mutant{spans("a", coreFile, 11, 20)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 5, 11, 1)),
			},
			want: map[string][]string{"a": {core}},
		},
		{
			name:    "a multi-line mutant overlapping a covered block at its end",
			mutants: []coverage.Mutant{spans("a", coreFile, 11, 20)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 20, 30, 1)),
			},
			want: map[string][]string{"a": {core}},
		},
		{
			name:    "a multi-line mutant straddling a covered block entirely",
			mutants: []coverage.Mutant{spans("a", coreFile, 1, 99)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 40, 42, 1)),
			},
			want: map[string][]string{"a": {core}},
		},
		{
			name:    "a multi-line mutant that misses every covered block",
			mutants: []coverage.Mutant{spans("a", coreFile, 11, 20)},
			profiles: map[string]coverage.Profile{
				core: profile(
					block(coreProfiled, 1, 10, 1),
					block(coreProfiled, 21, 30, 1),
					block(coreProfiled, 11, 20, 0),
				),
			},
			want: nil,
		},
		{
			name:    "a reversed span is covered by nothing",
			mutants: []coverage.Mutant{spans("a", coreFile, 10, 3)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 1, 20, 1)),
			},
			want: nil,
		},
		{
			name:    "covered blocks listed out of order still cover the line between them",
			mutants: []coverage.Mutant{mutant("a", coreFile, 3)},
			profiles: map[string]coverage.Profile{
				core: profile(
					block(coreProfiled, 10, 20, 1),
					block(coreProfiled, 1, 5, 1),
				),
			},
			want: map[string][]string{"a": {core}},
		},
		{
			name:    "a block nested inside another still covers the outer block's first line",
			mutants: []coverage.Mutant{mutant("a", coreFile, 1)},
			profiles: map[string]coverage.Profile{
				core: profile(
					block(coreProfiled, 1, 100, 1),
					block(coreProfiled, 5, 10, 1),
				),
			},
			want: map[string][]string{"a": {core}},
		},
		{
			name:    "two covered blocks that open on one line reach the further end",
			mutants: []coverage.Mutant{mutant("a", coreFile, 9)},
			profiles: map[string]coverage.Profile{
				core: profile(
					block(coreProfiled, 3, 5, 1),
					block(coreProfiled, 3, 9, 1),
				),
			},
			want: map[string][]string{"a": {core}},
		},
		{
			name: "two mutants on one line are decided together",
			mutants: []coverage.Mutant{
				mutant("a", coreFile, 10),
				mutant("b", coreFile, 10),
			},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 10, 10, 1)),
			},
			want: map[string][]string{"a": {core}, "b": {core}},
		},
		{
			name:    "a binary with an empty profile covers nothing",
			mutants: []coverage.Mutant{mutant("a", coreFile, 10)},
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 10, 10, 1)),
				edge: profile(),
			},
			want: map[string][]string{"a": {core}},
		},
		{
			name:    "a file outside the module is ignored",
			mutants: []coverage.Mutant{mutant("a", coreFile, 10)},
			profiles: map[string]coverage.Profile{
				core: profile(block("other.example/pkg/"+coreFile, 10, 10, 1)),
			},
			want: nil,
		},
		{
			name:    "a module whose path is a prefix of another is not confused with it",
			mutants: []coverage.Mutant{mutant("a", coreFile, 10)},
			profiles: map[string]coverage.Profile{
				core: profile(block("example.com/mine/"+coreFile, 10, 10, 1)),
			},
			want: nil,
		},
		{
			name:     "no profiles at all leaves every mutant uncovered",
			mutants:  []coverage.Mutant{mutant("a", coreFile, 10)},
			profiles: nil,
			want:     nil,
		},
		{
			name:    "no mutants is not a failure",
			mutants: nil,
			profiles: map[string]coverage.Profile{
				core: profile(block(coreProfiled, 10, 10, 1)),
			},
			want: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := coverage.Map(coverage.Options{
				ModulePath: module,
				Mutants:    test.mutants,
				Profiles:   test.profiles,
			})

			for _, m := range test.mutants {
				want, covered := test.want[m.ID]
				gotCovering := got.CoveringOf(m.ID)
				if !slices.Equal(gotCovering, want) {
					t.Errorf("mutant %s is covered by %v, want %v", m.ID, gotCovering, want)
				}
				if inUncovered := slices.Contains(got.Uncovered, m.ID); inUncovered == covered {
					t.Errorf("mutant %s: covered by %v but uncovered = %t", m.ID, gotCovering, inUncovered)
				}
			}
			if len(got.Uncovered) != len(test.mutants)-len(test.want) {
				t.Errorf("uncovered = %v, want %d of %d mutants",
					got.Uncovered, len(test.mutants)-len(test.want), len(test.mutants))
			}
		})
	}
}

func TestMapIsDeterministic(t *testing.T) {
	t.Parallel()

	profiles := map[string]coverage.Profile{}
	for _, name := range []string{"z", "y", "x", "w", "v", "u", "t", "s"} {
		profiles[module+"/internal/"+name] = profile(block(coreProfiled, 10, 10, 1))
	}
	opts := coverage.Options{
		ModulePath: module,
		Mutants:    []coverage.Mutant{mutant("a", coreFile, 10), mutant("b", edgeFile, 1)},
		Profiles:   profiles,
	}

	first := coverage.Map(opts)
	if !slices.IsSorted(first.Binaries) {
		t.Errorf("Binaries = %v, want them sorted", first.Binaries)
	}
	if !slices.IsSorted(first.CoveringOf("a")) {
		t.Errorf("the covering list %v is not sorted", first.CoveringOf("a"))
	}
	for i := range 50 {
		next := coverage.Map(opts)
		if !slices.Equal(next.Binaries, first.Binaries) {
			t.Fatalf("attempt %d: Binaries = %v, first = %v", i, next.Binaries, first.Binaries)
		}
		if !slices.Equal(next.CoveringOf("a"), first.CoveringOf("a")) {
			t.Fatalf("attempt %d: covering = %v, first = %v", i, next.CoveringOf("a"), first.CoveringOf("a"))
		}
		if !slices.Equal(next.Uncovered, first.Uncovered) {
			t.Fatalf("attempt %d: uncovered = %v, first = %v", i, next.Uncovered, first.Uncovered)
		}
	}
}

func TestMapKeepsTheCallersMutantOrder(t *testing.T) {
	t.Parallel()

	got := coverage.Map(coverage.Options{
		ModulePath: module,
		Mutants: []coverage.Mutant{
			mutant("third", coreFile, 3),
			mutant("first", coreFile, 1),
			mutant("second", coreFile, 2),
		},
	})
	if want := []string{"third", "first", "second"}; !slices.Equal(got.Uncovered, want) {
		t.Errorf("uncovered = %v, want %v", got.Uncovered, want)
	}
}

func TestMapReportsWhetherAnythingLinedUp(t *testing.T) {
	t.Parallel()

	profiles := map[string]coverage.Profile{
		core: profile(block(coreProfiled, 10, 10, 1), block(edgeProfiled, 4, 4, 0)),
	}
	lined := coverage.Map(coverage.Options{
		ModulePath: module,
		Mutants:    []coverage.Mutant{mutant("a", coreFile, 10)},
		Profiles:   profiles,
	})
	if lined.Matched != 2 {
		t.Errorf("Matched = %d, want both files of the profile", lined.Matched)
	}

	mismatched := coverage.Map(coverage.Options{
		ModulePath: "example.com/other",
		Mutants:    []coverage.Mutant{mutant("a", coreFile, 10)},
		Profiles:   profiles,
	})
	if mismatched.Matched != 0 {
		t.Errorf("Matched = %d for a module path nothing is under, want 0", mismatched.Matched)
	}
	if len(mismatched.Uncovered) != 1 {
		t.Errorf("uncovered = %v, want the one mutant: this is the shape the engine has to distrust",
			mismatched.Uncovered)
	}
}

func TestMapWithNoModulePathTakesProfilesAtFaceValue(t *testing.T) {
	t.Parallel()

	got := coverage.Map(coverage.Options{
		Mutants:  []coverage.Mutant{mutant("a", coreFile, 10)},
		Profiles: map[string]coverage.Profile{core: profile(block(coreFile, 10, 10, 1))},
	})
	if want := []string{core}; !slices.Equal(got.CoveringOf("a"), want) {
		t.Errorf("covering = %v, want %v", got.CoveringOf("a"), want)
	}
}

func TestEndLineCountsTheNewlinesInTheOriginal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		start    int
		original string
		want     int
	}{
		{name: "an operator", start: 12, original: "!=", want: 12},
		{name: "an empty replacement's original", start: 12, original: "", want: 12},
		{name: "a two-line statement", start: 12, original: "foo(\n\tbar)", want: 13},
		{name: "a trailing newline still opens a line", start: 12, original: "foo()\n", want: 13},
		{name: "a whole block", start: 3, original: "a\nb\nc\nd", want: 6},
		{
			name: "windows line endings", start: 12, original: "foo(\r\n\tbar)", want: 13,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := coverage.EndLine(test.start, test.original); got != test.want {
				t.Errorf("EndLine(%d, %q) = %d, want %d", test.start, test.original, got, test.want)
			}
		})
	}
}

func TestCodesAreUniqueAndInBlock(t *testing.T) {
	t.Parallel()

	seen := make(map[coverage.Code]bool, len(coverage.Codes()))
	for _, code := range coverage.Codes() {
		if seen[code] {
			t.Errorf("duplicate code %q", code)
		}
		seen[code] = true
		if !strings.HasPrefix(string(code), "GOM76") || len(code) != len("GOM0000") {
			t.Errorf("%q is outside the GOM76xx block this package owns", code)
		}
	}
	if !slices.IsSortedFunc(coverage.Codes(), func(a, b coverage.Code) int {
		return strings.Compare(string(a), string(b))
	}) {
		t.Errorf("Codes() is not in numeric order: %v", coverage.Codes())
	}
	for _, code := range []coverage.Code{
		coverage.CodeMalformedProfile,
		coverage.CodeCustomTestCommand,
		coverage.CodeUnavailable,
	} {
		if !seen[code] {
			t.Errorf("Codes() does not list %q", code)
		}
	}
}

func TestCodeStringIsTheCodeItself(t *testing.T) {
	t.Parallel()

	for _, code := range coverage.Codes() {
		if got := code.String(); got != string(code) {
			t.Errorf("%q.String() = %q, want the code itself", string(code), got)
		}
	}
	if got := coverage.CodeMalformedProfile.String(); got != "GOM7600" {
		t.Errorf("CodeMalformedProfile.String() = %q, want %q", got, "GOM7600")
	}
}

func TestErrorRendersItsCause(t *testing.T) {
	t.Parallel()

	withCause := &coverage.Error{
		Code:    coverage.CodeMalformedProfile,
		Message: "the coverage profile could not be read",
		Err:     errUnrelated,
	}
	want := "GOM7600: the coverage profile could not be read: " + errUnrelated.Error()
	if got := withCause.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	alone := &coverage.Error{
		Code:    coverage.CodeMalformedProfile,
		Message: "the coverage profile could not be read",
	}
	if got, wantAlone := alone.Error(), "GOM7600: the coverage profile could not be read"; got != wantAlone {
		t.Errorf("Error() without a cause = %q, want %q", got, wantAlone)
	}
}

func TestCodeOfForeignError(t *testing.T) {
	t.Parallel()

	if got := coverage.CodeOf(nil); got != "" {
		t.Errorf("CodeOf(nil) = %q, want the empty code", got)
	}
	if got := coverage.CodeOf(errUnrelated); got != "" {
		t.Errorf("CodeOf(foreign) = %q, want the empty code", got)
	}
	wrapped := &coverage.Error{
		Code:    coverage.CodeUnavailable,
		Message: "the pass produced nothing",
		Err:     errUnrelated,
	}
	if got := coverage.CodeOf(wrapped); got != coverage.CodeUnavailable {
		t.Errorf("CodeOf = %q, want %q", got, coverage.CodeUnavailable)
	}
	if !strings.HasPrefix(wrapped.Error(), string(coverage.CodeUnavailable)+": ") {
		t.Errorf("rendered error does not start with its code: %q", wrapped.Error())
	}
	if wrapped.Unwrap() != errUnrelated {
		t.Errorf("Unwrap = %v, want the cause", wrapped.Unwrap())
	}
}

func TestMapTestsNamesTheTestsThatReachEachMutant(t *testing.T) {
	t.Parallel()

	const module = "example.com/m"
	mutants := []coverage.Mutant{
		{ID: "a", Path: "core/a.go", StartLine: 10, EndLine: 10},
		{ID: "b", Path: "core/a.go", StartLine: 20, EndLine: 22},
		{ID: "c", Path: "user/u.go", StartLine: 5, EndLine: 5},
		{ID: "d", Path: "core/b.go", StartLine: 1, EndLine: 1},
	}
	profiles := map[coverage.TestKey]coverage.Profile{
		{ImportPath: module + "/core", Name: "TestOne"}: profile(
			coverage.Block{File: module + "/core/a.go", StartLine: 9, EndLine: 11, NumStmt: 1, Count: 1},
			coverage.Block{File: module + "/core/a.go", StartLine: 20, EndLine: 22, NumStmt: 1, Count: 0},
		),
		{ImportPath: module + "/core", Name: "TestTwo"}: profile(
			coverage.Block{File: module + "/core/a.go", StartLine: 21, EndLine: 21, NumStmt: 1, Count: 3},
		),
		{ImportPath: module + "/user", Name: "TestUser"}: profile(
			coverage.Block{File: module + "/core/a.go", StartLine: 10, EndLine: 10, NumStmt: 1, Count: 1},
			coverage.Block{File: module + "/user/u.go", StartLine: 5, EndLine: 6, NumStmt: 1, Count: 1},
		),
	}

	got := coverage.MapTests(coverage.TestOptions{ModulePath: module, Mutants: mutants, Profiles: profiles})

	want := map[string][]coverage.TestKey{
		"a": {{ImportPath: module + "/core", Name: "TestOne"}, {ImportPath: module + "/user", Name: "TestUser"}},
		"b": {{ImportPath: module + "/core", Name: "TestTwo"}},
		"c": {{ImportPath: module + "/user", Name: "TestUser"}},
	}
	for id, tests := range want {
		if !slices.Equal(got.Covering[id], tests) {
			t.Errorf("Covering[%s] = %v, want %v", id, got.Covering[id], tests)
		}
		if !slices.Equal(got.CoveringOf(id), tests) {
			t.Errorf("CoveringOf(%s) = %v, want %v, the same answer as the map", id, got.CoveringOf(id), tests)
		}
	}
	if _, ok := got.Covering["d"]; ok {
		t.Errorf("Covering[d] = %v, want absent: no test's profile holds core/b.go", got.Covering["d"])
	}
	if !slices.Equal(got.Uncovered, []string{"d"}) {
		t.Errorf("Uncovered = %v, want [d]", got.Uncovered)
	}
	wantTests := []coverage.TestKey{
		{ImportPath: module + "/core", Name: "TestOne"},
		{ImportPath: module + "/core", Name: "TestTwo"},
		{ImportPath: module + "/user", Name: "TestUser"},
	}
	if !slices.Equal(got.Tests, wantTests) {
		t.Errorf("Tests = %v, want %v, sorted by import path then name", got.Tests, wantTests)
	}
	if got.Matched != 2 {
		t.Errorf("Matched = %d, want 2: core/a.go and user/u.go are the files the profiles name; core/b.go is named by none, which is why d is uncovered",
			got.Matched)
	}
}

func TestMapTestsAgreesWithMapOnBinaries(t *testing.T) {
	t.Parallel()

	const module = "example.com/m"
	mutants := []coverage.Mutant{
		{ID: "a", Path: "p/a.go", StartLine: 3, EndLine: 3},
		{ID: "b", Path: "p/a.go", StartLine: 8, EndLine: 8},
		{ID: "c", Path: "p/a.go", StartLine: 30, EndLine: 30},
	}
	one := profile(coverage.Block{File: module + "/p/a.go", StartLine: 1, EndLine: 4, NumStmt: 1, Count: 1})
	two := profile(coverage.Block{File: module + "/p/a.go", StartLine: 7, EndLine: 9, NumStmt: 1, Count: 2})
	perTest := map[coverage.TestKey]coverage.Profile{
		{ImportPath: module + "/p", Name: "TestOne"}: one,
		{ImportPath: module + "/p", Name: "TestTwo"}: two,
	}
	union := profile(append(slices.Clone(one.Blocks), two.Blocks...)...)

	byTest := coverage.MapTests(coverage.TestOptions{ModulePath: module, Mutants: mutants, Profiles: perTest})
	byBinary := coverage.Map(coverage.Options{ModulePath: module, Mutants: mutants,
		Profiles: map[string]coverage.Profile{module + "/p": union}})

	for _, m := range mutants {
		var folded []string
		for _, key := range byTest.Covering[m.ID] {
			if len(folded) == 0 || folded[len(folded)-1] != key.ImportPath {
				folded = append(folded, key.ImportPath)
			}
		}
		if !slices.Equal(folded, byBinary.Covering[m.ID]) {
			t.Errorf("mutant %s: tests fold to binaries %v, Map says %v", m.ID, folded, byBinary.Covering[m.ID])
		}
	}
	if !slices.Equal(byTest.Uncovered, byBinary.Uncovered) {
		t.Errorf("Uncovered by tests = %v, by binaries = %v", byTest.Uncovered, byBinary.Uncovered)
	}
}
