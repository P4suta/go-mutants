// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package coverage_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/coverage"
)

// The module and the two test binaries every case below is written against.
const (
	module = "example.com/m"
	core   = module + "/internal/core"
	edge   = module + "/internal/edge"
)

// coreFile and edgeFile are the two sources, in the two spellings that have to
// be kept apart: a profile names a file by import path, and a mutant is located
// by a module-relative path.
const (
	coreFile     = "internal/core/core.go"
	edgeFile     = "internal/edge/edge.go"
	coreProfiled = module + "/" + coreFile
	edgeProfiled = module + "/" + edgeFile
)

// profile builds a `set`-mode profile out of "<file> <startLine>-<endLine>
// <count>" triples, so a case can be read as a table rather than as a struct
// literal.
func profile(blocks ...coverage.Block) coverage.Profile {
	return coverage.Profile{Mode: "set", Blocks: blocks}
}

// block is one covered or uncovered line range. The columns are filled in with
// values the mapping must ignore: 1 at the start of every block and 1 at the
// end, which is a *narrower* range than any real block and would exclude every
// mutant if columns were ever consulted.
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

// mutant is one located mutant on a single line.
func mutant(id, path string, line int) coverage.Mutant {
	return coverage.Mutant{ID: id, Path: path, StartLine: line, EndLine: line}
}

// spans is one located mutant over several lines.
func spans(id, path string, start, end int) coverage.Mutant {
	return coverage.Mutant{ID: id, Path: path, StartLine: start, EndLine: end}
}

func TestMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mutants  []coverage.Mutant
		profiles map[string]coverage.Profile
		// want is the covering binaries per mutant id; an id absent from it is
		// expected to be uncovered.
		want map[string][]string
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
			// The whole point of per-binary profiles. `edge`'s tests call into
			// `core`; `core`'s own tests do not reach this line.
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
			// A file no profile names is a package no test binary linked, which
			// is the ordinary way a mutant ends up uncovered.
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
			// A multi-line statement deletion. Overlap, not containment: one
			// reached line inside the span is enough, because the mutant
			// changes the whole span and any of it running can change what a
			// test sees.
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
			// Two mutants on one line share a verdict. That is the
			// over-approximation the line-only rule buys, stated as a test so
			// that nobody later "fixes" it with columns.
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
			// A file from outside the module cannot hold a mutant, and a name
			// that does not resolve must not be matched by accident.
			name:    "a file outside the module is ignored",
			mutants: []coverage.Mutant{mutant("a", coreFile, 10)},
			profiles: map[string]coverage.Profile{
				core: profile(block("other.example/pkg/"+coreFile, 10, 10, 1)),
			},
			want: nil,
		},
		{
			// The prefix has to be a whole path element. "example.com/mine" is
			// not inside "example.com/m".
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

// TestMapIsDeterministic proves the output does not depend on map iteration
// order, which is the one way a pure function over a map can stop being one.
//
// Go randomizes map iteration per range statement rather than per process, so
// repeating the call inside one test is what exercises it.
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

// TestMapKeepsTheCallersMutantOrder pins the one ordering the mapping does not
// impose itself, because a renderer listing uncovered mutants should list them
// in the order the run scheduled them.
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

// TestMapReportsWhetherAnythingLinedUp is the sanity check the engine fails open
// on.
//
// A module path that does not match what the toolchain wrote produces a
// perfectly well formed answer in which every mutant is uncovered, and a run
// that believed it would report a workspace as having no test coverage at all.
// Matched is what makes that visible without the caller having to reason about
// it.
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
	// Both files are counted, the uncovered one included: "profiled and never
	// reached" is a fact that lined up, and it is the fact that makes a mutant
	// honestly uncovered.
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

// TestMapWithNoModulePathTakesProfilesAtFaceValue covers the hand-written
// fixture case, where a profile already spells its files module-relatively.
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

// TestEndLineCountsTheNewlinesInTheOriginal pins the derivation the engine uses
// to turn a start line and the mutant's own bytes into a line interval.
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
			// CRLF, because the snapshot preserves whatever the file had and a
			// Windows checkout has them. Counting '\n' is right for both.
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
