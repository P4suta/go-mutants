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
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func TestProbeRuntimeGolden(t *testing.T) {
	t.Parallel()

	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, sampleFile), []byte(runtimeSample))
	catalog := catalogOf(t, threeAlternatives(t, []byte(runtimeSample)))
	if catalog.Len() != 3 {
		t.Fatalf("the fixture catalogue holds %d mutants, want 3", catalog.Len())
	}

	result := probeSnapshot(t, root, catalog)
	generated := filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go")
	out := testkit.ReadFile(t, generated)

	testkit.Golden(t, "proberuntime.golden", out)

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

	for _, m := range catalog.Mutants() {
		if bytes.Contains(out, []byte(m.ID)) {
			t.Errorf("the generated probe runtime carries mutant %s's full ID, which nothing in a probe tree resolves", m.DisplayID)
		}
	}

	if got, want := exportedNames(t, generated, out), []string{"Differs", "Infect"}; !equalStrings(got, want) {
		t.Errorf("the generated probe runtime exports %v, want %v", got, want)
	}
}

func TestMutantRuntimeExportsWhatItsTreeSpells(t *testing.T) {
	t.Parallel()

	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, sampleFile), []byte(runtimeSample))
	catalog := catalogOf(t, threeAlternatives(t, []byte(runtimeSample)))

	result := instrumentSnapshot(t, root, catalog)
	generated := filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go")
	out := testkit.ReadFile(t, generated)

	if got, want := exportedNames(t, generated, out), []string{"Limit", "M", "Over"}; !equalStrings(got, want) {
		t.Errorf("the generated activation runtime exports %v, want %v", got, want)
	}
	if want := testkit.ReadFile(t, filepath.Join("testdata", "runtime.golden")); !bytes.Equal(out, want) {
		t.Errorf("the activation runtime changed\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

func TestProbeRuntimeIsGeneratedForAnEmptyCatalogue(t *testing.T) {
	t.Parallel()

	root := testkit.Scratch(t)
	catalog := catalogOf(t, nil)
	result := probeSnapshot(t, root, catalog)

	if len(result.FilesInstrumented) != 0 {
		t.Errorf("FilesInstrumented = %v, want none", result.FilesInstrumented)
	}
	out := testkit.ReadFile(t, filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go"))
	for _, want := range []string{
		"var probeSeen [1]uint32",
		`const probeHeader = "gomutants-infection-v1 ` + catalog.Digest() + ` 1"`,
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("an empty catalogue generated a probe runtime without %q:\n%s", want, out)
		}
	}
}

func TestProbeRuntimeWritesOneLinePerDistinctMutant(t *testing.T) {
	t.Parallel()

	fixture := newProbeFixture(t)
	log := filepath.Join(testkit.Scratch(t), "infection.log")

	stdout, stderr, code := runProbe(t, testkit.Scratch(t), fixture.binary, instrument.ProbeEnv+"="+log)
	if code != 0 {
		t.Fatalf("the probe binary exited %d\n--- stdout ---\n%s\n--- stderr ---\n%s", code, stdout, stderr)
	}

	data := testkit.ReadFile(t, log)
	header := "gomutants-infection-v1 " + fixture.digest + " " + strconv.Itoa(fixture.mutants)
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
	if headers != 2 {
		t.Errorf("the log holds %d header lines, want 2: one from the parent process and one from the child", headers)
	}
	slices.Sort(indices)
	if want := []string{"0", "1", "2"}; !equalStrings(indices, want) {
		t.Errorf("the log holds the indices %v, want %v (each distinct mutant exactly once)", indices, want)
	}

	got, err := instrument.ReadInfectionLog(bytes.NewReader(data), fixture.digest, fixture.mutants)
	if err != nil {
		t.Fatalf("ReadInfectionLog over a log a real probe wrote: %v", err)
	}
	if want := []uint32{0, 1, 2}; !slices.Equal(got, want) {
		t.Errorf("ReadInfectionLog = %v, want %v", got, want)
	}
}

func TestProbeRuntimeIsSilentWithoutTheProbeVariable(t *testing.T) {
	t.Parallel()

	fixture := newProbeFixture(t)
	quiet := testkit.Scratch(t)

	stdout, stderr, code := runProbe(t, quiet, fixture.binary)
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

func TestProbeRuntimeExitsWhenTheLogCannotBeOpened(t *testing.T) {
	t.Parallel()

	fixture := newProbeFixture(t)
	log := filepath.Join(testkit.Scratch(t), "no-such-directory", "infection.log")

	_, stderr, code := runProbe(t, testkit.Scratch(t), fixture.binary, instrument.ProbeEnv+"="+log)
	if code != instrument.ProbeUnavailableExit {
		t.Errorf("an unwritable log exited %d, want %d\n%s", code, instrument.ProbeUnavailableExit, stderr)
	}
	for _, want := range []string{"go-mutants", log} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the diagnostic does not mention %q:\n%s", want, stderr)
		}
	}
}

func TestInfectIsRaceFree(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	const rel = "pkg/sample/sample.go"
	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), []byte(runtimeSample))

	candidates := threeAlternatives(t, []byte(runtimeSample))
	for i := range candidates {
		candidates[i].Path = rel
	}
	catalog := catalogOf(t, candidates)
	result := probeSnapshot(t, root, catalog)
	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash("pkg/probe/probe.go")), []byte(probePackage))
	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash("pkg/probe/probe_test.go")),
		[]byte(fmt.Sprintf(probeRaceTest, result.RuntimeImport)))

	binary := filepath.Join(root, "probe.test")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := goCommand(t, toolchain, root, env, "test", "-race", "-c", "-o", binary, "./pkg/probe")
	if build.ExitCode != 0 && raceUnavailable(string(build.Output)) {
		t.Skipf("this toolchain cannot build with -race, so the guard cannot be exercised under it:\n%s",
			build.Output)
	}
	mutantkit.RequireExit(t, build, 0, "building the probe test binary with -race")

	log := filepath.Join(testkit.Scratch(t), "infection.log")
	stdout, stderr, code := runProbe(t, testkit.Scratch(t), binary, instrument.ProbeEnv+"="+log)
	if code != 0 {
		t.Errorf("the race-instrumented probe test exited %d\n--- stdout ---\n%s\n--- stderr ---\n%s", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, "DATA RACE") {
		t.Errorf("the race detector reported a race in the generated probe runtime\n--- stdout ---\n%s\n--- stderr ---\n%s",
			stdout, stderr)
	}
	if got, err := instrument.ReadInfectionLog(bytes.NewReader(testkit.ReadFile(t, log)), catalog.Digest(), catalog.Len()); err != nil {
		t.Errorf("ReadInfectionLog after the race run: %v", err)
	} else if want := []uint32{0, 1, 2}; !slices.Equal(got, want) {
		t.Errorf("the race run recorded %v, want %v", got, want)
	}
}

func TestProbeModeRewritesOnlyWhereItHasAProbeForm(t *testing.T) {
	t.Parallel()

	in := testkit.ReadFile(t, filepath.Join("testdata", "comparison.input"))
	other := testkit.ReadFile(t, filepath.Join("testdata", "nested.input"))

	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, sampleFile), in)
	testkit.WriteFile(t, filepath.Join(root, "other.go"), other)

	catalog := catalogOf(t, candidatesFor(t, nil, in))
	result := probeSnapshotWith(t, root, catalog, hintOptions{unprobedSites: []string{"a > b"}})

	if got := testkit.ReadFile(t, filepath.Join(root, sampleFile)); !bytes.Equal(got, in) {
		t.Errorf("the catalogued file was rewritten in probe mode:\n%s", got)
	}
	if got := testkit.ReadFile(t, filepath.Join(root, "other.go")); !bytes.Equal(got, other) {
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
	generated := testkit.ReadFile(t, filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go"))
	if !bytes.Contains(generated, []byte("func Infect(")) {
		t.Errorf("probe mode did not generate a probe runtime:\n%s", generated)
	}

	mutantRoot := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(mutantRoot, sampleFile), []byte(runtimeSample))
	mutantResult := instrumentSnapshot(t, mutantRoot, catalogOf(t, threeAlternatives(t, []byte(runtimeSample))))
	got := testkit.ReadFile(t, filepath.Join(mutantRoot, mutantResult.RuntimeDir, mutantResult.RuntimeDir+".go"))
	if want := testkit.ReadFile(t, filepath.Join("testdata", "runtime.golden")); !bytes.Equal(got, want) {
		t.Errorf("Options with a zero Mode no longer produce the activation runtime\n--- got ---\n%s\n--- want ---\n%s",
			got, want)
	}
}

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
				root := testkit.Scratch(t)
				testkit.WriteFile(t, filepath.Join(root, sampleFile), []byte(runtimeSample))
				if c.collision {
					testkit.WriteFile(t, filepath.Join(root, "gomutants_rt", "theirs.go"), []byte("package theirs\n"))
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

func probeSnapshot(t *testing.T, root string, catalog *mutation.Catalog) instrument.Result {
	t.Helper()
	return probeSnapshotWith(t, root, catalog, hintOptions{})
}

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

type probeFixture struct {
	binary  string
	digest  string
	mutants int
}

func newProbeFixture(t *testing.T) probeFixture {
	t.Helper()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	const rel = "pkg/sample/sample.go"
	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), []byte(runtimeSample))

	candidates := threeAlternatives(t, []byte(runtimeSample))
	for i := range candidates {
		candidates[i].Path = rel
	}
	catalog := catalogOf(t, candidates)
	result := probeSnapshot(t, root, catalog)

	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash("cmd/mini/main.go")),
		[]byte(fmt.Sprintf(probeMain, result.RuntimeImport)))

	binary := filepath.Join(root, "mini")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	mutantkit.RequireExit(t, goCommand(t, toolchain, root, env, "build", "-o", binary, "./cmd/mini"),
		0, "building the probe fixture")
	return probeFixture{binary: binary, digest: catalog.Digest(), mutants: catalog.Len()}
}

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

const probePackage = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package probe exists so that the test beside it has a package to belong to.
package probe
`

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

func runProbe(t *testing.T, dir, binary string, env ...string) (string, string, int) {
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
	cmd.Dir = dir
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
