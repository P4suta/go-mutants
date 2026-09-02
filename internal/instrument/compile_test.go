// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
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
// cannot: a guard that parses and does not type, a declaration hoisted out of a
// scope something later reads, a `return` that stopped being a function's
// terminating statement, an alias that resolves to the wrong thing, a generated
// runtime that does not build. Every form's promise is that both branches
// compile in the site's own context, and this is where that promise is kept or
// broken.
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
	hints := instrument.Hints{}
	for _, fixture := range []struct {
		name string
		// candidates overrides the fixture's catalogue, exactly as it does in
		// the golden test; nil is every comparison and boolean literal.
		candidates func(*testing.T, []byte) []mutation.Candidate
		// hints are what this fixture's guard hints need beyond its syntax.
		hints hintOptions
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
		{name: "statement", candidates: statementEdits},
		{name: "declaration", candidates: declarationEdits, hints: hintOptions{declared: declaredTypes()}},
		{name: "mixedforms", candidates: mixedFormEdits},
		// The one that used to be here to prove the opposite: a named boolean
		// type was instrumented as Form C and left for the compiler to reject.
		// It is a statement site now, and this is where "and it compiles" is
		// established.
		{name: "namedbool", candidates: namedBoolEdits, hints: hintOptions{namedBool: namedBoolExprs()}},
	} {
		src := readFile(t, filepath.Join("testdata", fixture.name+".input"))
		dir := "pkg/" + fixture.name
		rel := dir + "/sample.go"
		writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), src)
		if fixture.sibling != "" {
			writeFile(t, filepath.Join(root, filepath.FromSlash(dir), fixture.sibling),
				readFile(t, filepath.Join("testdata", fixture.name+".sibling")))
		}
		here := candidatesFor(t, fixture.candidates, src)
		for i := range here {
			here[i].Path = rel
		}
		candidates = append(candidates, here...)
		// Derived per fixture and merged, because each fixture answers the
		// questions syntax cannot for itself: "true" is the universe constant
		// in one of these files and a Flag in another.
		maps.Copy(hints, hintsOfCandidates(t, rel, src, here, fixture.hints))
	}
	instrumentSnapshotHinted(t, root, catalogOf(t, candidates), hints)

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

// TestInstrumentedBinaryTakesEachStatementBranch runs a program whose three
// mutants are a declaration guard and two alternatives of one statement guard.
//
// Form C's branches are two expressions and the compiler chooses between them;
// the statement forms are a chain of `if`s that this package writes out itself,
// and a chain is exactly the shape where an off-by-one in the alternatives puts
// the wrong mutant behind a flag. Nothing about the bytes would look wrong, and
// the run would report the wrong operator as surviving. So each flag is set in
// turn and the program's answer is read back: four runs, four numbers, one of
// them the unmutated one.
//
// The empty branch is the one worth naming. A statement-deletion mutant renders
// as `if __gm.M[k] { }`, and the only proof that this means "the statement did
// not run" rather than "something else ran" is the accumulator coming back
// untouched.
func TestInstrumentedBinaryTakesEachStatementBranch(t *testing.T) {
	t.Parallel()

	toolchain := locateToolchain(t)
	const src = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package sample totals a slice, in the two statement shapes that carry a
// guard: one that declares and one that does not.
package sample

// Total sums values, starting from an accumulator it declares.
func Total(values []int) int {
	sum := 1 - 1
	for _, v := range values {
		sum = sum + v
	}
	return sum
}
`
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	rel := "pkg/sample/sample.go"
	writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), []byte(src))
	writeFile(t, filepath.Join(root, filepath.FromSlash("cmd/mini/main.go")), []byte(`// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Command mini prints one total, so that a test can watch a mutant change it.
package main

import (
	"fmt"

	"example.com/mini/pkg/sample"
)

func main() {
	fmt.Println(sample.Total([]int{1, 2, 3}))
}
`))

	candidates := editsIn(t, []byte(src),
		editSpec{rule: "sub-to-add", in: "sum := 1 - 1", find: "-", with: "+"},
		editSpec{rule: "add-to-sub", in: "sum = sum + v", find: "+", with: "-"},
		editSpec{rule: "delete-assignment", in: "sum = sum + v"},
	)
	for i := range candidates {
		candidates[i].Path = rel
	}
	catalog := catalogOf(t, candidates)
	hints := hintsOfCandidates(t, rel, []byte(src), candidates,
		hintOptions{declared: map[string]string{"sum": "int"}})
	instrumentSnapshotHinted(t, root, catalog, hints)

	binary := filepath.Join(root, "mini")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := goCommand(t, toolchain, root, "build", "-o", binary, "./cmd/mini"); err != nil {
		t.Fatalf("building the instrumented program: %v\n%s", err, out)
	}

	for _, c := range []struct {
		what        string
		rule        string
		replacement string
		want        string
	}{
		// The instrumented baseline: every guard takes the branch holding the
		// original source, so the accumulator starts at zero and gains 6.
		{what: "no mutant", want: "6"},
		// The declaration guard: `1 - 1` becomes `1 + 1`, so the same sum
		// starts at two. It is also the proof that the hoisted `var sum int`
		// and the assignment inside the guard are one variable.
		{what: "the declaration's mutant", rule: "sub-to-add", replacement: "+", want: "8"},
		// The first alternative of the statement guard: every value is
		// subtracted instead.
		{what: "the statement's operator mutant", rule: "add-to-sub", replacement: "-", want: "-6"},
		// The second: the statement does not run at all.
		{what: "the statement's deletion", rule: "delete-assignment", replacement: "", want: "0"},
	} {
		active := ""
		if c.rule != "" {
			active = mutantOf(t, catalog, c.rule, c.replacement).ID
		}
		out, code := run(t, binary, active)
		if code != 0 || strings.TrimSpace(out) != c.want {
			t.Errorf("with %s the program printed %q and exited %d, want %q and 0",
				c.what, strings.TrimSpace(out), code, c.want)
		}
	}
}

// TestUncompilableMutantsAreLeftToTheValidationPhase pins the hand-off this
// package documents: it writes the guard, and the compiler decides.
//
// The named boolean type used to be the example, because Form C evaluates to
// `bool` and a site wanting `Flag` would not take it. It is not an example any
// more: discovery hints such a site as Form S and the result compiles, which
// [TestInstrumentedTreeCompiles] is where it is established. What is left is
// the mutant that is not a program at all — `v * 0` swapped into `v / 0` is a
// constant division by zero — and the contract for it is the one that has not
// changed: nothing here type-checks anything, and the next phase compiles,
// bisects, and rejects the individual candidate with a reason a user can read.
//
// The test asserts the build fails, which reads like an odd thing to want. It
// is the honest form of the contract: if this ever starts compiling, the
// documented hand-off is wrong and both this test and the package
// documentation have to be revisited.
func TestUncompilableMutantsAreLeftToTheValidationPhase(t *testing.T) {
	t.Parallel()

	toolchain := locateToolchain(t)
	const src = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package sample multiplies by a constant that a swapped operator cannot
// divide by.
package sample

// Zero returns nothing at all, at some expense.
func Zero(v int) int {
	return v * 0
}
`
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	rel := "pkg/zero/sample.go"
	writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), []byte(src))

	candidates := editsIn(t, []byte(src), editSpec{rule: "mul-to-div", in: "v * 0", find: "*", with: "/"})
	for i := range candidates {
		candidates[i].Path = rel
	}
	// Instrumentation itself does not refuse: the rewrite is a byte edit, and
	// the guard around the statement is written exactly as it would be
	// anywhere else.
	catalog := catalogOf(t, candidates)
	instrumentSnapshotHinted(t, root, catalog, hintsOfCandidates(t, rel, []byte(src), candidates, hintOptions{}))

	out, err := goCommand(t, toolchain, root, "build", "./...")
	if err == nil {
		t.Fatalf("the instrumented tree built, so this mutant is no longer the compiler's to reject:\n%s", out)
	}
	if !strings.Contains(out, "division by zero") {
		t.Errorf("the build failed for some other reason than the mutated copy:\n%s", out)
	}
}

// mutantOf finds the catalogued mutant one rule proposed, told from the others
// by what it writes. Two rules can write the same bytes in one file, and one
// rule can appear more than once, so both halves are needed to name exactly
// one.
func mutantOf(t *testing.T, catalog *mutation.Catalog, rule, replacement string) mutation.Mutant {
	t.Helper()
	for _, m := range catalog.Mutants() {
		if m.Rule.Name == rule && m.Replacement == replacement {
			return m
		}
	}
	t.Fatalf("no catalogued mutant of rule %q writes %q", rule, replacement)
	return mutation.Mutant{}
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
	//
	// -buildvcs=false is part of the same statement rather than a workaround. A
	// fixture module lives in a temporary directory and has no version control
	// of its own, so stamping it can only ever find somebody else's repository
	// above it — and a stray or half-created .git in the temporary root then
	// decides whether these tests build, which is a fact about the machine and
	// not about the instrumented tree.
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod -buildvcs=false", "GOPROXY=off")
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
