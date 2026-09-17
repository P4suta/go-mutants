// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"maps"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/runner"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

const goModule = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

module example.com/mini

go 1.21
`

func TestInstrumentedTreeCompiles(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, "go.mod"), []byte(goModule))

	var candidates []mutation.Candidate
	hints := instrument.Hints{}
	for _, fixture := range []struct {
		name       string
		candidates func(*testing.T, []byte) []mutation.Candidate
		hints      hintOptions
		sibling    string
	}{
		{name: "comparison"}, {name: "boolliteral"}, {name: "alternatives"},
		{name: "nested"}, {name: "multiline"}, {name: "unicode"},
		{name: "aliascollision"}, {name: "siblingalias", sibling: "sibling.go"},
		{name: "statement", candidates: statementEdits},
		{name: "declaration", candidates: declarationEdits, hints: hintOptions{declared: declaredTypes()}},
		{name: "mixedforms", candidates: mixedFormEdits},
		{name: "namedbool", candidates: namedBoolEdits, hints: hintOptions{namedBool: namedBoolExprs()}},
	} {
		src := testkit.ReadFile(t, filepath.Join("testdata", fixture.name+".input"))
		dir := "pkg/" + fixture.name
		rel := dir + "/sample.go"
		testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), src)
		if fixture.sibling != "" {
			testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash(dir), fixture.sibling),
				testkit.ReadFile(t, filepath.Join("testdata", fixture.name+".sibling")))
		}
		here := candidatesFor(t, fixture.candidates, src)
		for i := range here {
			here[i].Path = rel
		}
		candidates = append(candidates, here...)
		maps.Copy(hints, hintsOfCandidates(t, rel, src, here, fixture.hints))
	}
	instrumentSnapshotHinted(t, root, catalogOf(t, candidates), hints)

	build := goCommand(t, toolchain, root, env, "build", "./...")
	mutantkit.RequireExit(t, build, 0, "`go build ./...` over the instrumented fixtures")
}

func TestInstrumentedBinaryActivatesOneMutant(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash("pkg/sample/sample.go")), []byte(runtimeSample))
	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash("cmd/mini/main.go")), []byte(`// SPDX-FileCopyrightText: 2026 go-mutants contributors
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
	mutantkit.RequireExit(t, goCommand(t, toolchain, root, env, "build", "-o", binary, "./cmd/mini"),
		0, "building the instrumented program")

	if out, code := run(t, env, binary, ""); code != 0 || strings.TrimSpace(out) != "true" {
		t.Errorf("the instrumented baseline printed %q and exited %d, want \"true\" and 0", out, code)
	}

	flip := mutantWithReplacement(t, catalog, "==")
	if out, code := run(t, env, binary, flip.ID); code != 0 || strings.TrimSpace(out) != "false" {
		t.Errorf("mutant %s printed %q and exited %d, want \"false\" and 0", flip.DisplayID, out, code)
	}

	stale := strings.Repeat("0", len(flip.ID))
	out, code := run(t, env, binary, stale)
	if code != 97 {
		t.Errorf("an unknown mutant id exited %d, want 97", code)
	}
	for _, want := range []string{"go-mutants", stale, "stale"} {
		if !strings.Contains(out, want) {
			t.Errorf("the unknown-mutant diagnostic does not mention %q:\n%s", want, out)
		}
	}
}

func TestInstrumentedBinaryTakesEachStatementBranch(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
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
	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	rel := "pkg/sample/sample.go"
	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), []byte(src))
	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash("cmd/mini/main.go")), []byte(`// SPDX-FileCopyrightText: 2026 go-mutants contributors
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
	mutantkit.RequireExit(t, goCommand(t, toolchain, root, env, "build", "-o", binary, "./cmd/mini"),
		0, "building the instrumented program")

	for _, c := range []struct {
		what        string
		rule        string
		replacement string
		want        string
	}{
		{what: "no mutant", want: "6"},
		{what: "the declaration's mutant", rule: "sub-to-add", replacement: "+", want: "8"},
		{what: "the statement's operator mutant", rule: "add-to-sub", replacement: "-", want: "-6"},
		{what: "the statement's deletion", rule: "delete-assignment", replacement: "", want: "0"},
	} {
		active := ""
		if c.rule != "" {
			active = mutantOf(t, catalog, c.rule, c.replacement).ID
		}
		out, code := run(t, env, binary, active)
		if code != 0 || strings.TrimSpace(out) != c.want {
			t.Errorf("with %s the program printed %q and exited %d, want %q and 0",
				c.what, strings.TrimSpace(out), code, c.want)
		}
	}
}

func TestUncompilableMutantsAreLeftToTheValidationPhase(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	const src = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// divide by.
package sample

// Zero returns nothing at all, at some expense.
func Zero(v int) int {
	return v * 0
}
`
	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	rel := "pkg/zero/sample.go"
	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), []byte(src))

	candidates := editsIn(t, []byte(src), editSpec{rule: "mul-to-div", in: "v * 0", find: "*", with: "/"})
	for i := range candidates {
		candidates[i].Path = rel
	}
	catalog := catalogOf(t, candidates)
	instrumentSnapshotHinted(t, root, catalog, hintsOfCandidates(t, rel, []byte(src), candidates, hintOptions{}))

	build := goCommand(t, toolchain, root, env, "build", "./...")
	if build.ExitCode == 0 {
		t.Fatalf("the instrumented tree built, so this mutant is no longer the compiler's to reject:\n%s",
			build.Output)
	}
	if !strings.Contains(string(build.Output), "division by zero") {
		t.Errorf("the build failed for some other reason than the mutated copy:\n%s", build.Output)
	}
}

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

func goCommand(t *testing.T, toolchain gocmd.Toolchain, dir string, env []string, verb string, args ...string) runner.Result {
	t.Helper()
	return mutantkit.RunGo(t, toolchain, dir, env,
		append([]string{verb, "-mod=mod", "-buildvcs=false"}, args...)...)
}

func run(t *testing.T, env []string, binary, active string) (string, int) {
	t.Helper()
	result := testkit.Exec(t, filepath.Dir(binary), mutantkit.Activate(env, active), binary)
	switch {
	case result.Err != nil:
		t.Fatalf("running %s: %v", binary, result.Err)
	case result.TimedOut:
		t.Fatalf("%s did not finish within its deadline:\n%s", binary, result.Output)
	}
	return string(result.Output), result.ExitCode
}
