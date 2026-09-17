// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
	"github.com/P4suta/go-mutants/internal/testkit"
	"github.com/P4suta/go-mutants/internal/testkit/mutantkit"
)

func probeSnapshotHinted(
	t *testing.T,
	root string,
	catalog *mutation.Catalog,
	hints instrument.Hints,
) instrument.Result {
	t.Helper()

	result, err := instrument.Instrument(instrument.Options{
		SnapshotRoot: root,
		ModulePath:   testModule,
		Catalog:      catalog,
		Hints:        hints,
		Mode:         instrument.ModeProbe,
	})
	if err != nil {
		t.Fatalf("Instrument in probe mode: %v", err)
	}
	return result
}

func probeSnapshotWith(
	t *testing.T,
	root string,
	catalog *mutation.Catalog,
	opts hintOptions,
) instrument.Result {
	t.Helper()
	return probeSnapshotHinted(t, root, catalog, hintsFor(t, root, catalog, opts))
}

type probeCase struct {
	name       string
	input      string
	candidates func(*testing.T, []byte) []mutation.Candidate
	hints      hintOptions
	sites      int
	extra      func(t *testing.T, in, out []byte)
}

func probeCases() []probeCase {
	return []probeCase{{
		name:       "probe-reach",
		input:      "reach.input",
		candidates: probeReachEdits,
		hints:      hintOptions{valueTypes: map[string]string{"total + i": "int"}},
		sites:      3,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "{ __gm.Infect(0); note(i) }")
			assertContains(t, out, "{ __gm.Infect(1); total = func() int { var __gm_r0 int = (total + i);"+
				" if __gm_r0 != (total-i) { __gm.Infect(2) }; return __gm_r0 }() }")
		},
	}, {
		name:       "probe-value",
		input:      "value.input",
		candidates: probeValueEdits,
		hints: hintOptions{
			declared:   map[string]string{"k": "Kind", "total": "int"},
			valueTypes: map[string]string{"Kind(a + b)": "Kind", "a*b + a": "int"},
		},
		sites: 2,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "func() Kind { var __gm_r0 Kind = (Kind(a + b));")
			assertContains(t, out, "var __gm_r0 int = (a*b + a);"+
				" if __gm_r0 != (a/b+a) { __gm.Infect(1) };"+
				" if __gm_r0 != (a*b-a) { __gm.Infect(2) }; return __gm_r0 }()")
			if bytes.Contains(out, []byte(".M[")) {
				t.Errorf("the probe tree reads an activation flag:\n%s", out)
			}
		},
	}, {
		name:       "probe-bool",
		input:      "bool.input",
		candidates: probeBoolEdits,
		sites:      4,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "__gm.Differs(1, "+
				"(__gm.Differs(0, (v > lo), (v>=lo)) && __gm.Differs(2, (v < hi), (v<=hi))), "+
				"(v>lo||v<hi))")
			assertContains(t, out, "__gm.Differs(4, __gm.Differs(3, (a == b), (!(a==b))), (a!=b))")
			if bytes.Contains(out, []byte(".M[")) {
				t.Errorf("the probe tree reads an activation flag:\n%s", out)
			}
		},
	}, {
		name:       "probe-statement",
		input:      "statement.input",
		candidates: probeStatementEdits,
		sites:      2,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "{ var __gm_r0 int = count; var __gm_r1 error = err; "+
				"if __gm_r0 != 0 { __gm.Infect(0) }; if __gm_r1 != nil { __gm.Infect(1) }; "+
				"return __gm_r0, __gm_r1 }")
			assertContains(t, out, "{ var __gm_r0 int = total; if __gm_r0 != 0 { __gm.Infect(2) }; return __gm_r0 }")
			if bytes.Contains(out, []byte(".M[")) {
				t.Errorf("the probe tree reads an activation flag:\n%s", out)
			}
		},
	}, {
		name:       "probe-namedbool",
		input:      "namedbool.input",
		candidates: probeNamedBoolEdits,
		hints:      hintOptions{namedBool: namedBoolExprs()},
		sites:      2,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "{ var __gm_r0 Flag = x>y; if __gm_r0 != true { __gm.Infect(0) }; "+
				"if __gm_r0 != false { __gm.Infect(1) }; return __gm_r0 }")
		},
	}, {
		name:       "probe-multiline",
		input:      "multiline.input",
		candidates: probeMultilineEdits,
		sites:      2,
		extra: func(t *testing.T, in, out []byte) {
			assertLinesUntouched(t, in, out, 5, 10, 11, 12, 18, 19)
			assertContains(t, out, "var __gm_r0 bool = x<=limit;")
		},
	}, {
		name:       "probe-typed",
		input:      "probe-typed.input",
		candidates: probeTypedEdits,
		sites:      3,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "{ var __gm_r0 float32 = 1; if __gm_r0 != 0 { __gm.Infect(0) }; return __gm_r0 }")
			assertContains(t, out, "{ var __gm_r0 Level = 1; if __gm_r0 != 0 { __gm.Infect(1) }; return __gm_r0 }")
			assertContains(t, out, `{ var __gm_r0 string = s; if __gm_r0 != "" { __gm.Infect(2) }; return __gm_r0 }`)
		},
	}, {
		name:       "probe-nested",
		input:      "probe-nested.input",
		candidates: probeNestedEdits,
		sites:      2,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "var __gm_r0 func() int = func()int{{var __gm_r0 int=a+b;"+
				"if __gm_r0!=0{__gm.Infect(0)};return __gm_r0};};")
			assertContains(t, out, "if __gm_r1 != nil { __gm.Infect(1) }; return __gm_r0, __gm_r1 }")
		},
	}, {
		name:       "probe-names",
		input:      "probe-names.input",
		candidates: probeNameEdits,
		sites:      1,
		extra: func(t *testing.T, _, out []byte) {
			assertContains(t, out, "{ var __gm_r1_0 int = a; var __gm_r1_1 int = __gm_r0; "+
				"if __gm_r1_0 != 0 { __gm.Infect(0) }; return __gm_r1_0, __gm_r1_1 }")
			if bytes.Contains(out, []byte("var __gm_r0 int =")) || bytes.Contains(out, []byte("var __gm_r1 int =")) {
				t.Errorf("the rewrite declared a temporary the file had already bound:\n%s", out)
			}
		},
	}}
}

func TestProbeGolden(t *testing.T) {
	t.Parallel()

	for _, c := range probeCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := testkit.ReadFile(t, filepath.Join("testdata", c.input))
			root := testkit.Scratch(t)
			testkit.WriteFile(t, filepath.Join(root, sampleFile), in)

			catalog := catalogOf(t, c.candidates(t, in))
			result := probeSnapshotWith(t, root, catalog, c.hints)
			out := testkit.ReadFile(t, filepath.Join(root, sampleFile))

			testkit.Golden(t, c.name+".golden", out)

			assertProbeWellFormed(t, in, out, catalog)
			if got := result.GuardsByFile[sampleFile]; got != c.sites {
				t.Errorf("GuardsByFile[%s] = %d, want %d probe sites", sampleFile, got, c.sites)
			}
			if got := result.FilesInstrumented; len(got) != 1 || got[0] != sampleFile {
				t.Errorf("FilesInstrumented = %v, want [%s]", got, sampleFile)
			}
			if c.extra != nil {
				c.extra(t, in, out)
			}
		})
	}
}

func TestProbeTreePreservesLines(t *testing.T) {
	t.Parallel()

	for _, c := range probeCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := testkit.ReadFile(t, filepath.Join("testdata", c.input))
			root := testkit.Scratch(t)
			testkit.WriteFile(t, filepath.Join(root, sampleFile), in)

			catalog := catalogOf(t, c.candidates(t, in))
			hints := hintsFor(t, root, catalog, c.hints)
			probeSnapshotHinted(t, root, catalog, hints)
			out := testkit.ReadFile(t, filepath.Join(root, sampleFile))

			if got, want := instrument.CountLines(out), instrument.CountLines(in); got != want {
				t.Fatalf("the probe tree holds %d line breaks, the original holds %d", got, want)
			}
			assertLinesUntouched(t, in, out, probeWrittenLines(t, in, out, catalog, hints)...)
		})
	}
}

func probeWrittenLines(
	t *testing.T,
	in, out []byte,
	catalog *mutation.Catalog,
	hints instrument.Hints,
) []int {
	t.Helper()

	written := make(map[int]bool)
	for _, m := range catalog.Mutants() {
		site := hints[m.ID].Probe
		if site == nil {
			continue
		}
		first := instrument.CountLines(in[:site.Span.StartByte])
		last := instrument.CountLines(in[:site.Span.EndByte])
		for line := first; line <= last; line++ {
			written[line] = true
		}
	}
	for i, line := range lines(out) {
		if strings.Contains(line, testModule+"/gomutants_rt") {
			written[i] = true
		}
	}
	return slices.Sorted(maps.Keys(written))
}

func TestProbeModeLeavesTheMutantGoldensAlone(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name       string
		candidates func(*testing.T, []byte) []mutation.Candidate
		hints      hintOptions
		sibling    string
	}{
		{name: "comparison"}, {name: "boolliteral"},
		{name: "alternatives", candidates: everyAlternative},
		{name: "nested"}, {name: "multiline"}, {name: "unicode"},
		{name: "aliascollision"}, {name: "siblingalias", sibling: "sibling.go"},
		{name: "statement", candidates: statementEdits},
		{name: "declaration", candidates: declarationEdits, hints: hintOptions{declared: declaredTypes()}},
		{name: "mixedforms", candidates: mixedFormEdits},
		{name: "namedbool", candidates: namedBoolEdits, hints: hintOptions{namedBool: namedBoolExprs()}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := testkit.ReadFile(t, filepath.Join("testdata", c.name+".input"))
			catalog := catalogOf(t, candidatesFor(t, c.candidates, in))

			render := func(mode instrument.Mode) []byte {
				root := testkit.Scratch(t)
				testkit.WriteFile(t, filepath.Join(root, sampleFile), in)
				if c.sibling != "" {
					testkit.WriteFile(t, filepath.Join(root, c.sibling),
						testkit.ReadFile(t, filepath.Join("testdata", c.name+".sibling")))
				}
				hints := hintsFor(t, root, catalog, c.hints)
				if mode == instrument.ModeProbe {
					probeSnapshotHinted(t, root, catalog, hints)
				} else {
					instrumentSnapshotHinted(t, root, catalog, hints)
				}
				return testkit.ReadFile(t, filepath.Join(root, sampleFile))
			}

			probed := render(instrument.ModeProbe)
			if got, want := render(instrument.ModeMutant), testkit.ReadFile(t, filepath.Join("testdata", c.name+".golden")); !bytes.Equal(got, want) {
				t.Errorf("the mutant tree of %s changed\n--- got ---\n%s\n--- want ---\n%s", c.name, got, want)
			}
			if bytes.Contains(probed, []byte(".M[")) {
				t.Errorf("the probe tree of %s reads an activation flag:\n%s", c.name, probed)
			}
		})
	}
}

func TestProbeSkipsAMutantWithoutAProbeSite(t *testing.T) {
	t.Parallel()

	in := testkit.ReadFile(t, filepath.Join("testdata", "comparison.input"))
	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, sampleFile), in)

	catalog := catalogOf(t, candidatesFor(t, nil, in))
	result := probeSnapshotWith(t, root, catalog, hintOptions{unprobedSites: []string{"a > b"}})

	if got := testkit.ReadFile(t, filepath.Join(root, sampleFile)); !bytes.Equal(got, in) {
		t.Errorf("a file whose every mutant is unprobed was rewritten:\n%s", got)
	}
	if len(result.FilesInstrumented) != 0 || len(result.GuardsByFile) != 0 {
		t.Errorf("probe mode reported FilesInstrumented=%v GuardsByFile=%v, want neither",
			result.FilesInstrumented, result.GuardsByFile)
	}
}

func TestProbeRefusesAnUnspellableResultType(t *testing.T) {
	t.Parallel()

	in := testkit.ReadFile(t, filepath.Join("testdata", "statement.input"))
	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, sampleFile), in)

	catalog := catalogOf(t, probeStatementEdits(t, in))
	result := probeSnapshotWith(t, root, catalog, hintOptions{unprobed: []string{"return count, err"}})
	out := testkit.ReadFile(t, filepath.Join(root, sampleFile))

	if got := result.GuardsByFile[sampleFile]; got != 1 {
		t.Errorf("GuardsByFile[%s] = %d, want 1: only the spellable statement is probed", sampleFile, got)
	}
	assertContains(t, out, "\treturn count, err\n")
	assertContains(t, out, "{ var __gm_r0 int = total; if __gm_r0 != 0 { __gm.Infect(2) }; return __gm_r0 }")
	for _, index := range []string{"Infect(0)", "Infect(1)"} {
		if bytes.Contains(out, []byte(index)) {
			t.Errorf("the refused statement was probed anyway: the file holds %s\n%s", index, out)
		}
	}
}

func TestProbeIsDeterministic(t *testing.T) {
	t.Parallel()

	for _, c := range probeCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := testkit.ReadFile(t, filepath.Join("testdata", c.input))
			catalog := catalogOf(t, c.candidates(t, in))

			run := func() (string, []byte, []byte) {
				root := testkit.Scratch(t)
				testkit.WriteFile(t, filepath.Join(root, sampleFile), in)
				result := probeSnapshotWith(t, root, catalog, c.hints)
				runtime := testkit.ReadFile(t, filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go"))
				return result.RuntimeImport, testkit.ReadFile(t, filepath.Join(root, sampleFile)), runtime
			}

			firstImport, firstSource, firstRuntime := run()
			secondImport, secondSource, secondRuntime := run()

			if firstImport != secondImport {
				t.Errorf("runtime import differs between runs: %q and %q", firstImport, secondImport)
			}
			if !bytes.Equal(firstSource, secondSource) {
				t.Errorf("the probe tree differs between runs\n--- first ---\n%s\n--- second ---\n%s",
					firstSource, secondSource)
			}
			if !bytes.Equal(firstRuntime, secondRuntime) {
				t.Errorf("the generated probe runtime differs between runs\n--- first ---\n%s\n--- second ---\n%s",
					firstRuntime, secondRuntime)
			}
		})
	}
}

func TestProbeTreeCompiles(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, "go.mod"), []byte(goModule))

	var candidates []mutation.Candidate
	hints := instrument.Hints{}
	for _, c := range probeCases() {
		src := testkit.ReadFile(t, filepath.Join("testdata", c.input))
		rel := "pkg/" + c.name + "/sample.go"
		testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), src)

		here := c.candidates(t, src)
		for i := range here {
			here[i].Path = rel
		}
		candidates = append(candidates, here...)
		maps.Copy(hints, hintsOfCandidates(t, rel, src, here, c.hints))
	}
	probeSnapshotHinted(t, root, catalogOf(t, candidates), hints)

	mutantkit.RequireExit(t, goCommand(t, toolchain, root, env, "build", "./..."),
		0, "`go build ./...` over the probe tree")
}

func TestProbeCapturesEveryResultOfAReturn(t *testing.T) {
	t.Parallel()

	toolchain := mutantkit.Toolchain(t)
	env := testkit.Compose(t, testkit.Scratch(t))
	root := testkit.Scratch(t)
	testkit.WriteFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	const rel = "pkg/sample/sample.go"
	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), []byte(divideSource))
	testkit.WriteFile(t, filepath.Join(root, filepath.FromSlash("pkg/sample/sample_test.go")), []byte(divideTest))

	candidates := editsIn(t, []byte(divideSource),
		editSpec{rule: "return-err-to-nil", in: "return 0, ErrZero", find: "ErrZero", with: "nil"},
		editSpec{rule: "return-zero-numeric", in: "return a / b, nil", find: "a / b", with: "0"},
	)
	for i := range candidates {
		candidates[i].Path = rel
	}
	catalog := catalogOf(t, candidates)
	probeSnapshotHinted(t, root, catalog,
		hintsOfCandidates(t, rel, []byte(divideSource), candidates, hintOptions{}))

	swallowed := mutantOf(t, catalog, "return-err-to-nil", "nil").Index
	zeroed := mutantOf(t, catalog, "return-zero-numeric", "0").Index
	for _, c := range []struct {
		name string
		test string
		want []uint32
	}{{
		name: "both paths",
		test: "TestBoth",
		want: []uint32{swallowed, zeroed},
	}, {
		name: "only the values the constants already are",
		test: "TestNothingDiffers",
		want: nil,
	}} {
		t.Run(c.name, func(t *testing.T) {
			log := filepath.Join(testkit.Scratch(t), "infection.log")
			suite := goCommand(t, toolchain, root, append(slices.Clip(env), instrument.ProbeEnv+"="+log),
				"test", "-count=1", "-run", c.test, "./pkg/sample")
			mutantkit.RequireExit(t, suite, 0, "running the probe tree's suite")

			got, readErr := instrument.ReadInfectionLog(
				bytes.NewReader(testkit.ReadFile(t, log)), catalog.Digest(), catalog.Len())
			if readErr != nil {
				t.Fatalf("reading the infection log: %v", readErr)
			}
			want := slices.Clone(c.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("the probe recorded %v, want %v", got, want)
			}
		})
	}
}

const divideSource = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package sample divides, or refuses to.
package sample

import "errors"

// ErrZero is what Div refuses a zero divisor with.
var ErrZero = errors.New("division by zero")

// Div divides a by b.
func Div(a, b int) (int, error) {
	if b == 0 {
		return 0, ErrZero
	}
	return a / b, nil
}
`

const divideTest = `// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package sample

import (
	"errors"
	"testing"
)

// TestBoth takes both paths, so both mutants' values differ at least once.
func TestBoth(t *testing.T) {
	if got, err := Div(6, 3); got != 2 || err != nil {
		t.Fatalf("Div(6, 3) = %d, %v", got, err)
	}
	if _, err := Div(1, 0); !errors.Is(err, ErrZero) {
		t.Fatalf("Div(1, 0) error = %v, want %v", err, ErrZero)
	}
}

// TestNothingDiffers only ever divides zero by something, so the count really
// is zero and the error really is nil: neither mutant's value differs, and a
// probe that recorded one would be licensing a test to be skipped that is the
// only one that could kill it.
func TestNothingDiffers(t *testing.T) {
	if got, err := Div(0, 5); got != 0 || err != nil {
		t.Fatalf("Div(0, 5) = %d, %v", got, err)
	}
}
`

func assertProbeWellFormed(t *testing.T, in, out []byte, catalog *mutation.Catalog) {
	t.Helper()

	if got, want := instrument.CountLines(out), instrument.CountLines(in); got != want {
		t.Errorf("the probe tree holds %d line breaks, the original holds %d", got, want)
	}
	whole := []instrument.Splice{{
		Span:        mutation.Span{StartByte: 0, EndByte: uint32(len(in))},
		Original:    in,
		Replacement: out,
	}}
	if !instrument.LinePreserving(whole) {
		t.Error("the rewrite is not line-preserving")
	}
	for _, m := range catalog.Mutants() {
		infect := fmt.Sprintf(".Infect(%d)", m.Index)
		differs := fmt.Sprintf(".Differs(%d, ", m.Index)
		if !bytes.Contains(out, []byte(infect)) && !bytes.Contains(out, []byte(differs)) {
			t.Errorf("mutant %s: nothing in the probe tree reports it", m.DisplayID)
		}
	}
}

func probeReachEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "delete-call-statement", in: "note(i)", with: ""},
		editSpec{rule: "delete-assignment", in: "total = total + i", with: ""},
		editSpec{rule: "add-to-sub", in: "total = total + i", find: "+", with: "-"},
	)
}

func probeValueEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "add-to-sub", in: "k := Kind(a + b)", find: "+", with: "-"},
		editSpec{rule: "mul-to-div", in: "total := a*b + a", find: "*", with: "/"},
		editSpec{rule: "add-to-sub", in: "total := a*b + a", find: "+", with: "-"},
	)
}

func probeBoolEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "and-to-or", in: "v > lo && v < hi", find: "&&", with: "||"},
		editSpec{rule: "gt-to-ge", in: "v > lo && v < hi", find: ">", with: ">="},
		editSpec{rule: "lt-to-le", in: "v > lo && v < hi", find: "<", with: "<="},
		editSpec{rule: "eq-to-neq", in: "return a == b", find: "==", with: "!="},
		editSpec{rule: "negate-condition", in: "return a == b", find: "a == b", with: "!(a == b)"},
	)
}

func probeStatementEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-zero-numeric", in: "return count, err", find: "count", with: "0"},
		editSpec{rule: "return-err-to-nil", in: "return count, err", find: "err", with: "nil"},
		editSpec{rule: "return-zero-numeric", in: "return total", find: "total", with: "0"},
	)
}

func probeNamedBoolEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-true", in: "return x > y", find: "x > y", with: "true"},
		editSpec{rule: "return-false", in: "return x > y", find: "x > y", with: "false"},
		editSpec{rule: "return-false", in: "return true", find: "true", with: "false"},
	)
}

func probeMultilineEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-true", in: "x >=\n\t\tlimit-\n\t\t\t1", with: "true"},
		editSpec{rule: "return-false", in: "x >=\n\t\tlimit-\n\t\t\t1", with: "false"},
		editSpec{rule: "return-true", in: "x <= // the limit is inclusive\n\t\tlimit", with: "true"},
	)
}

func probeTypedEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-zero-numeric", in: "func Half() float32 { return 1 }", find: "1", with: "0"},
		editSpec{rule: "return-zero-numeric", in: "func Count() (n Level) { return 1 }", find: "1", with: "0"},
		editSpec{rule: "return-empty-string", in: "{ return s }", find: "s", with: `""`},
	)
}

func probeNestedEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-err-to-nil", in: "\t}, err", find: "err", with: "nil"},
		editSpec{rule: "return-zero-numeric", in: "return a + b", find: "a + b", with: "0"},
	)
}

func probeNameEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-zero-numeric", in: "return a, __gm_r0", find: "a", with: "0"},
	)
}
