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

func TestIsolateFindsTheCandidatesThatDoNotCompile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		size         int
		bad          []int
		pair         []int
		want         []int
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
			name:         "one bad of three",
			size:         3,
			bad:          []int{1},
			want:         []int{0, 2},
			wantRejected: []int{1},
		},
		{
			name:         "an interacting pair across the split",
			size:         10,
			pair:         []int{2, 8},
			want:         []int{0, 1, 2, 3, 4, 5, 6, 7, 9},
			wantRejected: []int{8},
		},
		{
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
			for _, r := range rejected {
				if !strings.Contains(r.output, r.mutant.Path) {
					t.Errorf("the rejection of %s carries %q, which does not name its file",
						r.mutant.DisplayID, r.output)
				}
			}
			if v, err := compiler.probe(t.Context(), accepted); err != nil || v.failed {
				t.Errorf("the accepted set does not compile: %+v %v", v, err)
			}
		})
	}
}

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

type fakeCompiler struct {
	cands  []mutation.Mutant
	bad    []int
	pair   []int
	probes int
}

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

func mutantsOf(rejected []condemned) []mutation.Mutant {
	out := make([]mutation.Mutant, 0, len(rejected))
	for _, r := range rejected {
		out = append(out, r.mutant)
	}
	return out
}

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

func TestIsolateStopsAtAProbeErrorWhereverItHappens(t *testing.T) {
	t.Parallel()

	boom := errors.New("the toolchain is on fire")

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

func TestIsolateScansAtTheThresholdAndHalvesAboveIt(t *testing.T) {
	t.Parallel()

	cands := fakeMutants("sample.go", linearThreshold)
	compiler := &fakeCompiler{cands: cands, bad: []int{0}}
	if _, _, err := isolate(t.Context(), cands, compiler.probe); err != nil {
		t.Fatalf("isolate: %v", err)
	}
	if want := 1 + linearThreshold; compiler.probes != want {
		t.Errorf("isolate spent %d builds on %d candidates, want %d: the first probe and one scan",
			compiler.probes, linearThreshold, want)
	}

	cands = fakeMutants("sample.go", linearThreshold+1)
	compiler = &fakeCompiler{cands: cands, bad: []int{0}}
	if _, _, err := isolate(t.Context(), cands, compiler.probe); err != nil {
		t.Fatalf("isolate: %v", err)
	}
	if want := 6; compiler.probes != want {
		t.Errorf("isolate spent %d builds on %d candidates, want %d",
			compiler.probes, linearThreshold+1, want)
	}
}

func TestIsolateDoesNotPayForAJoinWhoseAnswerIsKnown(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		bad    []int
		want   []int
		builds int
	}{{
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
			if compiler.probes != test.builds {
				t.Errorf("isolate spent %d builds, want %d with no join among them",
					compiler.probes, test.builds)
			}
		})
	}
}

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
	if want := 1 + 2*(1+4) + 1; compiler.probes != want {
		t.Errorf("isolate spent %d builds, want %d including the join", compiler.probes, want)
	}
}

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
