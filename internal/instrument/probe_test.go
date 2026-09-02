// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// TestProbeRuntimeGolden pins the generated probe package for the same
// three-mutant catalogue the activation runtime's fixture uses.
//
// The fixture is the whole contract of the probe half in one file: the name a
// probe form will call, the environment variable that turns recording on, the
// exit status that stands in for silence, and the header line every reader of
// the log matches against. Each of those is read somewhere else — by the
// rewriter, by the runner, by [instrument.ReadInfectionLog] — so a change to
// any of them has to arrive as a diff somebody justifies rather than as a
// consumer that quietly stops agreeing.
func TestProbeRuntimeGolden(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, sampleFile), []byte(runtimeSample))
	catalog := catalogOf(t, threeAlternatives(t, []byte(runtimeSample)))
	if catalog.Len() != 3 {
		t.Fatalf("the fixture catalogue holds %d mutants, want 3", catalog.Len())
	}

	result := probeSnapshot(t, root, catalog)
	generated := filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go")
	out := readFile(t, generated)

	golden := filepath.Join("testdata", "proberuntime.golden")
	if *updateGolden {
		writeFile(t, golden, out)
	}
	if want := readFile(t, golden); !bytes.Equal(out, want) {
		t.Errorf("the generated probe runtime does not match its fixture\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}

	if _, err := parser.ParseFile(token.NewFileSet(), generated, out, parser.SkipObjectResolution); err != nil {
		t.Errorf("the generated probe runtime does not parse: %v", err)
	}
	if !generatedMarker.Match(out) {
		t.Error("the generated probe runtime does not carry the standard generated-code marker")
	}
	if !bytes.HasPrefix(out, []byte("// SPDX-FileCopyrightText:")) {
		t.Error("the generated probe runtime does not carry an SPDX header")
	}
	for _, want := range []string{
		"package gomutants_rt\n",
		"var probeSeen [3]uint32",
		`const probeEnv = "` + instrument.ProbeEnv + `"`,
		"const probeUnavailableExit = 98",
		`const probeHeader = "gomutants-infection-v1 ` + catalog.Digest() + ` 3"`,
		"os.Exit(probeUnavailableExit)",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("the generated probe runtime does not contain %q", want)
		}
	}

	// The catalogue's IDs are the activation runtime's business and nobody
	// else's: a probe tree activates nothing, so a table from ID to index in it
	// would be a second copy of a mapping only the other runtime reads.
	for _, m := range catalog.Mutants() {
		if bytes.Contains(out, []byte(m.ID)) {
			t.Errorf("the generated probe runtime carries mutant %s's full ID, which nothing in a probe tree resolves", m.DisplayID)
		}
	}

	if got, want := exportedNames(t, generated, out), []string{"Infect"}; !equalStrings(got, want) {
		t.Errorf("the generated probe runtime exports %v, want %v", got, want)
	}
}

// TestMutantRuntimeStillExportsOnlyM is the other half of that assertion, and
// the reason the two runtimes can share a package name at all.
//
// They are generated into different snapshots, so the names never meet; what
// keeps that true is that neither package grew a second export somebody started
// depending on. The activation runtime's fixture is asserted here a second time
// on purpose: this change adds a generator beside its own, and "the mutant tree
// is byte-for-byte what it was" is the one claim that has to survive it.
func TestMutantRuntimeStillExportsOnlyM(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, sampleFile), []byte(runtimeSample))
	catalog := catalogOf(t, threeAlternatives(t, []byte(runtimeSample)))

	result := instrumentSnapshot(t, root, catalog)
	generated := filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go")
	out := readFile(t, generated)

	if got, want := exportedNames(t, generated, out), []string{"M"}; !equalStrings(got, want) {
		t.Errorf("the generated activation runtime exports %v, want %v", got, want)
	}
	if want := readFile(t, filepath.Join("testdata", "runtime.golden")); !bytes.Equal(out, want) {
		t.Errorf("the activation runtime changed\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

// TestProbeRuntimeIsGeneratedForAnEmptyCatalogue keeps the probe runtime one
// shape rather than two, for the reason the activation runtime is: a run whose
// filters selected nothing is a real case, `var probeSeen [0]uint32` is legal
// Go that no index can address, and a header claiming zero mutants would be a
// log no reader could ever accept a line of.
func TestProbeRuntimeIsGeneratedForAnEmptyCatalogue(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	catalog := catalogOf(t, nil)
	result := probeSnapshot(t, root, catalog)

	if len(result.FilesInstrumented) != 0 {
		t.Errorf("FilesInstrumented = %v, want none", result.FilesInstrumented)
	}
	out := readFile(t, filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go"))
	for _, want := range []string{
		"var probeSeen [1]uint32",
		`const probeHeader = "gomutants-infection-v1 ` + catalog.Digest() + ` 1"`,
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("an empty catalogue generated a probe runtime without %q:\n%s", want, out)
		}
	}
}

// TestProbeRuntimeWritesOneLinePerDistinctMutant runs a real instrumented
// program and reads back what it recorded.
//
// Everything the log format promises is a statement about processes rather than
// about bytes, so only processes can establish it. Four goroutines report the
// same two sites twice each and one line per site comes out, which is the
// compare-and-swap doing its job; a child process re-executing the same binary
// appends a third to the same file, which is the shape a probe pass actually
// runs in — one log per target, several test binaries writing it — and the
// proof that O_APPEND keeps two processes' lines whole. The header appearing
// once per process rather than once per file is the same fact seen from the
// other side, and it is why the reader has to accept a repeat.
func TestProbeRuntimeWritesOneLinePerDistinctMutant(t *testing.T) {
	t.Parallel()

	fixture := newProbeFixture(t)
	log := filepath.Join(t.TempDir(), "infection.log")

	stdout, stderr, code := runProbe(t, fixture.binary, instrument.ProbeEnv+"="+log)
	if code != 0 {
		t.Fatalf("the probe binary exited %d\n--- stdout ---\n%s\n--- stderr ---\n%s", code, stdout, stderr)
	}

	data := readFile(t, log)
	header := "gomutants-infection-v1 " + fixture.digest + " " + strconv.Itoa(fixture.size)
	var headers int
	var indices []string
	for _, line := range lines(data) {
		if strings.HasPrefix(line, "gomutants-infection-v1") {
			if line != header {
				t.Errorf("the log holds the header %q, want %q", line, header)
			}
			headers++
			continue
		}
		indices = append(indices, line)
	}
	// Two processes wrote this file and each wrote its header once, which is
	// the whole invariant: not once per file, and not once per report.
	if headers != 2 {
		t.Errorf("the log holds %d header lines, want 2: one from the parent process and one from the child", headers)
	}
	slices.Sort(indices)
	if want := []string{"0", "1", "2"}; !equalStrings(indices, want) {
		t.Errorf("the log holds the indices %v, want %v (each distinct mutant exactly once)", indices, want)
	}

	got, err := instrument.ReadInfectionLog(bytes.NewReader(data), fixture.digest, fixture.size)
	if err != nil {
		t.Fatalf("ReadInfectionLog over a log a real probe wrote: %v", err)
	}
	if want := []uint32{0, 1, 2}; !slices.Equal(got, want) {
		t.Errorf("ReadInfectionLog = %v, want %v", got, want)
	}
}

// TestProbeRuntimeIsSilentWithoutTheProbeVariable pins what the probe runtime
// costs a process that is not probing.
//
// The same tree is built once and run many times, so the runtime is linked into
// binaries no probe pass is watching. With nothing in the environment it must
// open nothing, write nothing, say nothing and change no exit status: a
// diagnostic on a run nobody asked to probe would be noise in somebody's test
// output, and a file appearing beside their tests would be worse.
func TestProbeRuntimeIsSilentWithoutTheProbeVariable(t *testing.T) {
	t.Parallel()

	fixture := newProbeFixture(t)
	// Nothing is written here; the directory is watched precisely because a
	// runtime that invented a path would have to put the file somewhere.
	quiet := t.TempDir()

	stdout, stderr, code := runProbe(t, fixture.binary)
	if code != 0 {
		t.Errorf("the probe binary exited %d with no log to write\n--- stderr ---\n%s", code, stderr)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("the probe binary was not silent\n--- stdout ---\n%s\n--- stderr ---\n%s", stdout, stderr)
	}
	entries, err := os.ReadDir(quiet)
	if err != nil {
		t.Fatalf("reading %s: %v", quiet, err)
	}
	if len(entries) != 0 {
		t.Errorf("a run with no probe variable created %d files", len(entries))
	}
}

// TestProbeRuntimeExitsWhenTheLogCannotBeOpened is the reason the exit status
// exists at all.
//
// A probe that cannot write its log has proved nothing, and an empty log reads
// exactly like a run in which no site was ever infected — which is the reading
// that licenses skipping tests. So the process refuses to start, with a status
// the runner can tell apart from a test failure and a diagnostic naming the
// path, because the only way to fix this is to look at that path.
func TestProbeRuntimeExitsWhenTheLogCannotBeOpened(t *testing.T) {
	t.Parallel()

	fixture := newProbeFixture(t)
	log := filepath.Join(t.TempDir(), "no-such-directory", "infection.log")

	_, stderr, code := runProbe(t, fixture.binary, instrument.ProbeEnv+"="+log)
	if code != instrument.ProbeUnavailableExit {
		t.Errorf("an unwritable log exited %d, want %d\n%s", code, instrument.ProbeUnavailableExit, stderr)
	}
	for _, want := range []string{"go-mutants", log} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the diagnostic does not mention %q:\n%s", want, stderr)
		}
	}
}

// TestInfectIsRaceFree runs Infect from many goroutines at once under the race
// detector.
//
// A probe tree is instrumented test code, and test code is concurrent: the same
// site is evaluated from several goroutines of one suite, and the first of them
// to see a difference writes the index. The compare-and-swap and the append are
// what make that safe, and the race detector is the only thing that can say so
// — a plain run of the same program would pass with a torn guard.
func TestInfectIsRaceFree(t *testing.T) {
	t.Parallel()

	toolchain := locateToolchain(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	const rel = "pkg/sample/sample.go"
	writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), []byte(runtimeSample))

	candidates := threeAlternatives(t, []byte(runtimeSample))
	for i := range candidates {
		candidates[i].Path = rel
	}
	catalog := catalogOf(t, candidates)
	result := probeSnapshot(t, root, catalog)
	writeFile(t, filepath.Join(root, filepath.FromSlash("pkg/probe/probe.go")), []byte(probePackage))
	writeFile(t, filepath.Join(root, filepath.FromSlash("pkg/probe/probe_test.go")),
		[]byte(fmt.Sprintf(probeRaceTest, result.RuntimeImport)))

	binary := filepath.Join(root, "probe.test")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	out, err := goCommand(t, toolchain, root, "test", "-race", "-c", "-o", binary, "./pkg/probe")
	if err != nil {
		if raceUnavailable(out) {
			t.Skipf("this toolchain cannot build with -race, so the guard cannot be exercised under it:\n%s", out)
		}
		t.Fatalf("building the probe test binary with -race: %v\n%s", err, out)
	}

	log := filepath.Join(t.TempDir(), "infection.log")
	stdout, stderr, code := runProbe(t, binary, instrument.ProbeEnv+"="+log)
	if code != 0 {
		t.Errorf("the race-instrumented probe test exited %d\n--- stdout ---\n%s\n--- stderr ---\n%s", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, "DATA RACE") {
		t.Errorf("the race detector reported a race in the generated probe runtime\n--- stdout ---\n%s\n--- stderr ---\n%s",
			stdout, stderr)
	}
	if got, err := instrument.ReadInfectionLog(bytes.NewReader(readFile(t, log)), catalog.Digest(), catalog.Len()); err != nil {
		t.Errorf("ReadInfectionLog after the race run: %v", err)
	} else if want := []uint32{0, 1, 2}; !slices.Equal(got, want) {
		t.Errorf("the race run recorded %v, want %v", got, want)
	}
}

// TestInstrumentInProbeModeRewritesNoFile pins the honest intermediate this
// change ships.
//
// The probe forms are not written yet, so a probe tree today is the original
// source with a runtime nobody calls. That is worth asserting rather than
// leaving implied: a mode that rewrote a file by accident would produce a tree
// whose sites are guarded and whose runtime never activates any of them, which
// is a program that looks instrumented and proves nothing.
func TestInstrumentInProbeModeRewritesNoFile(t *testing.T) {
	t.Parallel()

	in := readFile(t, filepath.Join("testdata", "comparison.input"))
	other := readFile(t, filepath.Join("testdata", "nested.input"))

	root := t.TempDir()
	writeFile(t, filepath.Join(root, sampleFile), in)
	writeFile(t, filepath.Join(root, "other.go"), other)

	catalog := catalogOf(t, candidatesFor(t, nil, in))
	result := probeSnapshot(t, root, catalog)

	if got := readFile(t, filepath.Join(root, sampleFile)); !bytes.Equal(got, in) {
		t.Errorf("the catalogued file was rewritten in probe mode:\n%s", got)
	}
	if got := readFile(t, filepath.Join(root, "other.go")); !bytes.Equal(got, other) {
		t.Errorf("an uncataloged file was rewritten in probe mode:\n%s", got)
	}
	if len(result.FilesInstrumented) != 0 || len(result.GuardsByFile) != 0 {
		t.Errorf("probe mode reported FilesInstrumented=%v GuardsByFile=%v, want neither",
			result.FilesInstrumented, result.GuardsByFile)
	}
	if got, want := result.RuntimeDir, "gomutants_rt"; got != want {
		t.Errorf("RuntimeDir = %q, want %q", got, want)
	}
	if got, want := result.RuntimeImport, testModule+"/gomutants_rt"; got != want {
		t.Errorf("RuntimeImport = %q, want %q", got, want)
	}
	generated := readFile(t, filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go"))
	if !bytes.Contains(generated, []byte("func Infect(")) {
		t.Errorf("probe mode did not generate a probe runtime:\n%s", generated)
	}

	// The zero value of the new field is the mode every existing caller passes,
	// and it has to keep producing exactly the package it always did.
	mutantRoot := t.TempDir()
	writeFile(t, filepath.Join(mutantRoot, sampleFile), []byte(runtimeSample))
	mutantResult := instrumentSnapshot(t, mutantRoot, catalogOf(t, threeAlternatives(t, []byte(runtimeSample))))
	got := readFile(t, filepath.Join(mutantRoot, mutantResult.RuntimeDir, mutantResult.RuntimeDir+".go"))
	if want := readFile(t, filepath.Join("testdata", "runtime.golden")); !bytes.Equal(got, want) {
		t.Errorf("Options with a zero Mode no longer produce the activation runtime\n--- got ---\n%s\n--- want ---\n%s",
			got, want)
	}
}

// TestProbeAndMutantRuntimesShareTheDirectoryName keeps the two trees' runtimes
// interchangeable to everything above them.
//
// They are generated into different snapshots, so one name and one collision
// rule mean an import path a caller can predict from the mode-independent
// [instrument.Result] alone — and mean that a directory the user's own tree
// already holds is stepped around identically whichever tree is being built.
func TestProbeAndMutantRuntimesShareTheDirectoryName(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name      string
		collision bool
		want      string
	}{
		{name: "fresh snapshot", want: "gomutants_rt"},
		{name: "the name is taken", collision: true, want: "gomutants_rt1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			var dirs []string
			for _, probe := range []bool{false, true} {
				root := t.TempDir()
				writeFile(t, filepath.Join(root, sampleFile), []byte(runtimeSample))
				if c.collision {
					writeFile(t, filepath.Join(root, "gomutants_rt", "theirs.go"), []byte("package theirs\n"))
				}
				catalog := catalogOf(t, threeAlternatives(t, []byte(runtimeSample)))
				if probe {
					dirs = append(dirs, probeSnapshot(t, root, catalog).RuntimeDir)
					continue
				}
				dirs = append(dirs, instrumentSnapshot(t, root, catalog).RuntimeDir)
			}
			if dirs[0] != c.want || dirs[1] != c.want {
				t.Errorf("the mutant tree chose %q and the probe tree %q, want %q for both", dirs[0], dirs[1], c.want)
			}
		})
	}
}

// probeSnapshot runs the instrumenter over a snapshot in probe mode and fails
// the test if it refuses. No hints are handed over because no file is rewritten:
// a hint answers "which form does this site take", and a probe tree has no
// sites yet.
func probeSnapshot(t *testing.T, root string, catalog *mutation.Catalog) instrument.Result {
	t.Helper()
	result, err := instrument.Instrument(instrument.Options{
		SnapshotRoot: root,
		ModulePath:   testModule,
		Catalog:      catalog,
		Mode:         instrument.ModeProbe,
	})
	if err != nil {
		t.Fatalf("Instrument in probe mode: %v", err)
	}
	return result
}

// exportedNames returns every identifier a generated package declares at the
// top level and exports, sorted.
//
// It walks the declarations rather than searching the bytes because "exactly
// one export" is a claim about the package's API, and a grep for a capital
// letter would answer a different question: a comment naming M, a string
// holding Infect, and a genuine second export all look alike to it.
func exportedNames(t *testing.T, path string, src []byte) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the generated package %s: %v", path, err)
	}
	var out []string
	keep := func(name *ast.Ident) {
		if name != nil && name.IsExported() {
			out = append(out, name.Name)
		}
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			keep(d.Name)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, name := range s.Names {
						keep(name)
					}
				case *ast.TypeSpec:
					keep(s.Name)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// A probeFixture is a built command whose runtime is a probe runtime, together
// with what a reader of its log has to be told.
type probeFixture struct {
	// binary is the built command.
	binary string
	// digest identifies the catalogue the runtime was generated from.
	digest string
	// size is how many mutants that catalogue holds.
	size int
}

// newProbeFixture instruments a mini module as a probe tree and builds a
// command against it.
//
// One build per test, as the rest of this package's toolchain-backed tests do:
// the fixtures are two files and the module has no dependencies, so a build is
// cheap and a shared one would make the tests order-dependent for nothing.
func newProbeFixture(t *testing.T) probeFixture {
	t.Helper()

	toolchain := locateToolchain(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	const rel = "pkg/sample/sample.go"
	writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), []byte(runtimeSample))

	candidates := threeAlternatives(t, []byte(runtimeSample))
	for i := range candidates {
		candidates[i].Path = rel
	}
	catalog := catalogOf(t, candidates)
	result := probeSnapshot(t, root, catalog)

	// The command is written after the pass rather than before it, because the
	// path it calls Infect through is the one the pass chose.
	writeFile(t, filepath.Join(root, filepath.FromSlash("cmd/mini/main.go")),
		[]byte(fmt.Sprintf(probeMain, result.RuntimeImport)))

	binary := filepath.Join(root, "mini")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := goCommand(t, toolchain, root, "build", "-o", binary, "./cmd/mini"); err != nil {
		t.Fatalf("building the probe fixture: %v\n%s", err, out)
	}
	return probeFixture{binary: binary, digest: catalog.Digest(), size: catalog.Len()}
}

// probeMain is the command the probe fixture builds, with the generated
// runtime's import path spliced in. It reports two sites from four goroutines,
// twice each, and re-executes itself to report a third from a second process.
const probeMain = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command mini reports the same two sites from several goroutines and a third
// from a child process, so that a test can watch one log absorb both.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sync"

	rt %q
)

// childEnv tells a re-executed copy of this command to report the one site the
// parent never touches, which is how two processes come to append to one log.
const childEnv = "MINI_PROBE_CHILD"

func main() {
	if os.Getenv(childEnv) != "" {
		rt.Infect(1)
		return
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rt.Infect(0)
			rt.Infect(2)
			rt.Infect(0)
			rt.Infect(2)
		}()
	}
	wg.Wait()

	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), childEnv+"=1")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "mini: the child process failed:", err)
		os.Exit(1)
	}
}
`

// probePackage is the package the race test lives in. A directory holding only
// an external test file is not a package the go tool will build, so the test
// needs something to be the test of.
const probePackage = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package probe exists so that the test beside it has a package to belong to.
package probe
`

// probeRaceTest is the test binary the race detector is pointed at, with the
// generated runtime's import path spliced in.
const probeRaceTest = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package probe

import (
	"sync"
	"testing"

	rt %q
)

// TestInfectFromManyGoroutines reports every site from every goroutine, so that
// each index is raced for by eight writers and exactly one of them wins.
func TestInfectFromManyGoroutines(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := uint32(0); j < 3; j++ {
				rt.Infect(j)
			}
		}()
	}
	wg.Wait()
}
`

// raceUnavailable reports whether a failed build failed because this toolchain
// or this platform cannot build with -race at all, which is a reason to skip
// rather than to fail: the guard it exercises is the same on every platform,
// and a machine without cgo can still run every other test here.
func raceUnavailable(out string) bool {
	if !strings.Contains(out, "-race") && !strings.Contains(out, "race detector") {
		return false
	}
	for _, marker := range []string{"not supported", "only supported", "requires cgo", "requires CGO_ENABLED"} {
		if strings.Contains(out, marker) {
			return true
		}
	}
	return false
}

// runProbe executes a built binary with the given environment assignments
// appended, returning its streams separately and its exit status.
//
// The streams are separate because one of these tests asserts that nothing at
// all was said, and [instrument.ProbeEnv] is stripped from the inherited
// environment first: "unset" has to mean unset even when the developer running
// the suite happens to be probing something else.
func runProbe(t *testing.T, binary string, env ...string) (string, string, int) {
	t.Helper()

	inherited := os.Environ()
	clean := make([]string, 0, len(inherited)+len(env))
	for _, assignment := range inherited {
		if strings.HasPrefix(assignment, instrument.ProbeEnv+"=") {
			continue
		}
		clean = append(clean, assignment)
	}

	cmd := exec.Command(binary)
	cmd.Env = append(clean, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	var exit *exec.ExitError
	switch {
	case err == nil:
		return stdout.String(), stderr.String(), 0
	case errors.As(err, &exit):
		return stdout.String(), stderr.String(), exit.ExitCode()
	default:
		t.Fatalf("running %s: %v", binary, err)
		return "", "", 0
	}
}
