// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/trace"
)

// TestSearch drives the whole phase — first build, pristine gate, per-file
// isolation, the round that follows it — against a compiler that is a table.
//
// What the fake supplies is exactly the two operations the search is allowed:
// write a subset of one file's guards, and build. Everything else it asserts on
// its own behalf, which is what makes the fake worth having rather than a
// mirror of the code — a search that instrumented a file it had already decided,
// or that wrote one file's mutants into another, fails inside the fake rather
// than by producing a wrong answer somewhere downstream.
func TestSearch(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// files maps each catalogued file to how many candidates it holds, and
		// sizes are given in path order.
		files []fakeFile
		// bad are the catalogue indices that do not compile.
		bad []int
		// mask reports the failure against a file the catalogue does not know,
		// which is the compiler declining to point at the guard that broke it.
		mask bool
		// brokenAlways fails every build, gate included.
		brokenAlways bool
		// brokenFrom fails every build from that build number on.
		brokenFrom int

		// wantRejected are the indices the search must reject.
		wantRejected []int
		// wantState is what each file must be left holding.
		wantState map[string][]int
		// wantCode is the error the search must end with, if any.
		wantCode Code
		// wantBuilds pins the build count where it is the point.
		wantBuilds int
	}{
		{
			name:       "nothing is wrong",
			files:      []fakeFile{{"a.go", 6}, {"b.go", 4}},
			wantState:  map[string][]int{"a.go": seq(0, 6), "b.go": seq(6, 10)},
			wantBuilds: 1,
		},
		{
			name:         "one bad candidate of ten in one file",
			files:        []fakeFile{{"a.go", 10}},
			bad:          []int{6},
			wantRejected: []int{6},
			wantState:    map[string][]int{"a.go": {0, 1, 2, 3, 4, 5, 7, 8, 9}},
		},
		{
			name:         "two bad candidates in one file",
			files:        []fakeFile{{"a.go", 10}},
			bad:          []int{1, 8},
			wantRejected: []int{1, 8},
			wantState:    map[string][]int{"a.go": {0, 2, 3, 4, 5, 6, 7, 9}},
		},
		{
			name:         "bad candidates in two files",
			files:        []fakeFile{{"a.go", 6}, {"b.go", 6}},
			bad:          []int{2, 9},
			wantRejected: []int{2, 9},
			wantState: map[string][]int{
				"a.go": {0, 1, 3, 4, 5},
				"b.go": {6, 7, 8, 10, 11},
			},
		},
		{
			// The file comes out pristine, which is the only state that
			// compiles: a file with the runtime import and no guard that reads
			// it would not build.
			name:         "every candidate in one file is bad",
			files:        []fakeFile{{"a.go", 3}, {"b.go", 3}},
			bad:          seq(0, 3),
			wantRejected: seq(0, 3),
			wantState:    map[string][]int{"a.go": nil, "b.go": seq(3, 6)},
		},
		{
			// The compiler names a file this run does not catalogue, so there
			// is nothing to blame and everything undecided has to be searched.
			// Slower, same answer — which is the trade this phase always makes.
			name:         "a failure the compiler does not attribute",
			files:        []fakeFile{{"a.go", 5}, {"b.go", 5}},
			bad:          []int{7},
			mask:         true,
			wantRejected: []int{7},
			wantState: map[string][]int{
				"a.go": seq(0, 5),
				"b.go": {5, 6, 8, 9},
			},
		},
		{
			// The gate build fails with every guard removed, so nothing this
			// phase could reject would fix it.
			name:         "a tree that was broken before go-mutants touched it",
			files:        []fakeFile{{"a.go", 4}},
			brokenAlways: true,
			wantCode:     CodeNotMutantInduced,
			wantState:    map[string][]int{"a.go": nil},
			wantBuilds:   2,
		},
		{
			// Every file has been isolated, each accepted subset compiled on
			// its own, and the tree still does not build. Here it is a compiler
			// that changed its mind; in a real run it is candidates in separate
			// files interacting. Either way the accepted set cannot be trusted
			// and the phase says so instead of returning it.
			name:         "a failure that survives isolating every file",
			files:        []fakeFile{{"a.go", 2}},
			bad:          []int{1},
			brokenFrom:   5,
			wantRejected: []int{1},
			wantCode:     CodeStillFailing,
			wantState:    map[string][]int{"a.go": {0}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			tree := newFakeTree(t, c.files)
			tree.bad = indexSet(c.bad)
			tree.mask = c.mask
			tree.brokenAlways = c.brokenAlways
			tree.brokenFrom = c.brokenFrom

			recorder, sink := recording(t)
			v := &validator{
				root:     posixRoot,
				paths:    tree.paths,
				byPath:   tree.byPath,
				apply:    tree.apply,
				build:    tree.build,
				recorder: recorder,
			}
			rejected, err := v.search(t.Context())

			// Every build the phase spent is one line of its account of
			// itself. The count is asserted for every case here rather than
			// in one test of its own, because a build that goes unrecorded is
			// invisible in exactly the run somebody is trying to explain.
			if got := len(opsOf(sink, trace.ValidateOpBuild)); got != tree.builds {
				t.Errorf("the recording holds %d build events, want the %d builds the search spent",
					got, tree.builds)
			}

			if got := CodeOf(err); got != c.wantCode {
				t.Fatalf("search failed with %q, want %q: %v", got, c.wantCode, err)
			}
			if got := positionsOf(mutantsOf(rejected)); !slices.Equal(got, c.wantRejected) {
				t.Errorf("rejected %v, want %v", got, c.wantRejected)
			}
			if got := tree.positions(); !maps.EqualFunc(got, c.wantState, slices.Equal) {
				t.Errorf("the snapshot holds %v, want %v", got, c.wantState)
			}
			if c.wantBuilds > 0 && tree.builds != c.wantBuilds {
				t.Errorf("the search spent %d builds, want %d", tree.builds, c.wantBuilds)
			}
			// Whatever the search left behind has to be a tree that compiles,
			// unless it said out loud that it could not produce one.
			if c.wantCode == "" {
				if v, err := tree.build(context.Background()); err != nil || v.failed {
					t.Errorf("the search returned with a tree that does not build: %+v %v", v, err)
				}
			}
		})
	}
}

// TestSearchAcceptsEverythingInOneBuild states the fast path on its own, in the
// terms the design promises it in.
//
// One build for a whole catalogue is the entire justification for instrumenting
// every mutant at once, and it is invisible in an assertion about accepted sets:
// a phase that rebuilt once per file would return exactly the same answer. The
// count is the assertion.
func TestSearchAcceptsEverythingInOneBuild(t *testing.T) {
	t.Parallel()

	tree := newFakeTree(t, []fakeFile{{"a.go", 20}, {"b.go", 20}, {"c.go", 20}})
	v := &validator{root: posixRoot, paths: tree.paths, byPath: tree.byPath, apply: tree.apply, build: tree.build}

	rejected, err := v.search(t.Context())
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rejected) != 0 {
		t.Errorf("search rejected %d candidates, want none", len(rejected))
	}
	if tree.builds != 1 {
		t.Errorf("search spent %d builds on a catalogue that compiles, want 1", tree.builds)
	}
	if tree.applies != 0 {
		t.Errorf("search rewrote %d files on the fast path, want none", tree.applies)
	}
}

// TestSearchRecordsEveryBuildIsolationAndRejection is what a reader of a
// validation that dropped a mutant has to be able to reconstruct.
//
// A rejection is an ordinary outcome of the design and the run carries on past
// it, so the only place the reasoning survives is the recording: which builds
// were spent, which file was searched and what it offered, which candidate was
// condemned and what the compiler said about it. Without it a user sees one
// mutant fewer in a report and has nothing to ask about it.
func TestSearchRecordsEveryBuildIsolationAndRejection(t *testing.T) {
	t.Parallel()

	tree := newFakeTree(t, []fakeFile{{"a.go", 10}})
	tree.bad = indexSet([]int{6})
	recorder, sink := recording(t)
	v := &validator{
		root:     posixRoot,
		paths:    tree.paths,
		byPath:   tree.byPath,
		apply:    tree.apply,
		build:    tree.build,
		recorder: recorder,
	}

	rejected, err := v.search(t.Context())
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := positionsOf(mutantsOf(rejected)); !slices.Equal(got, []int{6}) {
		t.Fatalf("rejected %v, want the one bad candidate", got)
	}

	builds := opsOf(sink, trace.ValidateOpBuild)
	if len(builds) != tree.builds {
		t.Fatalf("the recording holds %d build events, want the %d the search spent", len(builds), tree.builds)
	}
	for i, record := range builds {
		if record.Build != i+1 {
			t.Errorf("build event %d is numbered %d, want %d", i, record.Build, i+1)
		}
		// The compile the step ran, so a step and the command underneath it
		// are one thing in the stream.
		if want := tree.execSeq(i + 1); record.ExecSeq != want {
			t.Errorf("build %d points at exec %d, want %d", record.Build, record.ExecSeq, want)
		}
		if record.Tree != trace.ValidateTreeMutant {
			t.Errorf("build %d is about the %q tree, want %q", record.Build, record.Tree, trace.ValidateTreeMutant)
		}
	}

	if gates := opsOf(sink, trace.ValidateOpGate); len(gates) != 1 || gates[0].Failed {
		t.Errorf("the recording holds %+v for the gate, want exactly one that passed", gates)
	}

	// Which isolation spent which build. The three builds about the tree as a
	// whole — the first, the gate, and the one that closes the round — name no
	// file, and everything between them was spent asking one file a question.
	if len(builds) < 4 {
		t.Fatalf("the search spent %d builds, want the whole-tree ones and the isolation's", len(builds))
	}
	for _, i := range []int{0, 1, len(builds) - 1} {
		if builds[i].Path != "" {
			t.Errorf("build %d is about %q, want no file: it is a build of the whole tree",
				builds[i].Build, builds[i].Path)
		}
	}
	for _, record := range builds[2 : len(builds)-1] {
		if record.Path != "a.go" {
			t.Errorf("build %d is about %q, want the file being isolated %q", record.Build, record.Path, "a.go")
		}
	}

	isolations := opsOf(sink, trace.ValidateOpIsolate)
	if len(isolations) != 1 {
		t.Fatalf("the recording holds %d isolate events, want the one file the search took", len(isolations))
	}
	if got := isolations[0]; got.Path != "a.go" || got.Candidates != 10 || got.Accepted != 9 {
		t.Errorf("the isolation reads %+v, want a.go offering 10 and keeping 9", got)
	}

	rejects := opsOf(sink, trace.ValidateOpReject)
	if len(rejects) != 1 {
		t.Fatalf("the recording holds %d reject events, want one per condemned candidate", len(rejects))
	}
	if want := fmt.Sprintf("%064x", 6); rejects[0].MutantID != want {
		t.Errorf("the rejection names %q, want the condemned mutant %q", rejects[0].MutantID, want)
	}
	if rejects[0].Path != "a.go" {
		t.Errorf("the rejection is about %q, want %q", rejects[0].Path, "a.go")
	}
	// The first line of the build that condemned it, and no more: by the time
	// the phase ends the tree compiles and that message exists nowhere else.
	if got := rejects[0].Diagnostic; !strings.Contains(got, "cannot use guard") || strings.Contains(got, "\n") {
		t.Errorf("the rejection quotes %q, want the first line of the condemning build", got)
	}

	done := opsOf(sink, trace.ValidateOpDone)
	if len(done) != 1 {
		t.Fatalf("the recording holds %d done events, want exactly one", len(done))
	}
	if got := done[0]; got.Builds != tree.builds || got.Accepted != 9 || got.Rejected != 1 {
		t.Errorf("the phase closed with %+v, want %d builds, 9 accepted and 1 rejected", got, tree.builds)
	}
}

// TestSearchRecordsTheGateWhenTheTreeIsBrokenWithoutMutants records the one
// failure this phase refuses to blame on a candidate.
//
// A tree that does not build with every guard removed was broken before
// go-mutants touched it, and the gate is what establishes that. It is the
// difference between "your code does not compile" and "go-mutants rejected
// eleven mutants", and a recording that did not hold it would leave a reader
// with an error message and no evidence for it.
func TestSearchRecordsTheGateWhenTheTreeIsBrokenWithoutMutants(t *testing.T) {
	t.Parallel()

	tree := newFakeTree(t, []fakeFile{{"a.go", 4}})
	tree.brokenAlways = true
	recorder, sink := recording(t)
	v := &validator{
		root:     posixRoot,
		paths:    tree.paths,
		byPath:   tree.byPath,
		apply:    tree.apply,
		build:    tree.build,
		recorder: recorder,
	}

	if _, err := v.search(t.Context()); CodeOf(err) != CodeNotMutantInduced {
		t.Fatalf("search failed with %q, want %q: %v", CodeOf(err), CodeNotMutantInduced, err)
	}

	gates := opsOf(sink, trace.ValidateOpGate)
	if len(gates) != 1 {
		t.Fatalf("the recording holds %d gate events, want exactly one", len(gates))
	}
	if !gates[0].Failed {
		t.Errorf("the gate reads %+v, want the failure that stopped the phase", gates[0])
	}

	// The gate build names nobody. It is a build of the tree with every guard
	// removed, so the files the compiler pointed at are not a list of where to
	// search — there is nothing to search, which is the whole finding — and a
	// `blamed` there would read as candidates about to be condemned.
	builds := opsOf(sink, trace.ValidateOpBuild)
	if len(builds) != 2 {
		t.Fatalf("the recording holds %d build events, want the first and the gate", len(builds))
	}
	if got := builds[1]; len(got.Blamed) != 0 || got.Pending != 0 {
		t.Errorf("the gate's build reads %+v, want no blamed files and nothing pending", got)
	}
	if got := opsOf(sink, trace.ValidateOpDone); len(got) != 0 {
		t.Errorf("the recording closes with %+v, want nothing: the phase decided nothing", got)
	}
	for _, op := range []string{trace.ValidateOpIsolate, trace.ValidateOpReject} {
		if got := opsOf(sink, op); len(got) != 0 {
			t.Errorf("the recording holds %d %s events, want none: nothing was bisected", len(got), op)
		}
	}
}

// recording opens a recorder over an unbounded ring and returns both.
func recording(t *testing.T) (*trace.Recorder, *trace.MemorySink) {
	t.Helper()
	sink := trace.NewMemorySink(0)
	recorder := trace.New(sink, time.Now, trace.StartRecord{
		Kind:        trace.StartKindRun,
		ToolVersion: "test",
		PID:         os.Getpid(),
		Root:        t.TempDir(),
	})
	if recorder == nil {
		t.Fatal("trace.New returned no recorder for a real sink")
	}
	return recorder, sink
}

// opsOf returns the validation steps of one kind the sink kept, in order.
func opsOf(sink *trace.MemorySink, op string) []trace.ValidateRecord {
	var found []trace.ValidateRecord
	for _, event := range sink.Events() {
		if event.Validate != nil && event.Validate.Op == op {
			found = append(found, *event.Validate)
		}
	}
	return found
}

// A fakeFile is one catalogued file and how many candidates it holds.
type fakeFile struct {
	path string
	size int
}

// A fakeTree is a snapshot and a compiler, both made of a table.
//
// It owns the state the real phase keeps on disk — which subset of each file's
// candidates is currently written — so that a test can assert on the tree the
// search left behind, which is half of what this phase produces and the half
// nothing downstream would notice going wrong.
type fakeTree struct {
	t      *testing.T
	paths  []string
	byPath map[string][]mutation.Mutant

	bad          map[uint32]bool
	mask         bool
	brokenAlways bool
	brokenFrom   int

	state   map[string][]mutation.Mutant
	builds  int
	applies int
}

// newFakeTree lays out one catalogue over several files, numbering the mutants
// densely across the whole of it exactly as [mutation.Catalog] does.
func newFakeTree(t *testing.T, files []fakeFile) *fakeTree {
	t.Helper()

	tree := &fakeTree{
		t:      t,
		byPath: make(map[string][]mutation.Mutant, len(files)),
		state:  make(map[string][]mutation.Mutant, len(files)),
	}
	var index int
	for _, f := range files {
		mutants := fakeMutants(f.path, f.size)
		for i := range mutants {
			mutants[i].Index = uint32(index)
			mutants[i].ID = fmt.Sprintf("%064x", index)
			mutants[i].DisplayID = mutants[i].ID[:mutation.DisplayIDLength]
			index++
		}
		tree.paths = append(tree.paths, f.path)
		tree.byPath[f.path] = mutants
		// The search's own precondition: the phase reaches it with every file
		// instrumented whole, because that is what the first build measured.
		tree.state[f.path] = mutants
	}
	slices.Sort(tree.paths)
	return tree
}

// apply writes one file's guards, and refuses what the real instrumenter would
// refuse.
func (f *fakeTree) apply(path string, subset []mutation.Mutant) error {
	f.t.Helper()
	f.applies++

	if _, known := f.byPath[path]; !known {
		f.t.Errorf("the search wrote to %q, which the catalogue does not name", path)
	}
	for _, m := range subset {
		if m.Path != path {
			f.t.Errorf("the search wrote mutant %d of %q into %q", m.Index, m.Path, path)
		}
	}
	f.state[path] = slices.Clone(subset)
	return nil
}

// execSeq is the sequence the nth build of this tree pretends its compile was
// recorded at. It is deliberately not the build number: a step that copied the
// wrong one of the two would still line up if they were equal.
func (f *fakeTree) execSeq(build int) int64 { return int64(100 + build) }

// build reports whether the tree as currently written compiles.
func (f *fakeTree) build(context.Context) (verdict, error) {
	f.builds++
	seq := f.execSeq(f.builds)

	if f.brokenAlways || (f.brokenFrom > 0 && f.builds >= f.brokenFrom) {
		return verdict{
			failed:  true,
			output:  "# fixture.example/fake\n./unrelated.go:1:1: undefined: somethingElse\n",
			execSeq: seq,
		}, nil
	}

	var failing []mutation.Mutant
	for _, path := range f.paths {
		for _, m := range f.state[path] {
			if f.bad[m.Index] {
				failing = append(failing, m)
			}
		}
	}
	if len(failing) == 0 {
		return verdict{execSeq: seq}, nil
	}

	var b strings.Builder
	b.WriteString("# fixture.example/fake\n")
	for _, m := range failing {
		path := m.Path
		if f.mask {
			path = "unrelated.go"
		}
		fmt.Fprintf(&b, "./%s:%d:9: cannot use guard (value of type bool) as Flag value in return statement\n",
			path, m.Index+1)
	}
	return verdict{failed: true, output: b.String(), execSeq: seq}, nil
}

// positions renders what the tree holds as catalogue indices per file.
func (f *fakeTree) positions() map[string][]int {
	out := make(map[string][]int, len(f.state))
	for path, subset := range f.state {
		out[path] = positionsOf(subset)
	}
	return out
}

// indexSet turns a list of catalogue positions into the lookup the fake
// compiler consults.
func indexSet(positions []int) map[uint32]bool {
	out := make(map[uint32]bool, len(positions))
	for _, p := range positions {
		out[uint32(p)] = true
	}
	return out
}
