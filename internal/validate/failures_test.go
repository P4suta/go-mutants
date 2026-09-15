// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package validate

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/trace"
)

// errStaged is the failure a test hands the phase in place of a machine that
// has stopped being able to compile.
var errStaged = errors.New("the machine cannot build")

// TestSearchCarriesUpAFailureFromWhereverItHappened is the rule the whole phase
// repeats: a build that could not be *run* is not a build that said no.
//
// The distinction is the difference between "this mutant cannot be compiled"
// and "this machine cannot compile", and the second has to reach the caller as
// itself. There are a dozen places the phase asks the filesystem or the
// compiler -- the first build, the restore before the gate, the gate, each
// probe of an isolation, the write of an accepted subset, the reinstatement,
// the build that closes a pass, the restore that opens the next -- and a
// failure read as a red build at any of them would reject candidates for a
// reason that has nothing to do with them.
//
// So rather than naming them, this sweeps: a successful search is run once to
// count how many times each seam is asked, and then the search is run again for
// every one of those, failing exactly that call.
func TestSearchCarriesUpAFailureFromWhereverItHappened(t *testing.T) {
	t.Parallel()

	// Two shapes, because the places a build happens depend on how many passes
	// the search makes. In the first, every file is blamed at once and the
	// search is one pass; in the second only one file is, so the other is
	// reinstated, and the tree turns red again afterwards so a second pass
	// restores it and looks once more.
	for _, shape := range []struct {
		name       string
		files      []fakeFile
		bad        []int
		brokenFrom int
	}{
		{name: "every file blamed at once", files: []fakeFile{{"a.go", 6}, {"b.go", 6}}, bad: []int{1, 9}},
		{name: "one file blamed and a second pass", files: []fakeFile{{"a.go", 6}, {"b.go", 6}}, bad: []int{1}, brokenFrom: 7},
	} {
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()
			searchFailsWherever(t, shape.files, shape.bad, shape.brokenFrom)
		})
	}
}

// searchFailsWherever is [TestSearchCarriesUpAFailureFromWhereverItHappened]
// over one shape of tree.
func searchFailsWherever(t *testing.T, files []fakeFile, bad []int, brokenFrom int) {
	t.Helper()

	counted := newFakeTree(t, files)
	counted.bad = indexSet(bad)
	counted.brokenFrom = brokenFrom
	countingValidator := &validator{
		root: posixRoot, paths: counted.paths, byPath: counted.byPath,
		apply: counted.apply, build: counted.build,
	}
	// The counting run may itself end in the phase's own refusal, which is a
	// perfectly good run to count: what is being counted is where the phase
	// asks, not what it concludes.
	if _, err := countingValidator.search(t.Context()); err != nil && CodeOf(err) != CodeStillFailing {
		t.Fatalf("the counting run failed: %v", err)
	}
	if counted.builds < 4 || counted.applies < 4 {
		t.Fatalf("the counting run spent %d builds and %d writes, want a search that reached its branches",
			counted.builds, counted.applies)
	}

	for _, seam := range []string{"build", "write"} {
		total := counted.builds
		if seam == "write" {
			total = counted.applies
		}
		for n := 1; n <= total; n++ {
			t.Run(seam+" number "+itoa(n), func(t *testing.T) {
				t.Parallel()

				tree := newFakeTree(t, files)
				tree.bad = indexSet(bad)
				tree.brokenFrom = brokenFrom
				v := &validator{root: posixRoot, paths: tree.paths, byPath: tree.byPath}

				builds, writes := 0, 0
				v.build = func(ctx context.Context) (verdict, error) {
					builds++
					if seam == "build" && builds == n {
						return verdict{}, errStaged
					}
					return tree.build(ctx)
				}
				v.apply = func(path string, subset []mutation.Mutant) error {
					writes++
					if seam == "write" && writes == n {
						return errStaged
					}
					return tree.apply(path, subset)
				}

				rejected, err := v.search(t.Context())
				if !errors.Is(err, errStaged) {
					t.Fatalf("search = %v, want the staged failure from %s %d", err, seam, n)
				}
				// Whatever it had decided before it stopped is what comes back
				// beside the failure: the phase reports what it did rather than
				// pretending it did nothing, and a caller writing a partial
				// report reads exactly that.
				for _, c := range rejected {
					if c.mutant.Path == "" {
						t.Errorf("a rejection came back with no path: %+v", c)
					}
				}
			})
		}
	}
}

// itoa is strconv.Itoa under a name this file can call in a subtest name.
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// TestSearchStopsBeforeTheGateWhenTheFirstBuildIsGreen is the fast path stated
// as what it does *not* do.
func TestSearchStopsBeforeTheGateWhenTheFirstBuildIsGreen(t *testing.T) {
	t.Parallel()

	tree := newFakeTree(t, []fakeFile{{"a.go", 4}})
	v := &validator{root: posixRoot, paths: tree.paths, byPath: tree.byPath, apply: tree.apply, build: tree.build}

	rejected, err := v.search(t.Context())
	if err != nil || rejected != nil {
		t.Fatalf("search = %v, %v, want a clean accept", rejected, err)
	}
	if tree.builds != 1 {
		t.Errorf("the search spent %d builds on a tree that compiles, want one", tree.builds)
	}
	if tree.applies != 0 {
		t.Errorf("the search rewrote %d files on a tree that compiles, want none", tree.applies)
	}
}

// TestBlameFallsBackToEverythingUndecided pins the answer a failing build with
// nothing useful to say gets.
//
// A compiler can report a guard's damage at a line in another file, or say
// nothing about a file whose package never got compiled because a dependency
// failed first. In both cases the candidates are still in the tree and still
// have to be found, so a build that names no undecided file sends the search
// over all of them: slower, and it terminates with the same answer.
func TestBlameFallsBackToEverythingUndecided(t *testing.T) {
	t.Parallel()

	v := &validator{root: posixRoot}
	pending := []string{"a.go", "b.go", "c.go"}

	named := v.blame(verdict{
		failed: true,
		output: "# fixture.example/x\n./b.go:3:9: undefined: guard\n",
	}, pending)
	if want := []string{"b.go"}; !slices.Equal(named, want) {
		t.Errorf("blame = %v, want %v", named, want)
	}

	// A file the compiler named that is not undecided is not a place to look:
	// it has been searched already, and going back to it would undo the pass.
	named = v.blame(verdict{
		failed: true,
		output: "# fixture.example/x\n./already.go:3:9: undefined: guard\n",
	}, pending)
	if !slices.Equal(named, pending) {
		t.Errorf("blame = %v, want every undecided file", named)
	}

	// And output naming nothing at all inside the snapshot.
	named = v.blame(verdict{failed: true, output: "# fixture.example/x\nsignal: killed\n"}, pending)
	if !slices.Equal(named, pending) {
		t.Errorf("blame = %v, want every undecided file", named)
	}
	// The fallback is a copy: a caller that pruned the answer would be pruning
	// the pending list the search is iterating.
	named[0] = "changed.go"
	if pending[0] != "a.go" {
		t.Error("blame handed back the pending list itself")
	}

	// Nothing pending is nothing to blame, whatever the compiler said.
	if got := v.blame(verdict{failed: true, output: "./a.go:1:1: x\n"}, nil); len(got) != 0 {
		t.Errorf("blame with nothing pending = %v, want nothing", got)
	}
}

// TestBuildTimeoutTreatsEveryNonPositiveValueAsNoChoice pins the one number a
// reader of `-v` output sees for this phase.
func TestBuildTimeoutTreatsEveryNonPositiveValueAsNoChoice(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		in   time.Duration
		want time.Duration
	}{
		{in: 0, want: DefaultBuildTimeout},
		{in: -time.Second, want: DefaultBuildTimeout},
		{in: -time.Nanosecond, want: DefaultBuildTimeout},
		{in: time.Nanosecond, want: time.Nanosecond},
		{in: 90 * time.Second, want: 90 * time.Second},
	} {
		if got := buildTimeout(test.in); got != test.want {
			t.Errorf("buildTimeout(%v) = %v, want %v", test.in, got, test.want)
		}
	}
}

// TestRuntimeDirsNamesEveryGeneratedPackageAndNothingElse is what the drift
// gate reads.
//
// A generated runtime is a directory go-mutants wrote into the snapshot, and
// the gate compares what changed against what it was told would change. A
// runtime missing from this list is a directory the gate reports as drift; one
// invented is a directory the gate stops noticing.
func TestRuntimeDirsNamesEveryGeneratedPackageAndNothingElse(t *testing.T) {
	t.Parallel()

	result := Result{Runtimes: []instrument.Result{
		{RuntimeDir: "app/gomutants_rt"},
		{RuntimeDir: ""},
		{RuntimeDir: "lib/gomutants_rt"},
	}}
	want := []string{"app/gomutants_rt", "lib/gomutants_rt"}
	if got := result.RuntimeDirs(); !slices.Equal(got, want) {
		t.Errorf("RuntimeDirs = %v, want %v", got, want)
	}
	// A phase that instrumented nothing names nothing, as an empty list rather
	// than as an absence: the gate is handed a set of exceptions, and a nil one
	// is a set with no exceptions in it.
	if got := (Result{}).RuntimeDirs(); got == nil || len(got) != 0 {
		t.Errorf("RuntimeDirs of nothing = %v, want an empty list", got)
	}
}

// TestTheRecordingSaysWhichTreeWasValidated pins the label a reader tells the
// two validations of one run apart by.
func TestTheRecordingSaysWhichTreeWasValidated(t *testing.T) {
	t.Parallel()

	if got := (&validator{mode: instrument.ModeProbe}).tree(); got != trace.ValidateTreeProbe {
		t.Errorf("tree() of a probe validation = %q, want %q", got, trace.ValidateTreeProbe)
	}
	if got := (&validator{}).tree(); got != trace.ValidateTreeMutant {
		t.Errorf("tree() of a mutant validation = %q, want %q", got, trace.ValidateTreeMutant)
	}
	if trace.ValidateTreeProbe == trace.ValidateTreeMutant {
		t.Error("the two trees are spelled the same, so a recording cannot tell them apart")
	}
}

// TestModuleOfFindsTheModuleAMutantsPathBelongsTo is the lookup a workspace
// makes necessary: two modules can each hold an `app.go`, and the file's own
// module path is what keeps them apart.
func TestModuleOfFindsTheModuleAMutantsPathBelongsTo(t *testing.T) {
	t.Parallel()

	v := &validator{
		modules: []Module{
			{Dir: "app", Path: "example.com/app"},
			{Dir: "lib", Path: "example.com/lib"},
		},
		catalog: catalogOf(t, "example.com/app", "example.com/lib"),
	}

	got, ok := v.moduleOf("example.com/lib")
	if !ok || got.Dir != "lib" {
		t.Errorf("moduleOf = %+v, %v, want the lib module", got, ok)
	}
	if _, ok = v.moduleOf("example.com/nobody"); ok {
		t.Error("moduleOf found a module nobody gave")
	}
	// A single-module tree catalogues its mutants with no module path at all,
	// and the one module answers for them however it is spelled.
	single := &validator{
		modules: []Module{{Dir: ".", Path: "example.com/m"}},
		catalog: catalogOf(t, ""),
	}
	if _, ok = single.moduleOf(""); !ok {
		t.Error("moduleOf did not answer for a single-module tree")
	}
}

// TestRuntimeImportOfPairsAModuleWithTheRuntimeWrittenForIt is the other half,
// and the one that decides which generated package a rewritten file imports.
//
// A file that imported another module's runtime would spell a dense index into
// an array that numbers somebody else's mutants, which is a guard that activates
// the wrong one.
func TestRuntimeImportOfPairsAModuleWithTheRuntimeWrittenForIt(t *testing.T) {
	t.Parallel()

	modules := []Module{
		{Dir: "app", Path: "example.com/app"},
		{Dir: "lib", Path: "example.com/lib"},
	}
	v := &validator{
		modules: modules,
		runtimes: []instrument.Result{
			{RuntimeImport: "example.com/app/gomutants_rt"},
			{RuntimeImport: "example.com/lib/gomutants_rt"},
		},
	}
	for i, module := range modules {
		want := v.runtimes[i].RuntimeImport
		if got := v.runtimeImportOf(module); got != want {
			t.Errorf("runtimeImportOf(%v) = %q, want %q", module, got, want)
		}
	}
	// A module nobody instrumented has no runtime to import, and saying so
	// with the empty string is what makes the rewrite refuse rather than
	// import the first one it finds.
	if got := v.runtimeImportOf(Module{Dir: "other", Path: "example.com/other"}); got != "" {
		t.Errorf("runtimeImportOf an uninstrumented module = %q, want nothing", got)
	}
	// And a module whose runtime was never written -- more modules than
	// instrumentation results, which is what a failure part way through
	// leaves.
	short := &validator{modules: modules, runtimes: v.runtimes[:1]}
	if got := short.runtimeImportOf(modules[1]); got != "" {
		t.Errorf("runtimeImportOf past the runtimes = %q, want nothing", got)
	}
}

// TestPositionIsOneBasedOnBothAxesAndCountsBytes pins the coordinates a
// rejected mutant is named in, which have to be the ones discovery puts on a
// live one.
func TestPositionIsOneBasedOnBothAxesAndCountsBytes(t *testing.T) {
	t.Parallel()

	src := []byte("package a\n\nfunc F() int {\n\treturn 1\n}\n")
	for _, test := range []struct {
		name         string
		offset       uint32
		line, column int
	}{
		{name: "the first byte", offset: 0, line: 1, column: 1},
		{name: "the second byte", offset: 1, line: 1, column: 2},
		{name: "the newline that ends line one", offset: 9, line: 1, column: 10},
		{name: "the first byte of line two", offset: 10, line: 2, column: 1},
		{name: "the first byte of line three", offset: 11, line: 3, column: 1},
		{name: "a tab-indented line", offset: 27, line: 4, column: 2},
		{name: "the end of the file", offset: uint32(len(src)), line: 6, column: 1},
		// Past the end, which is what a span into a file that has since been
		// rewritten would be. It is clamped rather than allowed to slice past
		// the end, because a coordinate is worth more than a panic.
		{name: "past the end", offset: uint32(len(src)) + 100, line: 6, column: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			line, column := position(src, test.offset)
			if line != test.line || column != test.column {
				t.Errorf("position(%d) = %d:%d, want %d:%d", test.offset, line, column, test.line, test.column)
			}
		})
	}

	// A file with nothing in it still has a first line and a first column.
	if line, column := position(nil, 0); line != 1 || column != 1 {
		t.Errorf("position of an empty file = %d:%d, want 1:1", line, column)
	}
}

// TestResultNamesOneRuntimeOnlyWhenThereIsOne is the difference between a
// module and a workspace in the merged view.
//
// A tree with one module has one runtime and it is the tree's. A workspace has
// one per module and no single answer, so the merged view names none: a caller
// that read one of them as "the" runtime would be pointing half the tree at the
// other half's activation array.
func TestResultNamesOneRuntimeOnlyWhenThereIsOne(t *testing.T) {
	t.Parallel()

	one := &validator{runtimes: []instrument.Result{
		{RuntimeDir: "gomutants_rt", RuntimeImport: "example.com/m/gomutants_rt"},
	}}
	got := one.result()
	if got.Instrumented.RuntimeDir != "gomutants_rt" || got.Instrumented.RuntimeImport == "" {
		t.Errorf("the merged view of one module = %+v, want its runtime named", got.Instrumented)
	}

	two := &validator{runtimes: []instrument.Result{
		{RuntimeDir: "app/gomutants_rt", RuntimeImport: "example.com/app/gomutants_rt"},
		{RuntimeDir: "lib/gomutants_rt", RuntimeImport: "example.com/lib/gomutants_rt"},
	}}
	got = two.result()
	if got.Instrumented.RuntimeDir != "" || got.Instrumented.RuntimeImport != "" {
		t.Errorf("the merged view of a workspace = %+v, want no single runtime named", got.Instrumented)
	}
	if len(got.Runtimes) != 2 {
		t.Errorf("Runtimes = %v, want both of them", got.Runtimes)
	}

	// And none at all: a phase that instrumented nothing names nothing.
	if named := (&validator{}).result(); named.Instrumented.RuntimeDir != "" {
		t.Errorf("the merged view of nothing = %+v", named.Instrumented)
	}
}

// TestRejectionAlwaysCarriesADiagnostic is the silence this whole phase exists
// to avoid.
//
// RunReport v1 requires a non-empty diagnostic on every rejected entry, and a
// rejection without one tells a user their mutant was refused and declines to
// say why. There are three tiers and the last of them says that the compiler
// said nothing, which is still an answer.
func TestRejectionAlwaysCarriesADiagnostic(t *testing.T) {
	t.Parallel()

	src := []byte("package a\n\nfunc F() int {\n\treturn 1\n}\n")
	v := &validator{root: posixRoot, pristine: map[string][]byte{"a.go": src}}
	m := mutation.Mutant{
		ID:        strings.Repeat("a", 64),
		DisplayID: strings.Repeat("a", 8),
		Candidate: mutation.Candidate{
			Path: "a.go",
			Span: mustSpan(t, 27, 35),
			Rule: mutation.Rule{Name: "return-zero-numeric"},
		},
	}

	for _, test := range []struct {
		name   string
		output string
		want   string
	}{{
		name:   "a diagnostic inside the mutant's own lines",
		output: "# fixture.example/x\n./a.go:4:9: cannot use guard\n",
		want:   "./a.go:4:9: cannot use guard",
	}, {
		name:   "a diagnostic elsewhere in the same file",
		output: "# fixture.example/x\n./a.go:1:1: something else\n",
		want:   "./a.go:1:1: something else",
	}, {
		name:   "output that names nothing in the snapshot",
		output: "# fixture.example/x\nsignal: killed\n",
		want:   "# fixture.example/x",
	}, {
		name:   "no output at all",
		output: "",
		want:   "the build failed without printing a diagnostic",
	}, {
		name:   "output that is only blank lines",
		output: "\n\n   \n",
		want:   "the build failed without printing a diagnostic",
	}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := v.rejection(m, test.output)
			if got.Diagnostic != test.want {
				t.Errorf("Diagnostic = %q, want %q", got.Diagnostic, test.want)
			}
			if got.ID != m.ID || got.Path != m.Path || got.Rule != m.Rule.Name {
				t.Errorf("Rejection = %+v, want the mutant's own identity", got)
			}
			if got.Line != 4 || got.Column != 2 {
				t.Errorf("Rejection is at %d:%d, want the coordinates discovery uses", got.Line, got.Column)
			}
		})
	}
}

// mustSpan builds a span or fails the test.
func mustSpan(t *testing.T, start, end uint32) mutation.Span {
	t.Helper()
	span, err := mutation.NewSpan(start, end)
	if err != nil {
		t.Fatalf("NewSpan(%d, %d): %v", start, end, err)
	}
	return span
}

// TestBuildArgsCarryEveryPackageTheCallerNamed pins the vector one build is
// made with, including the capacity arithmetic that decides nothing and would
// panic if it were wrong.
func TestBuildArgsCarryEveryPackageTheCallerNamed(t *testing.T) {
	t.Parallel()

	// More packages than the head of the vector, which is where a capacity
	// computed by subtraction rather than addition stops being a capacity.
	packages := []string{"./a/...", "./b/...", "./c/...", "./d/...", "./e/...", "./f/...", "./g/..."}
	args := buildArgs(4, packages)
	if got := args[len(args)-len(packages):]; !slices.Equal(got, packages) {
		t.Errorf("buildArgs ended with %v, want %v", got, packages)
	}
	if !slices.Contains(args, "-p") {
		t.Errorf("buildArgs = %v, want the job count in it", args)
	}

	// No job count and no packages: the whole tree, and nothing about
	// parallelism.
	args = buildArgs(0, nil)
	if slices.Contains(args, "-p") {
		t.Errorf("buildArgs = %v, want no job count when none was chosen", args)
	}
	if args[len(args)-1] != "./..." {
		t.Errorf("buildArgs = %v, want the whole tree when no package was named", args)
	}
}

// catalogOf builds a catalogue holding one candidate per module path given, so
// that the module lookup has the thing it asks about which kind of tree this
// is: a catalogue whose first mutant names no module is a single-module tree.
func catalogOf(t *testing.T, modulePaths ...string) *mutation.Catalog {
	t.Helper()

	const source = "package a\n\nfunc F(count int) int { return count }\n"
	builder := mutation.NewBuilder()
	for i, modulePath := range modulePaths {
		start := uint32(37 + i)
		span, err := mutation.NewSpan(start, start+1)
		if err != nil {
			t.Fatalf("NewSpan: %v", err)
		}
		err = builder.Add(mutation.Candidate{
			Path:         "a.go",
			ModulePath:   modulePath,
			Rule:         mutation.Rule{Name: "return-zero-numeric", Version: 1, Family: mutation.FamilyReturnReplacement, Tier: mutation.TierBalanced},
			Span:         span,
			Original:     "c",
			Replacement:  "0",
			SourceDigest: mutation.Digest([]byte(source)),
		})
		if err != nil {
			t.Fatalf("cataloguing a candidate for %q: %v", modulePath, err)
		}
	}
	catalog, err := builder.Build()
	if err != nil {
		t.Fatalf("building the catalogue: %v", err)
	}
	return catalog
}

// TestSearchReportsWhatItHadRejectedBeforeItStopped is the other half of the
// rule above: a phase that stops reports what it had already decided.
//
// The rejections are what a partial report is written from, and a search that
// answered nothing beside its failure would throw away every build it had
// already spent -- which is the difference between a run that can say why it
// stopped and one that can only say that it did.
func TestSearchReportsWhatItHadRejectedBeforeItStopped(t *testing.T) {
	t.Parallel()

	files := []fakeFile{{"a.go", 6}, {"b.go", 6}}
	bad := []int{1, 9}

	counted := newFakeTree(t, files)
	counted.bad = indexSet(bad)
	counting := &validator{
		root: posixRoot, paths: counted.paths, byPath: counted.byPath,
		apply: counted.apply, build: counted.build,
	}
	if _, err := counting.search(t.Context()); err != nil {
		t.Fatalf("the counting run failed: %v", err)
	}

	// The last build of a run that worked: by then both files have been
	// isolated and both bad candidates condemned, so the failure staged there
	// has something to come back beside.
	tree := newFakeTree(t, files)
	tree.bad = indexSet(bad)
	v := &validator{root: posixRoot, paths: tree.paths, byPath: tree.byPath, apply: tree.apply}
	builds := 0
	v.build = func(ctx context.Context) (verdict, error) {
		builds++
		if builds == counted.builds {
			return verdict{}, errStaged
		}
		return tree.build(ctx)
	}

	rejected, err := v.search(t.Context())
	if !errors.Is(err, errStaged) {
		t.Fatalf("search = %v, want the staged failure", err)
	}
	if got := positionsOf(mutantsOf(rejected)); !slices.Equal(got, bad) {
		t.Errorf("rejected %v beside the failure, want the %v it had already condemned", got, bad)
	}
}

// TestSearchSaysSoWhenEveryFileHasBeenIsolatedAndTheTreeStillFails pins the one
// answer that is neither a rejection nor an infrastructure failure.
//
// Every file has been isolated and each accepted subset compiled on its own, so
// whatever is left is candidates in separate files interacting. The accepted set
// cannot be trusted, and the phase says that rather than returning it -- and it
// says it only once there is nothing left to look at, because while anything is
// still undecided there is somewhere else to look.
func TestSearchSaysSoWhenEveryFileHasBeenIsolatedAndTheTreeStillFails(t *testing.T) {
	t.Parallel()

	// Two files, one bad candidate in the first, and a tree that turns red for
	// good part way through. The first pass decides a.go and leaves b.go
	// pending, so the phase has to make a second pass rather than refusing
	// with something still undecided.
	tree := newFakeTree(t, []fakeFile{{"a.go", 4}, {"b.go", 4}})
	tree.bad = indexSet([]int{1})
	tree.brokenFrom = 6
	v := &validator{root: posixRoot, paths: tree.paths, byPath: tree.byPath, apply: tree.apply, build: tree.build}

	rejected, err := v.search(t.Context())
	if got := CodeOf(err); got != CodeStillFailing {
		t.Fatalf("search = %q (%v), want %q", got, err, CodeStillFailing)
	}
	if !strings.Contains(err.Error(), "interact") {
		t.Errorf("the failure does not say what is left: %v", err)
	}
	if len(rejected) == 0 {
		t.Error("the phase reported no rejections at all, want what it had condemned on the way")
	}
	// Two passes, which is what makes this different from a refusal with
	// something still pending: the first decided one file and the second the
	// other.
	if tree.builds < 6 {
		t.Errorf("the search spent %d builds, want a second pass among them", tree.builds)
	}
}

// TestTheRecordingOfABuildCarriesBlameOnlyWhenThereIsSomeToCarry pins what one
// build's event says.
//
// Blame is the list of undecided files a *failing* build pointed at, and it is
// computed once because deriving it twice would parse the compiler's whole
// output twice per build. A green build has nothing to blame, and a trial build
// inside an isolation is not being asked the question at all -- the search
// already knows which file it is in. An event that carried either would tell a
// reader the search was about to go somewhere it was not.
func TestTheRecordingOfABuildCarriesBlameOnlyWhenThereIsSomeToCarry(t *testing.T) {
	t.Parallel()

	tree := newFakeTree(t, []fakeFile{{"a.go", 6}})
	tree.bad = indexSet([]int{2})
	recorder, sink := recording(t)
	v := &validator{
		root: posixRoot, paths: tree.paths, byPath: tree.byPath,
		apply: tree.apply, build: tree.build, recorder: recorder,
	}
	if _, err := v.search(t.Context()); err != nil {
		t.Fatalf("search: %v", err)
	}

	builds := opsOf(sink, trace.ValidateOpBuild)
	if len(builds) == 0 {
		t.Fatal("the recording holds no build at all")
	}
	var blamedEvents int
	for _, record := range builds {
		if len(record.Blamed) > 0 {
			blamedEvents++
			if record.Pending == 0 {
				t.Errorf("a build that blamed %v recorded no pending count", record.Blamed)
			}
		}
		if record.Pending > 0 && len(record.Blamed) == 0 {
			t.Errorf("a build that blamed nothing recorded a pending count of %d", record.Pending)
		}
		if !record.Failed && len(record.Blamed) > 0 {
			t.Errorf("a build that compiled blamed %v", record.Blamed)
		}
		if record.Path != "" && len(record.Blamed) > 0 {
			t.Errorf("a trial build of %q blamed %v", record.Path, record.Blamed)
		}
	}
	if blamedEvents == 0 {
		t.Error("no build in the recording blamed anything, so the rule is untested here")
	}
	// The last build of a search that ended cleanly compiled, and a build that
	// compiled blames nothing and counts nothing.
	last := builds[len(builds)-1]
	if last.Failed || len(last.Blamed) > 0 || last.Pending != 0 {
		t.Errorf("the closing build recorded %+v, want a green build with nothing blamed", last)
	}
}

// TestReadPristineRefusesAFileTheSnapshotDoesNotHold is the first thing the
// phase does, and the one failure that is about the tree rather than about a
// mutant.
//
// Every rewrite in the phase is composed against these bytes, so a file that
// cannot be read is a file no subset of can be written -- and the phase says so
// before it builds anything rather than producing a tree it cannot explain.
func TestReadPristineRefusesAFileTheSnapshotDoesNotHold(t *testing.T) {
	t.Parallel()

	v := &validator{
		root:     t.TempDir(),
		catalog:  catalogOf(t, ""),
		byPath:   map[string][]mutation.Mutant{},
		pristine: map[string][]byte{},
		guards:   map[string]int{},
		files:    map[string]fileRef{},
	}
	err := v.readPristine()
	if got := CodeOf(err); got != CodeSourceUnreadable {
		t.Fatalf("readPristine = %q (%v), want %q", got, err, CodeSourceUnreadable)
	}
	if !strings.Contains(err.Error(), `"a.go"`) {
		t.Errorf("the failure does not name the file: %v", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the failure does not carry the read's own refusal: %v", err)
	}

	// And run carries it up as it stands: a phase that could not read the tree
	// has not instrumented anything, so there is no result to report either.
	result, err := v.run(t.Context())
	if got := CodeOf(err); got != CodeSourceUnreadable {
		t.Fatalf("run = %q (%v), want %q", got, err, CodeSourceUnreadable)
	}
	if len(result.Instrumented.FilesInstrumented) != 0 {
		t.Errorf("run reported %v instrumented beside the failure", result.Instrumented.FilesInstrumented)
	}
}

// TestInstrumentModulesRefusesAModuleNobodyGave is the catalogue and the module
// list disagreeing about which tree this is.
//
// A file whose module path names no module given to the phase has no root to be
// rewritten under and no runtime to import, and writing it under whichever
// module came first would spell a dense index into another module's activation
// array.
func TestInstrumentModulesRefusesAModuleNobodyGave(t *testing.T) {
	t.Parallel()

	v := &validator{
		root:     t.TempDir(),
		catalog:  catalogOf(t, "example.com/app", "example.com/lib"),
		modules:  []Module{{Dir: "app", Path: "example.com/app"}},
		byPath:   map[string][]mutation.Mutant{},
		pristine: map[string][]byte{},
		guards:   map[string]int{},
		files:    map[string]fileRef{},
	}
	// The catalogue names two modules and the phase was given one, so the walk
	// that assigns a root to every catalogued file has nothing to assign to the
	// second.
	for _, m := range v.catalog.Mutants() {
		key := m.ModulePath + "\x00" + m.Path
		v.files[key] = fileRef{module: m.ModulePath, path: m.Path}
		v.paths = append(v.paths, key)
	}
	err := v.instrumentModules()
	if err == nil {
		t.Fatal("instrumentModules accepted a catalogue naming a module nobody gave")
	}
	if got := CodeOf(err); got != CodeOptions && got != "" {
		t.Logf("instrumentModules = %q (%v)", got, err)
	}
}

// TestEveryStepOfASecondPassStillReportsWhatTheFirstDecided is
// [TestSearchReportsWhatItHadRejectedBeforeItStopped] at each of the five
// places a pass can stop.
//
// A search that reached its second pass has already condemned candidates in
// the first, and every `return` on the way out of the second has to carry them:
// the rejections are what a partial report is written from, and dropping them
// would throw away every build the phase had already spent.
//
// The ordinals below are the fixture's own and can be counted off it. Two files
// of six, a bad candidate in the first, and a tree that turns red for good from
// the seventh build: two writes restore the pair before the gate, the gate is
// the second build, the first file's isolation runs to the twelfth write, the
// thirteenth reinstates the second file, and the twelfth build closes the pass.
func TestEveryStepOfASecondPassStillReportsWhatTheFirstDecided(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		seam string
		n    int
	}{
		{name: "the reinstatement of the undecided file", seam: "write", n: 13},
		{name: "the build that closes the first pass", seam: "build", n: 12},
		{name: "the restore that opens the second pass", seam: "write", n: 14},
		{name: "a probe inside the second file's isolation", seam: "build", n: 13},
		{name: "the accepted subset of the second file", seam: "write", n: 24},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tree := newFakeTree(t, []fakeFile{{"a.go", 6}, {"b.go", 6}})
			tree.bad = indexSet([]int{1})
			tree.brokenFrom = 7
			v := &validator{root: posixRoot, paths: tree.paths, byPath: tree.byPath}
			builds, writes := 0, 0
			v.build = func(ctx context.Context) (verdict, error) {
				builds++
				if test.seam == "build" && builds == test.n {
					return verdict{}, errStaged
				}
				return tree.build(ctx)
			}
			v.apply = func(path string, subset []mutation.Mutant) error {
				writes++
				if test.seam == "write" && writes == test.n {
					return errStaged
				}
				return tree.apply(path, subset)
			}

			rejected, err := v.search(t.Context())
			if !errors.Is(err, errStaged) {
				t.Fatalf("search = %v, want the staged failure at %s %d", err, test.seam, test.n)
			}
			if len(rejected) == 0 {
				t.Error("the search reported no rejection, want the one the first pass condemned")
			}
		})
	}
}

// TestTheFirstBuildOfACleanTreeBlamesNothing is the green side of the blame
// rule, and it is the case where the pending set is at its largest.
//
// Blame is about a *failing* build. A green one that carried a list of files to
// look at next would tell a reader the search was about to go somewhere, and a
// pending count beside it would say how much was still undecided at a moment
// when nothing was.
func TestTheFirstBuildOfACleanTreeBlamesNothing(t *testing.T) {
	t.Parallel()

	tree := newFakeTree(t, []fakeFile{{"a.go", 6}, {"b.go", 6}})
	recorder, sink := recording(t)
	v := &validator{
		root: posixRoot, paths: tree.paths, byPath: tree.byPath,
		apply: tree.apply, build: tree.build, recorder: recorder,
	}
	if _, err := v.search(t.Context()); err != nil {
		t.Fatalf("search: %v", err)
	}

	builds := opsOf(sink, trace.ValidateOpBuild)
	if len(builds) != 1 {
		t.Fatalf("the recording holds %d builds, want the one a clean tree costs", len(builds))
	}
	if len(builds[0].Blamed) != 0 {
		t.Errorf("a build that compiled blamed %v, want nothing", builds[0].Blamed)
	}
	if builds[0].Pending != 0 {
		t.Errorf("a build that blamed nothing recorded a pending count of %d, want none", builds[0].Pending)
	}
}

// TestReportPutsBothListsInCatalogueOrder is what a caller reads a validation's
// answer out of.
//
// Both lists are the catalogue's order rather than the search's, because the
// search's order is an artefact of which file the compiler happened to name
// first -- and a report whose rejections moved between two runs over the same
// tree would be a report nobody could diff.
func TestReportPutsBothListsInCatalogueOrder(t *testing.T) {
	t.Parallel()

	catalog := catalogOf(t, "", "", "")
	mutants := catalog.Mutants()
	if len(mutants) != 3 {
		t.Fatalf("the fixture catalogue holds %d mutants, want three", len(mutants))
	}
	src := []byte("package a\n\nfunc F(count int) int { return count }\n")
	v := &validator{root: posixRoot, catalog: catalog, pristine: map[string][]byte{"a.go": src}}

	// Condemned out of order, which is what a search that reached the last
	// file first would hand over.
	rejections, accepted := v.report([]condemned{
		{mutant: mutants[2], output: "./a.go:3:1: third\n"},
		{mutant: mutants[0], output: "./a.go:3:1: first\n"},
	})
	if len(rejections) != 2 {
		t.Fatalf("report returned %d rejections, want two", len(rejections))
	}
	if rejections[0].ID != mutants[0].ID || rejections[1].ID != mutants[2].ID {
		t.Errorf("the rejections are %q and %q, want the catalogue's order",
			rejections[0].DisplayID, rejections[1].DisplayID)
	}
	if !slices.Equal(accepted, []string{mutants[1].ID}) {
		t.Errorf("accepted %v, want the one nobody condemned", accepted)
	}
	for _, r := range rejections {
		if r.Diagnostic == "" {
			t.Errorf("the rejection of %s carries no diagnostic", r.DisplayID)
		}
	}

	// Nothing condemned is every mutant accepted, as two lists rather than two
	// absences: a caller writing a report reads the length of each.
	rejections, accepted = v.report(nil)
	if rejections == nil || len(rejections) != 0 {
		t.Errorf("report of nothing condemned = %v, want an empty list", rejections)
	}
	if len(accepted) != len(mutants) {
		t.Errorf("accepted %d of %d, want all of them", len(accepted), len(mutants))
	}
}

// TestInstrumentFileCountsTheGuardsItWroteAndForgetsAFileWithNone is the state
// the phase keeps about the tree it is rewriting.
//
// A file holding no guards is not a file holding zero of them: the count is what
// [Result.Instrumented] reports, and a file listed with a zero would tell a
// caller the phase had instrumented something it had emptied.
func TestInstrumentFileCountsTheGuardsItWroteAndForgetsAFileWithNone(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const rel = "measure.go"
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(modeSource), 0o600); err != nil {
		t.Fatalf("staging the file: %v", err)
	}
	catalog, hints := modeCatalog(t, rel)
	mutants := catalog.Mutants()

	v := &validator{
		root:     dir,
		catalog:  catalog,
		hints:    hints,
		pristine: map[string][]byte{rel: []byte(modeSource)},
		guards:   map[string]int{},
		files:    map[string]fileRef{rel: {root: dir, path: rel, runtimeImport: "example.com/m/gomutants_rt"}},
	}

	// An empty subset is the pristine file, which carries no guards and is
	// therefore not a file this phase has instrumented.
	if err := v.instrumentFile(rel, nil); err != nil {
		t.Fatalf("instrumentFile with no subset: %v", err)
	}
	if _, listed := v.guards[rel]; listed {
		t.Errorf("a file with no guards is listed with %d of them", v.guards[rel])
	}

	if err := v.instrumentFile(rel, mutants); err != nil {
		t.Fatalf("instrumentFile: %v", err)
	}
	if v.guards[rel] != len(mutants) {
		t.Errorf("guards = %d, want the %d written", v.guards[rel], len(mutants))
	}

	// Source the rewriter cannot read is a failure rather than a file left
	// alone: a subset that was not written is a tree that does not hold what
	// the phase is about to build.
	v.pristine[rel] = []byte("this is not Go at all {{{")
	if err := v.instrumentFile(rel, mutants); err == nil {
		t.Error("instrumentFile accepted source it cannot parse")
	}
}

// TestEveryCataloguedFileNeedsAModuleTheCallerGave is the disagreement between
// a catalogue and a module list, caught before anything is built.
//
// A file whose module path names no module given to the phase has no root to be
// rewritten under and no runtime to import. Instrumenting it under whichever
// module came first would spell a dense index into another module's activation
// array, which is a guard that turns on somebody else's mutant -- so the phase
// refuses instead, and names the module it was not given.
func TestEveryCataloguedFileNeedsAModuleTheCallerGave(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o700); err != nil {
		t.Fatalf("staging the module: %v", err)
	}
	// The catalogue's one file belongs to a module the phase was not given,
	// and the module it was given holds nothing.
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(modeSource), 0o600); err != nil {
		t.Fatalf("staging the file: %v", err)
	}

	newValidator := func() *validator {
		return &validator{
			root:     root,
			catalog:  catalogOf(t, "example.com/lib"),
			modules:  []Module{{Dir: "app", Path: "example.com/app"}},
			byPath:   map[string][]mutation.Mutant{},
			pristine: map[string][]byte{},
			guards:   map[string]int{},
			files:    map[string]fileRef{},
		}
	}

	v := newValidator()
	if err := v.readPristine(); err != nil {
		t.Fatalf("readPristine: %v", err)
	}
	err := v.instrumentModules()
	if got := CodeOf(err); got != CodeOptions {
		t.Fatalf("instrumentModules = %q (%v), want %q", got, err, CodeOptions)
	}
	if !strings.Contains(err.Error(), `"example.com/lib"`) {
		t.Errorf("the failure does not name the module nobody gave: %v", err)
	}

	// And run carries it up rather than building a tree it could not finish
	// instrumenting.
	result, runErr := newValidator().run(t.Context())
	if got := CodeOf(runErr); got != CodeOptions {
		t.Fatalf("run = %q (%v), want %q", got, runErr, CodeOptions)
	}
	if len(result.Rejected) != 0 {
		t.Errorf("run reported %d rejections beside the failure", len(result.Rejected))
	}
}

// TestInstrumentModulesCarriesUpWhatTheRewriterRefused is the other failure of
// the same step, and it is the rewriter's rather than this package's.
//
// A module whose directory is not there cannot have a runtime written into it,
// and the message a user needs is the one the instrumenter produced -- not a
// second one this phase invented about a module list it was perfectly happy
// with.
func TestInstrumentModulesCarriesUpWhatTheRewriterRefused(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("a Windows directory mode does not refuse a write the way this test needs")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions this test uses to make a write fail")
	}

	root := t.TempDir()
	module := filepath.Join(root, "m")
	if err := os.Mkdir(module, 0o700); err != nil {
		t.Fatalf("staging the module: %v", err)
	}
	if err := os.WriteFile(filepath.Join(module, "measure.go"), []byte(modeSource), 0o600); err != nil {
		t.Fatalf("staging the file: %v", err)
	}
	catalog, hints := modeCatalog(t, "measure.go")
	v := &validator{
		root:     root,
		catalog:  catalog,
		hints:    hints,
		modules:  []Module{{Dir: "m", Path: "example.com/m"}},
		byPath:   map[string][]mutation.Mutant{},
		pristine: map[string][]byte{},
		guards:   map[string]int{},
		files:    map[string]fileRef{},
	}
	if err := v.readPristine(); err != nil {
		t.Fatalf("readPristine: %v", err)
	}
	// Read, and then closed to writing: the sources are all there and the
	// generated runtime has nowhere to go.
	if err := os.Chmod(module, 0o500); err != nil {
		t.Fatalf("closing the module to writes: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(module, 0o700) })
	if err := os.WriteFile(filepath.Join(module, "probe"), nil, 0o600); err == nil {
		t.Skip("this filesystem does not enforce the directory mode this test needs")
	}

	err := v.instrumentModules()
	if err == nil {
		t.Fatal("instrumentModules accepted a module it cannot write into")
	}
	// The instrumenter's own code, not this package's: a phase that relabelled
	// it would send a reader to look at their module list.
	if got := CodeOf(err); got == CodeOptions {
		t.Errorf("instrumentModules = %q (%v), want the rewriter's own refusal", got, err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("the failure does not carry the filesystem's own refusal: %v", err)
	}
}
