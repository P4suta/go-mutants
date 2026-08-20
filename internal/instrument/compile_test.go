// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// goModule is the module file every fixture module gets. The `go` directive is
// deliberately older than anything these tests need, so the fixtures build
// under whatever toolchain is on PATH rather than only under the pinned one.
const goModule = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

module example.com/mini

go 1.21
`

// TestInstrumentedTreeCompiles builds every golden fixture, instrumented,
// against a real toolchain.
//
// The goldens prove the bytes are what they were yesterday; only the compiler
// can prove they are Go. It is the check that catches what a byte-exact fixture
// cannot: a guard that parses and does not type, an alias that resolves to the
// wrong thing, a generated runtime that does not build. Form C's promise is
// that both branches compile in the site's own type context, and this is where
// that promise is kept or broken.
//
// One fixture is two files in one package rather than one file alone. An import
// alias that collides with a package-level declaration in a sibling file is a
// redeclaration error and not a shadow, so it is invisible to a parse, to a
// golden file, and to any fixture whose package holds a single file — the shape
// has to exist here or nothing ever compiles it.
func TestInstrumentedTreeCompiles(t *testing.T) {
	t.Parallel()

	toolchain := locateToolchain(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), []byte(goModule))

	var candidates []mutation.Candidate
	for _, fixture := range []struct {
		name string
		// sibling is the file the fixture's ".sibling" half is written to,
		// inside the same package directory. It is catalogued nowhere and
		// exists to be compiled beside the instrumented file: a package block
		// spans every file of a package, so it is the only shape in which a
		// clashing import alias can be caught by a compiler at all.
		sibling string
	}{
		{name: "comparison"}, {name: "boolliteral"}, {name: "alternatives"},
		{name: "nested"}, {name: "multiline"}, {name: "unicode"},
		{name: "aliascollision"}, {name: "siblingalias", sibling: "sibling.go"},
	} {
		src := readFile(t, filepath.Join("testdata", fixture.name+".input"))
		dir := "pkg/" + fixture.name
		rel := dir + "/sample.go"
		writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), src)
		if fixture.sibling != "" {
			writeFile(t, filepath.Join(root, filepath.FromSlash(dir), fixture.sibling),
				readFile(t, filepath.Join("testdata", fixture.name+".sibling")))
		}
		for _, candidate := range candidatesIn(t, src) {
			candidate.Path = rel
			candidates = append(candidates, candidate)
		}
	}
	instrumentSnapshot(t, root, catalogOf(t, candidates))

	if out, err := goCommand(t, toolchain, root, "build", "./..."); err != nil {
		t.Errorf("the instrumented tree does not build: %v\n%s", err, out)
	}
}

// TestInstrumentedBinaryActivatesOneMutant runs an instrumented program three
// times and watches the dispatch work.
//
// This is the whole mechanism end to end in one test: with nothing in the
// environment the program behaves exactly as its author wrote it, with a mutant
// ID it behaves as that one mutant, and with an ID this tree does not contain
// it refuses to run at all. The last is the one that protects a mutation score
// — a stale catalogue that activated nothing would report survivors for mutants
// that were never live — and it is why the refusal is an exit status the runner
// can recognise rather than a warning.
func TestInstrumentedBinaryActivatesOneMutant(t *testing.T) {
	t.Parallel()

	toolchain := locateToolchain(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	writeFile(t, filepath.Join(root, filepath.FromSlash("pkg/sample/sample.go")), []byte(runtimeSample))
	writeFile(t, filepath.Join(root, filepath.FromSlash("cmd/mini/main.go")), []byte(`// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command mini prints one comparison, so that a test can watch a mutant
// change its answer.
package main

import (
	"fmt"

	"example.com/mini/pkg/sample"
)

func main() {
	fmt.Println(sample.Less(1, 2))
}
`))

	candidates := threeAlternatives(t, []byte(runtimeSample))
	for i := range candidates {
		candidates[i].Path = "pkg/sample/sample.go"
	}
	catalog := catalogOf(t, candidates)
	instrumentSnapshot(t, root, catalog)

	binary := filepath.Join(root, "mini")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := goCommand(t, toolchain, root, "build", "-o", binary, "./cmd/mini"); err != nil {
		t.Fatalf("building the instrumented program: %v\n%s", err, out)
	}

	// The instrumented baseline: every guard takes the branch holding the
	// original source, so `1 < 2` is still true.
	if out, code := run(t, binary, ""); code != 0 || strings.TrimSpace(out) != "true" {
		t.Errorf("the instrumented baseline printed %q and exited %d, want \"true\" and 0", out, code)
	}

	// The mutant that rewrites "<" as "==", which turns the same comparison
	// false. Selecting it by its replacement rather than by index keeps the
	// test readable when the catalogue's order changes.
	flip := mutantWithReplacement(t, catalog, "==")
	if out, code := run(t, binary, flip.ID); code != 0 || strings.TrimSpace(out) != "false" {
		t.Errorf("mutant %s printed %q and exited %d, want \"false\" and 0", flip.DisplayID, out, code)
	}

	// A mutant this tree does not contain.
	stale := strings.Repeat("0", len(flip.ID))
	out, code := run(t, binary, stale)
	if code != 97 {
		t.Errorf("an unknown mutant id exited %d, want 97", code)
	}
	for _, want := range []string{"go-mutants", stale, "stale"} {
		if !strings.Contains(out, want) {
			t.Errorf("the unknown-mutant diagnostic does not mention %q:\n%s", want, out)
		}
	}
}

// TestNamedBooleanTypesAreLeftToTheValidationPhase pins the one shape this
// package knowingly instruments into source that does not compile.
//
// Form C evaluates to `bool`, and a site whose context wants a named boolean
// type will not take it. Deciding that here would mean type-checking every file
// to ask a question the compiler answers for free, so the guard is written and
// the next phase — compile, bisect, reject the individual candidate — is what
// removes it, with a reason the user can read.
//
// The test asserts the build fails, which reads like an odd thing to want. It
// is the honest form of the contract: if this ever starts compiling, the
// documented hand-off is wrong and both this test and the package
// documentation have to be revisited.
func TestNamedBooleanTypesAreLeftToTheValidationPhase(t *testing.T) {
	t.Parallel()

	toolchain := locateToolchain(t)
	const src = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package sample returns a named boolean type, which Form C cannot produce.
package sample

// Flag is a boolean type of its own, so a plain bool is not assignable to it.
type Flag bool

// Enabled reports whether name opts in.
func Enabled(name string) Flag {
	if name == "" {
		return false
	}
	return true
}
`
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	rel := "pkg/named/sample.go"
	writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), []byte(src))

	candidates := candidatesIn(t, []byte(src))
	for i := range candidates {
		candidates[i].Path = rel
	}
	// Instrumentation itself does not refuse: the rewrite is a byte edit, and
	// the guard around each boolean literal is written exactly as it would be
	// anywhere else.
	instrumentSnapshot(t, root, catalogOf(t, candidates))

	out, err := goCommand(t, toolchain, root, "build", "./...")
	if err == nil {
		t.Fatalf("the instrumented tree built, so Form C now produces a named boolean type:\n%s", out)
	}
	if !strings.Contains(out, "Flag") {
		t.Errorf("the build failed for some other reason than the named boolean type:\n%s", out)
	}
}

// mutantWithReplacement finds the catalogued mutant that writes replacement.
func mutantWithReplacement(t *testing.T, catalog *mutation.Catalog, replacement string) mutation.Mutant {
	t.Helper()
	for _, m := range catalog.Mutants() {
		if m.Replacement == replacement {
			return m
		}
	}
	t.Fatalf("no catalogued mutant writes %q", replacement)
	return mutation.Mutant{}
}

// locateToolchain finds the Go toolchain, skipping the test when there is
// none. A machine without a toolchain can still run every other test in this
// package: the instrumenter itself needs no go command.
func locateToolchain(t *testing.T) gocmd.Toolchain {
	t.Helper()
	toolchain, err := gocmd.Locate(gocmd.Options{})
	if err != nil {
		t.Skipf("no Go toolchain on PATH, so the instrumented tree cannot be built: %v", err)
	}
	return toolchain
}

// goCommand runs one go command in the snapshot and returns its combined
// output.
func goCommand(t *testing.T, toolchain gocmd.Toolchain, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(toolchain.GoBin, args...)
	cmd.Dir = dir
	// GOWORK and GOFLAGS are pinned so that whatever the developer's shell is
	// configured with cannot decide what this build resolves against, and
	// GOPROXY is off because a fixture module with no dependencies must never
	// reach the network to build.
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// run executes the instrumented binary with one mutant activated, returning
// its combined output and exit status.
func run(t *testing.T, binary, active string) (string, int) {
	t.Helper()
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "GO_MUTANTS_ACTIVE="+active)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &exit):
		return out.String(), exit.ExitCode()
	default:
		t.Fatalf("running %s: %v", binary, err)
		return "", 0
	}
}
