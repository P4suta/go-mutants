// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/mutation"
)

// TestIsolateFindsTheCandidatesThatDoNotCompile drives the search against a
// compiler that is a table.
//
// The seam is the whole reason this can be a table. A probe is "does this
// subset compile", and against a fake one the algorithm's every branch —
// halving, the scan below the threshold, the verified join, the fallback when
// the join fails — is reachable in microseconds, where against a toolchain each
// row would be tens of builds. What the fake cannot say anything about is
// whether a real compiler agrees; that is the integration test's job, and the
// two are complementary rather than redundant.
func TestIsolateFindsTheCandidatesThatDoNotCompile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// size is how many candidates the file holds.
		size int
		// bad are the candidate positions that fail on their own.
		bad []int
		// pair is a couple that compiles apart and fails together, or nil.
		pair []int
		// want is the positions the search must accept, in order.
		want []int
		// wantRejected is the positions it must reject.
		wantRejected []int
	}{
		{
			name: "none bad",
			size: 10,
			want: seq(0, 10),
		},
		{
			name:         "one bad of ten",
			size:         10,
			bad:          []int{4},
			want:         []int{0, 1, 2, 3, 5, 6, 7, 8, 9},
			wantRejected: []int{4},
		},
		{
			name:         "the first and the last of ten",
			size:         10,
			bad:          []int{0, 9},
			want:         []int{1, 2, 3, 4, 5, 6, 7, 8},
			wantRejected: []int{0, 9},
		},
		{
			// Both in one half, so the halving search has to keep splitting
			// after it has already found one.
			name:         "two bad in one half",
			size:         10,
			bad:          []int{5, 7},
			want:         []int{0, 1, 2, 3, 4, 6, 8, 9},
			wantRejected: []int{5, 7},
		},
		{
			name:         "all bad",
			size:         6,
			bad:          seq(0, 6),
			want:         nil,
			wantRejected: seq(0, 6),
		},
		{
			// Below the threshold the search never halves; this is the scan.
			name:         "one bad of three",
			size:         3,
			bad:          []int{1},
			want:         []int{0, 2},
			wantRejected: []int{1},
		},
		{
			// Neither fails alone, so halving accepts both halves and hands
			// back a set that does not build. The verified join catches it and
			// the scan settles it: the second of the two loses, which is
			// arbitrary and identical on every machine.
			name:         "an interacting pair across the split",
			size:         10,
			pair:         []int{2, 8},
			want:         []int{0, 1, 2, 3, 4, 5, 6, 7, 9},
			wantRejected: []int{8},
		},
		{
			// The same interaction inside one half, where the scan below the
			// threshold meets it instead of the join.
			name:         "an interacting pair inside a half",
			size:         8,
			pair:         []int{0, 2},
			want:         []int{0, 1, 3, 4, 5, 6, 7},
			wantRejected: []int{2},
		},
		{
			name: "nothing to search",
			size: 0,
			want: nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			cands := fakeMutants("sample.go", c.size)
			compiler := &fakeCompiler{cands: cands, bad: c.bad, pair: c.pair}

			accepted, rejected, err := isolate(t.Context(), cands, compiler.probe)
			if err != nil {
				t.Fatalf("isolate: %v", err)
			}
			if got := positionsOf(accepted); !slices.Equal(got, c.want) {
				t.Errorf("accepted %v, want %v", got, c.want)
			}
			if got := positionsOf(mutantsOf(rejected)); !slices.Equal(got, c.wantRejected) {
				t.Errorf("rejected %v, want %v", got, c.wantRejected)
			}
			// Every rejection carries the output of the build that condemned
			// it, which is what a user is shown and what the phase cannot
			// re-derive later: by the time it finishes, the tree compiles.
			for _, r := range rejected {
				if !strings.Contains(r.output, r.mutant.Path) {
					t.Errorf("the rejection of %s carries %q, which does not name its file",
						r.mutant.DisplayID, r.output)
				}
			}
			// Whatever came back has to be a set the probe agrees compiles.
			if v, err := compiler.probe(t.Context(), accepted); err != nil || v.failed {
				t.Errorf("the accepted set does not compile: %+v %v", v, err)
			}
		})
	}
}

// TestIsolateHalvesRatherThanScans pins the reason the halving exists at all.
//
// A search that scanned would produce exactly the same answer, so no assertion
// about accepted and rejected sets can tell the two apart. The count can: one
// bad candidate among sixteen costs a scan sixteen probes and costs this search
// far fewer, and a refactor that quietly turned the halving off would show up
// here and nowhere else.
func TestIsolateHalvesRatherThanScans(t *testing.T) {
	t.Parallel()

	const size = 16
	cands := fakeMutants("sample.go", size)
	compiler := &fakeCompiler{cands: cands, bad: []int{11}}

	accepted, rejected, err := isolate(t.Context(), cands, compiler.probe)
	if err != nil {
		t.Fatalf("isolate: %v", err)
	}
	if len(accepted) != size-1 || len(rejected) != 1 {
		t.Fatalf("accepted %d and rejected %d, want %d and 1", len(accepted), len(rejected), size-1)
	}
	if compiler.probes >= size {
		t.Errorf("isolate spent %d probes on %d candidates, which is no better than scanning them",
			compiler.probes, size)
	}
}

// TestIsolateStopsAtTheFirstProbeError proves an infrastructure failure is not
// mistaken for a compile failure.
//
// The distinction is the difference between "this mutant cannot be compiled"
// and "this machine cannot compile". A probe that returns an error means the
// build could not be run at all, and treating that as a red build would reject
// every candidate in the file for a reason that has nothing to do with any of
// them.
func TestIsolateStopsAtTheFirstProbeError(t *testing.T) {
	t.Parallel()

	boom := errors.New("the toolchain is on fire")
	var probes int
	failing := func(context.Context, []mutation.Mutant) (verdict, error) {
		probes++
		return verdict{}, boom
	}

	accepted, rejected, err := isolate(t.Context(), fakeMutants("sample.go", 10), failing)
	if !errors.Is(err, boom) {
		t.Fatalf("isolate returned %v, want the probe's error", err)
	}
	if accepted != nil || rejected != nil {
		t.Errorf("isolate returned %v and %v alongside the error, want neither", accepted, rejected)
	}
	if probes != 1 {
		t.Errorf("isolate probed %d times after an error, want 1", probes)
	}
}

// A fakeCompiler answers "does this subset compile" from a table: a set of
// candidates that fail alone, and at most one pair that fails only together.
type fakeCompiler struct {
	cands []mutation.Mutant
	bad   []int
	pair  []int
	// probes counts the questions asked, which is what the build count of a
	// real run would be.
	probes int
}

// probe is the [probe] the search is driven through.
func (f *fakeCompiler) probe(_ context.Context, subset []mutation.Mutant) (verdict, error) {
	f.probes++

	live := make(map[uint32]bool, len(subset))
	for _, m := range subset {
		live[m.Index] = true
	}
	var failing []mutation.Mutant
	for _, position := range f.bad {
		if live[uint32(position)] {
			failing = append(failing, f.cands[position])
		}
	}
	if len(f.pair) == 2 && live[uint32(f.pair[0])] && live[uint32(f.pair[1])] {
		failing = append(failing, f.cands[f.pair[1]])
	}
	if len(failing) == 0 {
		return verdict{}, nil
	}

	var b strings.Builder
	b.WriteString("# fixture.example/fake\n")
	for _, m := range failing {
		fmt.Fprintf(&b, "./%s:%d:9: cannot use guard (value of type bool) as Flag value in return statement\n",
			m.Path, m.Index+1)
	}
	return verdict{failed: true, output: b.String()}, nil
}

// fakeMutants builds n catalogue-shaped mutants for one file, each on its own
// line, with the dense indices a real catalogue would have assigned.
func fakeMutants(path string, n int) []mutation.Mutant {
	out := make([]mutation.Mutant, 0, n)
	for i := range n {
		id := fmt.Sprintf("%064x", i)
		out = append(out, mutation.Mutant{
			Index:     uint32(i),
			ID:        id,
			DisplayID: id[:mutation.DisplayIDLength],
			Candidate: mutation.Candidate{
				Path: path,
				Span: mutation.Span{StartByte: uint32(i), EndByte: uint32(i) + 1},
			},
		})
	}
	return out
}

// positionsOf renders mutants as their catalogue indices, which is how the
// tables above name them.
func positionsOf(mutants []mutation.Mutant) []int {
	if len(mutants) == 0 {
		return nil
	}
	out := make([]int, 0, len(mutants))
	for _, m := range mutants {
		out = append(out, int(m.Index))
	}
	return out
}

// mutantsOf unwraps condemned candidates.
func mutantsOf(rejected []condemned) []mutation.Mutant {
	out := make([]mutation.Mutant, 0, len(rejected))
	for _, r := range rejected {
		out = append(out, r.mutant)
	}
	return out
}

// seq returns the half-open range [from, to) as a slice.
func seq(from, to int) []int {
	if to <= from {
		return nil
	}
	out := make([]int, 0, to-from)
	for i := from; i < to; i++ {
		out = append(out, i)
	}
	return out
}

// TestIsolateStopsAtAProbeErrorWhereverItHappens is
// [TestIsolateStopsAtTheFirstProbeError] for every other place a build can fail
// to run.
//
// The search asks the compiler in six places -- the whole file, each half of a
// split, the join that verifies them, the scan that follows a failed join, and
// the scan below the threshold -- and a machine that stops compiling stops at
// whichever of them it happens to be in. Every one of those has to come back as
// the error it was: an infrastructure failure read as a red build would reject
// candidates for a reason that has nothing to do with them, and one read as a
// green build would accept a subset nobody compiled.
func TestIsolateStopsAtAProbeErrorWhereverItHappens(t *testing.T) {
	t.Parallel()

	boom := errors.New("the toolchain is on fire")

	// A compiler that answers honestly until the nth question and then cannot
	// run at all, which is the shape of a machine that loses its toolchain,
	// its disk or its patience in the middle of a search.
	failingAt := func(n int, inner *fakeCompiler) (probe, *int) {
		asked := 0
		return func(ctx context.Context, subset []mutation.Mutant) (verdict, error) {
			asked++
			if asked == n {
				return verdict{}, boom
			}
			return inner.probe(ctx, subset)
		}, &asked
	}

	for _, test := range []struct {
		name  string
		size  int
		bad   []int
		pair  []int
		after int
	}{
		{name: "the first build of the file", size: 16, bad: []int{3}, after: 1},
		{name: "the left half of a split", size: 16, bad: []int{3}, after: 2},
		{name: "somewhere inside the right half", size: 16, bad: []int{3}, after: 5},
		// A pair that compiles apart and fails together is what makes the join
		// fail, and the scan after it is the sixth place a build happens.
		{name: "the join that verifies two halves", size: 8, pair: []int{0, 7}, after: 4},
		{name: "the scan that follows a failed join", size: 8, pair: []int{0, 7}, after: 6},
		{name: "the scan below the threshold", size: 3, bad: []int{1}, after: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cands := fakeMutants("sample.go", test.size)
			inner := &fakeCompiler{cands: cands, bad: test.bad, pair: test.pair}
			p, asked := failingAt(test.after, inner)

			accepted, rejected, err := isolate(t.Context(), cands, p)
			if !errors.Is(err, boom) {
				t.Fatalf("isolate = %v, want the probe's own failure after %d builds", err, test.after)
			}
			if accepted != nil || rejected != nil {
				t.Errorf("isolate answered %v and %v beside the failure, want neither", accepted, rejected)
			}
			if *asked != test.after {
				t.Errorf("isolate asked %d times, want it to stop at %d", *asked, test.after)
			}
		})
	}
}

// TestIsolateScansAtTheThresholdAndHalvesAboveIt pins the boundary the two
// strategies meet at.
//
// Halving pays for itself only while a split is cheaper than the builds it
// saves, and at exactly [linearThreshold] it is not: the split costs a build
// per half and the scan spends one build per candidate and answers more. Which
// side of the boundary a file falls on is a number of builds, which is the one
// thing about this search that is the same on every machine.
func TestIsolateScansAtTheThresholdAndHalvesAboveIt(t *testing.T) {
	t.Parallel()

	// At the threshold the whole file is one failing probe followed by one
	// build per candidate, and nothing else.
	cands := fakeMutants("sample.go", linearThreshold)
	compiler := &fakeCompiler{cands: cands, bad: []int{0}}
	if _, _, err := isolate(t.Context(), cands, compiler.probe); err != nil {
		t.Fatalf("isolate: %v", err)
	}
	if want := 1 + linearThreshold; compiler.probes != want {
		t.Errorf("isolate spent %d builds on %d candidates, want %d: the first probe and one scan",
			compiler.probes, linearThreshold, want)
	}

	// One more candidate and it splits instead, which is a different shape and
	// a different count: the whole file, each half, a scan of the half that
	// failed, and the join that verifies the two.
	cands = fakeMutants("sample.go", linearThreshold+1)
	compiler = &fakeCompiler{cands: cands, bad: []int{0}}
	if _, _, err := isolate(t.Context(), cands, compiler.probe); err != nil {
		t.Fatalf("isolate: %v", err)
	}
	if want := 6; compiler.probes != want {
		t.Errorf("isolate spent %d builds on %d candidates, want %d",
			compiler.probes, linearThreshold+1, want)
	}
	// And at sixteen the split really is cheaper than scanning, which is the
	// whole reason it exists; [TestIsolateHalvesRatherThanScans] states that.
}

// TestIsolateDoesNotPayForAJoinWhoseAnswerIsKnown pins the two shortcuts, which
// are each a build not spent.
//
// A join of two accepted halves is worth verifying because candidates in one
// file can interact. A join with nothing on one side of it is not: it is the
// set that side's own last probe already compiled, or -- with nothing on either
// side -- the pristine file, which the phase proved compiles before it started
// searching. Spending a build on either would be asking a question whose answer
// is already written down.
func TestIsolateDoesNotPayForAJoinWhoseAnswerIsKnown(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		bad    []int
		want   []int
		builds int
	}{{
		// Everything in the left half fails, so the join is the right half and
		// the right half's own last probe compiled it. The whole file, the two
		// halves, and a scan of the one that failed.
		name:   "nothing survives one half",
		bad:    []int{0, 1, 2, 3},
		want:   []int{4, 5, 6, 7},
		builds: 1 + 1 + 4 + 1,
	}, {
		name:   "nothing survives the other half",
		bad:    []int{4, 5, 6, 7},
		want:   []int{0, 1, 2, 3},
		builds: 1 + 1 + 1 + 4,
	}, {
		// Nothing survives at all, so the join is the pristine file, and both
		// halves were scanned to find that out.
		name:   "nothing survives either half",
		bad:    []int{0, 1, 2, 3, 4, 5, 6, 7},
		want:   nil,
		builds: 1 + 2*(1+4),
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cands := fakeMutants("sample.go", 8)
			compiler := &fakeCompiler{cands: cands, bad: test.bad}
			accepted, rejected, err := isolate(t.Context(), cands, compiler.probe)
			if err != nil {
				t.Fatalf("isolate: %v", err)
			}
			if got := positionsOf(accepted); !slices.Equal(got, test.want) {
				t.Errorf("accepted %v, want %v", got, test.want)
			}
			if got := positionsOf(mutantsOf(rejected)); !slices.Equal(got, test.bad) {
				t.Errorf("rejected %v, want %v", got, test.bad)
			}
			// No join build among them, because there is nothing a join could
			// be asked that has not already been answered.
			if compiler.probes != test.builds {
				t.Errorf("isolate spent %d builds, want %d with no join among them",
					compiler.probes, test.builds)
			}
		})
	}
}

// TestIsolateVerifiesAJoinWithSomethingOnBothSides is the other side of the
// shortcut: a join that really is two halves costs the build that proves it.
func TestIsolateVerifiesAJoinWithSomethingOnBothSides(t *testing.T) {
	t.Parallel()

	cands := fakeMutants("sample.go", 8)
	compiler := &fakeCompiler{cands: cands, bad: []int{0, 7}}
	accepted, _, err := isolate(t.Context(), cands, compiler.probe)
	if err != nil {
		t.Fatalf("isolate: %v", err)
	}
	if got, want := positionsOf(accepted), []int{1, 2, 3, 4, 5, 6}; !slices.Equal(got, want) {
		t.Fatalf("accepted %v, want %v", got, want)
	}
	// One more than the shortcut cases above: the join of two non-empty halves
	// is the build this test is about.
	if want := 1 + 2*(1+4) + 1; compiler.probes != want {
		t.Errorf("isolate spent %d builds, want %d including the join", compiler.probes, want)
	}
}

// TestIsolateOfAnEmptyFileAsksNothing is the one call that is answered without
// a build at all, which is what keeps a catalogued file with no candidates from
// costing one.
func TestIsolateOfAnEmptyFileAsksNothing(t *testing.T) {
	t.Parallel()

	var probes int
	counting := func(context.Context, []mutation.Mutant) (verdict, error) {
		probes++
		return verdict{}, nil
	}
	accepted, rejected, err := isolate(t.Context(), nil, counting)
	if err != nil || accepted != nil || rejected != nil {
		t.Fatalf("isolate of nothing = %v, %v, %v", accepted, rejected, err)
	}
	if probes != 0 {
		t.Errorf("isolate asked %d times about a file with no candidates, want none", probes)
	}
}
