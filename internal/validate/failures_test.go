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

var errStaged = errors.New("the machine cannot build")

func TestSearchCarriesUpAFailureFromWhereverItHappened(t *testing.T) {
	t.Parallel()

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

func searchFailsWherever(t *testing.T, files []fakeFile, bad []int, brokenFrom int) {
	t.Helper()

	counted := newFakeTree(t, files)
	counted.bad = indexSet(bad)
	counted.brokenFrom = brokenFrom
	countingValidator := &validator{
		root: posixRoot, paths: counted.paths, byPath: counted.byPath,
		apply: counted.apply, build: counted.build,
	}
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
				for _, c := range rejected {
					if c.mutant.Path == "" {
						t.Errorf("a rejection came back with no path: %+v", c)
					}
				}
			})
		}
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

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

	named = v.blame(verdict{
		failed: true,
		output: "# fixture.example/x\n./already.go:3:9: undefined: guard\n",
	}, pending)
	if !slices.Equal(named, pending) {
		t.Errorf("blame = %v, want every undecided file", named)
	}

	named = v.blame(verdict{failed: true, output: "# fixture.example/x\nsignal: killed\n"}, pending)
	if !slices.Equal(named, pending) {
		t.Errorf("blame = %v, want every undecided file", named)
	}
	named[0] = "changed.go"
	if pending[0] != "a.go" {
		t.Error("blame handed back the pending list itself")
	}

	if got := v.blame(verdict{failed: true, output: "./a.go:1:1: x\n"}, nil); len(got) != 0 {
		t.Errorf("blame with nothing pending = %v, want nothing", got)
	}
}

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
	if got := (Result{}).RuntimeDirs(); got == nil || len(got) != 0 {
		t.Errorf("RuntimeDirs of nothing = %v, want an empty list", got)
	}
}

func TestLoopsIsEveryCountedLoopOfEveryModule(t *testing.T) {
	t.Parallel()

	result := Result{Runtimes: []instrument.Result{
		{RuntimeDir: "app/gomutants_rt", Loops: 12},
		{RuntimeDir: "quiet/gomutants_rt", Loops: 0},
		{RuntimeDir: "lib/gomutants_rt", Loops: 5},
	}}
	if got, want := result.Loops(), 17; got != want {
		t.Errorf("Loops = %d, want %d: every module's loops, added up", got, want)
	}
	if got := (Result{}).Loops(); got != 0 {
		t.Errorf("Loops of nothing = %d, want 0", got)
	}
}

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
	single := &validator{
		modules: []Module{{Dir: ".", Path: "example.com/m"}},
		catalog: catalogOf(t, ""),
	}
	if _, ok = single.moduleOf(""); !ok {
		t.Error("moduleOf did not answer for a single-module tree")
	}
}

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
	if got := v.runtimeImportOf(Module{Dir: "other", Path: "example.com/other"}); got != "" {
		t.Errorf("runtimeImportOf an uninstrumented module = %q, want nothing", got)
	}
	short := &validator{modules: modules, runtimes: v.runtimes[:1]}
	if got := short.runtimeImportOf(modules[1]); got != "" {
		t.Errorf("runtimeImportOf past the runtimes = %q, want nothing", got)
	}
}

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

	if line, column := position(nil, 0); line != 1 || column != 1 {
		t.Errorf("position of an empty file = %d:%d, want 1:1", line, column)
	}
}

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

	if named := (&validator{}).result(); named.Instrumented.RuntimeDir != "" {
		t.Errorf("the merged view of nothing = %+v", named.Instrumented)
	}
}

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

func mustSpan(t *testing.T, start, end uint32) mutation.Span {
	t.Helper()
	span, err := mutation.NewSpan(start, end)
	if err != nil {
		t.Fatalf("NewSpan(%d, %d): %v", start, end, err)
	}
	return span
}

func TestBuildArgsCarryEveryPackageTheCallerNamed(t *testing.T) {
	t.Parallel()

	packages := []string{"./a/...", "./b/...", "./c/...", "./d/...", "./e/...", "./f/...", "./g/..."}
	args := buildArgs(4, packages)
	if got := args[len(args)-len(packages):]; !slices.Equal(got, packages) {
		t.Errorf("buildArgs ended with %v, want %v", got, packages)
	}
	if !slices.Contains(args, "-p") {
		t.Errorf("buildArgs = %v, want the job count in it", args)
	}

	args = buildArgs(0, nil)
	if slices.Contains(args, "-p") {
		t.Errorf("buildArgs = %v, want no job count when none was chosen", args)
	}
	if args[len(args)-1] != "./..." {
		t.Errorf("buildArgs = %v, want the whole tree when no package was named", args)
	}
}

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

func TestSearchSaysSoWhenEveryFileHasBeenIsolatedAndTheTreeStillFails(t *testing.T) {
	t.Parallel()

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
	if tree.builds < 6 {
		t.Errorf("the search spent %d builds, want a second pass among them", tree.builds)
	}
}

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
	last := builds[len(builds)-1]
	if last.Failed || len(last.Blamed) > 0 || last.Pending != 0 {
		t.Errorf("the closing build recorded %+v, want a green build with nothing blamed", last)
	}
}

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

	result, err := v.run(t.Context())
	if got := CodeOf(err); got != CodeSourceUnreadable {
		t.Fatalf("run = %q (%v), want %q", got, err, CodeSourceUnreadable)
	}
	if len(result.Instrumented.FilesInstrumented) != 0 {
		t.Errorf("run reported %v instrumented beside the failure", result.Instrumented.FilesInstrumented)
	}
}

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

func TestReportPutsBothListsInCatalogueOrder(t *testing.T) {
	t.Parallel()

	catalog := catalogOf(t, "", "", "")
	mutants := catalog.Mutants()
	if len(mutants) != 3 {
		t.Fatalf("the fixture catalogue holds %d mutants, want three", len(mutants))
	}
	src := []byte("package a\n\nfunc F(count int) int { return count }\n")
	v := &validator{root: posixRoot, catalog: catalog, pristine: map[string][]byte{"a.go": src}}

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

	rejections, accepted = v.report(nil)
	if rejections == nil || len(rejections) != 0 {
		t.Errorf("report of nothing condemned = %v, want an empty list", rejections)
	}
	if len(accepted) != len(mutants) {
		t.Errorf("accepted %d of %d, want all of them", len(accepted), len(mutants))
	}
}

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

	v.pristine[rel] = []byte("this is not Go at all {{{")
	if err := v.instrumentFile(rel, mutants); err == nil {
		t.Error("instrumentFile accepted source it cannot parse")
	}
}

func TestEveryCataloguedFileNeedsAModuleTheCallerGave(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o700); err != nil {
		t.Fatalf("staging the module: %v", err)
	}
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

	result, runErr := newValidator().run(t.Context())
	if got := CodeOf(runErr); got != CodeOptions {
		t.Fatalf("run = %q (%v), want %q", got, runErr, CodeOptions)
	}
	if len(result.Rejected) != 0 {
		t.Errorf("run reported %d rejections beside the failure", len(result.Rejected))
	}
}

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
	if got := CodeOf(err); got == CodeOptions {
		t.Errorf("instrumentModules = %q (%v), want the rewriter's own refusal", got, err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("the failure does not carry the filesystem's own refusal: %v", err)
	}
}
