// SPDX-FileCopyrightText: 2026 go-mutants contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package instrument_test

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/P4suta/go-mutants/internal/gocmd"
	"github.com/P4suta/go-mutants/internal/instrument"
	"github.com/P4suta/go-mutants/internal/mutation"
)

// The probe tree's return form.
//
// A probe tree runs the original program and, for each mutant, records the
// first time the value at its site differed from the constant the mutant would
// have returned. The rewrite that says so for a `return` is
//
//	{ var r0 T0 = E0; var r1 T1 = E1; …; if r1 != K { __gm.Infect(i) }; return r0, r1, … }
//
// and everything below is about the two things that have to be true of it: it
// is the original program — every operand evaluated once, in order, converted
// to the declared result type it was always converted to — and it fits on the
// line the statement started on.

// probeSnapshotHinted runs the instrumenter over a snapshot in probe mode with
// hints the caller assembled, and fails the test if it refuses.
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

// probeSnapshotWith is [probeSnapshotHinted] for a snapshot whose hints are
// derived from the files it holds.
func probeSnapshotWith(
	t *testing.T,
	root string,
	catalog *mutation.Catalog,
	opts hintOptions,
) instrument.Result {
	t.Helper()
	return probeSnapshotHinted(t, root, catalog, hintsFor(t, root, catalog, opts))
}

// A probeCase is one fixture rendered in probe mode.
type probeCase struct {
	// name is the golden's base name, which is also the subtest's.
	name string
	// input is the fixture file the rewrite is composed from.
	input string
	// candidates is the fixture's catalogue. Unlike the mutant tree's fixtures
	// these are always stated, because a probe tree rewrites nothing for a
	// catalogue of comparisons and a golden of the original file would prove
	// nothing.
	candidates func(*testing.T, []byte) []mutation.Candidate
	// hints are what the fixture's guard hints need beyond its own syntax.
	hints hintOptions
	// sites is the expected number of probe sites, which is not the number of
	// mutants: two candidates of one `return` share one rewrite.
	sites int
	// extra asserts whatever else this fixture exists to prove.
	extra func(t *testing.T, in, out []byte)
}

// probeCases is every fixture with a probe golden.
//
// Between them they cover each thing the rewrite has to get right: a statement
// with two results and two mutants, a named result type and a boolean one,
// operands spanning lines, a result type the operand does not have, a probe
// site nested inside another, and a file that has already taken the names the
// temporaries want.
func probeCases() []probeCase {
	return []probeCase{{
		name:       "probe-statement",
		input:      "statement.input",
		candidates: probeStatementEdits,
		sites:      2,
		extra: func(t *testing.T, _, out []byte) {
			// One statement, two results, two mutants: one block, one
			// temporary per result whatever is mutated, and one `if` per
			// mutant. The error is compared with nil and the count with zero,
			// each against the constant its own rule would have returned.
			assertContains(t, out, "{ var __gm_r0 int = count; var __gm_r1 error = err; "+
				"if __gm_r0 != 0 { __gm.Infect(0) }; if __gm_r1 != nil { __gm.Infect(1) }; "+
				"return __gm_r0, __gm_r1 }")
			// A single result declares one temporary and returns it.
			assertContains(t, out, "{ var __gm_r0 int = total; if __gm_r0 != 0 { __gm.Infect(2) }; return __gm_r0 }")
			// No mutant is ever active in a probe tree, so nothing in it reads
			// an activation flag and nothing carries a mutated copy.
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
			// The declared result type is a named boolean, so that is what the
			// temporary is declared as — and both constants compare against it
			// without a conversion anybody has to write. Two mutants of one
			// result are two `if` lines over one temporary, in catalogue order.
			assertContains(t, out, "{ var __gm_r0 Flag = x>y; if __gm_r0 != true { __gm.Infect(0) }; "+
				"if __gm_r0 != false { __gm.Infect(1) }; return __gm_r0 }")
		},
	}, {
		name:       "probe-multiline",
		input:      "multiline.input",
		candidates: probeMultilineEdits,
		sites:      2,
		extra: func(t *testing.T, in, out []byte) {
			// The whole rewrite is on the statement's first line and the lines
			// it used to occupy are left empty, so every byte after it keeps
			// its line. Lines 6 (the package clause, which took the import),
			// 11 and 19 are the ones written; 12, 13 and 20 are the emptied
			// remainder of the two statements.
			assertLinesUntouched(t, in, out, 5, 10, 11, 12, 18, 19)
			// The operand is folded onto that line by the flattener, which
			// cannot keep a line comment — there is no second branch here
			// holding the original bytes, so this is the one place the probe
			// tree is not the user's own text.
			assertContains(t, out, "var __gm_r0 bool = x<=limit;")
		},
	}, {
		name:       "probe-typed",
		input:      "probe-typed.input",
		candidates: probeTypedEdits,
		sites:      3,
		extra: func(t *testing.T, _, out []byte) {
			// The declared result type and not the operand's: an untyped
			// constant returned as a float32 is declared float32, which is the
			// conversion the `return` itself performs.
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
			// The inner rewrite is composed first and folded into the operand
			// that holds it, which is then folded onto one line: one statement,
			// one block, two Infect calls, and the literal's own `return`
			// rewritten inside it.
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
			// The file binds __gm_r0 and __gm_r1 already, so the temporaries
			// bump past both. The second operand still reads the package-level
			// __gm_r0 rather than the first temporary, which is the whole point
			// of checking: a name declared by the first `var` is in scope for
			// the second one's initialiser.
			assertContains(t, out, "{ var __gm_r1_0 int = a; var __gm_r1_1 int = __gm_r0; "+
				"if __gm_r1_0 != 0 { __gm.Infect(0) }; return __gm_r1_0, __gm_r1_1 }")
			if bytes.Contains(out, []byte("var __gm_r0 int =")) || bytes.Contains(out, []byte("var __gm_r1 int =")) {
				t.Errorf("the rewrite declared a temporary the file had already bound:\n%s", out)
			}
		},
	}}
}

// TestProbeGolden pins the probe tree of every shape the return form has to
// handle.
//
// Byte-exact fixtures for the same reason the mutant tree has them: the output
// has to compile, preserve lines, and change nothing it did not mean to, and a
// test that re-derived what it expected would re-derive the same mistake. The
// difference here is that there is no branch holding the original bytes — the
// probe *is* the original program, rewritten — so the fixture is the only place
// a reader can check that claim by eye.
func TestProbeGolden(t *testing.T) {
	t.Parallel()

	for _, c := range probeCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := readFile(t, filepath.Join("testdata", c.input))
			root := t.TempDir()
			writeFile(t, filepath.Join(root, sampleFile), in)

			catalog := catalogOf(t, c.candidates(t, in))
			result := probeSnapshotWith(t, root, catalog, c.hints)
			out := readFile(t, filepath.Join(root, sampleFile))

			golden := filepath.Join("testdata", c.name+".golden")
			if *updateGolden {
				writeFile(t, golden, out)
			}
			if want := readFile(t, golden); !bytes.Equal(out, want) {
				t.Errorf("the probe tree of %s does not match its fixture\n--- got ---\n%s\n--- want ---\n%s",
					c.name, out, want)
			}

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

// TestProbeTreePreservesLines is the invariant every fixture holds at once:
// line N of the probe tree is line N of the file the user wrote, wherever the
// probe did not write.
//
// It is the sharp form of the equal-line-count check. The rewrite folds a
// statement onto its own first line and leaves the rest of its lines empty, so
// the only lines that may differ are the ones the statement occupied and the
// one the runtime import was injected on — and a coverage record, a panic
// trace, and a reported mutant coordinate all rest on that being the whole
// list.
func TestProbeTreePreservesLines(t *testing.T) {
	t.Parallel()

	for _, c := range probeCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := readFile(t, filepath.Join("testdata", c.input))
			root := t.TempDir()
			writeFile(t, filepath.Join(root, sampleFile), in)

			catalog := catalogOf(t, c.candidates(t, in))
			hints := hintsFor(t, root, catalog, c.hints)
			probeSnapshotHinted(t, root, catalog, hints)
			out := readFile(t, filepath.Join(root, sampleFile))

			if got, want := instrument.CountLines(out), instrument.CountLines(in); got != want {
				t.Fatalf("the probe tree holds %d line breaks, the original holds %d", got, want)
			}
			assertLinesUntouched(t, in, out, probeWrittenLines(t, in, out, catalog, hints)...)
		})
	}
}

// probeWrittenLines is every line a probe pass is allowed to have written: the
// lines each rewritten statement occupied, and the one carrying the injected
// import.
func probeWrittenLines(
	t *testing.T,
	in, out []byte,
	catalog *mutation.Catalog,
	hints instrument.Hints,
) []int {
	t.Helper()

	written := make(map[int]bool)
	for _, m := range catalog.Mutants() {
		site := hints[m.ID].Return
		if site == nil {
			continue
		}
		first := instrument.CountLines(in[:site.Span.StartByte])
		last := instrument.CountLines(in[:site.Span.EndByte])
		for line := first; line <= last; line++ {
			written[line] = true
		}
	}
	// The import is an insertion holding no line break, so it lands on a line
	// that already existed; it is found rather than computed because which line
	// that is depends on the shape of the file's import section.
	for i, line := range lines(out) {
		if strings.Contains(line, testModule+"/gomutants_rt") {
			written[i] = true
		}
	}
	return slices.Sorted(maps.Keys(written))
}

// TestProbeModeLeavesTheMutantGoldensAlone is the compatibility claim this
// change has to keep: adding a second tree changed nothing about the first.
//
// Every fixture is rendered in both modes from one catalogue and one set of
// hints. The mutant tree's bytes are compared against the golden that was
// committed before the probe form existed, and the probe tree is rendered
// beside it only to prove that doing so does not disturb it — a shared site
// index, a shared alias, a shared splicer, and one mode field between them.
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

			in := readFile(t, filepath.Join("testdata", c.name+".input"))
			catalog := catalogOf(t, candidatesFor(t, c.candidates, in))

			render := func(mode instrument.Mode) []byte {
				root := t.TempDir()
				writeFile(t, filepath.Join(root, sampleFile), in)
				if c.sibling != "" {
					writeFile(t, filepath.Join(root, c.sibling),
						readFile(t, filepath.Join("testdata", c.name+".sibling")))
				}
				hints := hintsFor(t, root, catalog, c.hints)
				if mode == instrument.ModeProbe {
					probeSnapshotHinted(t, root, catalog, hints)
				} else {
					instrumentSnapshotHinted(t, root, catalog, hints)
				}
				return readFile(t, filepath.Join(root, sampleFile))
			}

			probed := render(instrument.ModeProbe)
			if got, want := render(instrument.ModeMutant), readFile(t, filepath.Join("testdata", c.name+".golden")); !bytes.Equal(got, want) {
				t.Errorf("the mutant tree of %s changed\n--- got ---\n%s\n--- want ---\n%s", c.name, got, want)
			}
			if bytes.Contains(probed, []byte(".M[")) {
				t.Errorf("the probe tree of %s reads an activation flag:\n%s", c.name, probed)
			}
		})
	}
}

// TestProbeSkipsAMutantWithoutAReturnSite pins what a probe tree does with the
// families whose probe form is not written yet.
//
// It does not probe them, and it does not touch the file they are in. A file
// rewritten for no site would carry an import nothing uses, which does not
// compile; a file rewritten with a guard would be a mutant tree pretending to
// be a probe one. Doing nothing is the only answer that is honest about the
// mutant simply not being measured.
func TestProbeSkipsAMutantWithoutAReturnSite(t *testing.T) {
	t.Parallel()

	in := readFile(t, filepath.Join("testdata", "comparison.input"))
	root := t.TempDir()
	writeFile(t, filepath.Join(root, sampleFile), in)

	catalog := catalogOf(t, candidatesFor(t, nil, in))
	result := probeSnapshotWith(t, root, catalog, hintOptions{})

	if got := readFile(t, filepath.Join(root, sampleFile)); !bytes.Equal(got, in) {
		t.Errorf("a file whose every mutant is unprobed was rewritten:\n%s", got)
	}
	if len(result.FilesInstrumented) != 0 || len(result.GuardsByFile) != 0 {
		t.Errorf("probe mode reported FilesInstrumented=%v GuardsByFile=%v, want neither",
			result.FilesInstrumented, result.GuardsByFile)
	}
}

// TestProbeRefusesAnUnspellableResultType is the same refusal one statement at
// a time.
//
// Discovery hands down no site for a `return` whose result types it cannot
// spell in the file, and the instrumenter's answer has to be to leave that
// statement exactly as it is while its neighbours are still probed. Anything
// else would mean one unspellable type costing a whole file its measurement.
func TestProbeRefusesAnUnspellableResultType(t *testing.T) {
	t.Parallel()

	in := readFile(t, filepath.Join("testdata", "statement.input"))
	root := t.TempDir()
	writeFile(t, filepath.Join(root, sampleFile), in)

	catalog := catalogOf(t, probeStatementEdits(t, in))
	result := probeSnapshotWith(t, root, catalog, hintOptions{unprobed: []string{"return count, err"}})
	out := readFile(t, filepath.Join(root, sampleFile))

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

// TestProbeIsDeterministic is the probe tree's half of the promise the mutant
// tree makes: the same snapshot and the same catalogue produce the same bytes,
// down to the alias, the temporaries, and the order of the Infect calls.
func TestProbeIsDeterministic(t *testing.T) {
	t.Parallel()

	for _, c := range probeCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			in := readFile(t, filepath.Join("testdata", c.input))
			catalog := catalogOf(t, c.candidates(t, in))

			run := func() (string, []byte, []byte) {
				root := t.TempDir()
				writeFile(t, filepath.Join(root, sampleFile), in)
				result := probeSnapshotWith(t, root, catalog, c.hints)
				runtime := readFile(t, filepath.Join(root, result.RuntimeDir, result.RuntimeDir+".go"))
				return result.RuntimeImport, readFile(t, filepath.Join(root, sampleFile)), runtime
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

// TestProbeTreeCompiles builds every probe fixture against a real toolchain.
//
// The goldens prove the bytes are what they were yesterday; only the compiler
// proves they are Go. Everything the return form promises is a typing claim —
// that a temporary of the declared result type accepts the operand, that the
// constant compares against it, that a block ending in `return` is still a
// terminating statement, that the temporaries collide with nothing — and each
// of those is invisible to a parse and to a byte-exact fixture.
func TestProbeTreeCompiles(t *testing.T) {
	t.Parallel()

	toolchain := locateToolchain(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), []byte(goModule))

	var candidates []mutation.Candidate
	hints := instrument.Hints{}
	for _, c := range probeCases() {
		src := readFile(t, filepath.Join("testdata", c.input))
		rel := "pkg/" + c.name + "/sample.go"
		writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), src)

		here := c.candidates(t, src)
		for i := range here {
			here[i].Path = rel
		}
		candidates = append(candidates, here...)
		maps.Copy(hints, hintsOfCandidates(t, rel, src, here, c.hints))
	}
	probeSnapshotHinted(t, root, catalogOf(t, candidates), hints)

	if out, err := goCommand(t, toolchain, root, "build", "./..."); err != nil {
		t.Errorf("the probe tree does not build: %v\n%s", err, out)
	}
}

// TestProbeCapturesEveryResultOfAReturn runs a probe tree and reads back what
// it recorded, which is the only place the whole mechanism is visible at once.
//
// Div returns `(0, ErrZero)` for a zero divisor and `(a/b, nil)` otherwise, and
// each of its two mutants is observable through exactly one of those paths. A
// suite that takes both records both; a suite that only ever divides zero by
// something records neither, because the count really is zero and the error
// really is nil — which is the fact the whole probe pass exists to establish,
// and the one that licenses not running a test against a mutant.
func TestProbeCapturesEveryResultOfAReturn(t *testing.T) {
	t.Parallel()

	toolchain := locateToolchain(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), []byte(goModule))
	const rel = "pkg/sample/sample.go"
	writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), []byte(divideSource))
	writeFile(t, filepath.Join(root, filepath.FromSlash("pkg/sample/sample_test.go")), []byte(divideTest))

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
			log := filepath.Join(t.TempDir(), "infection.log")
			out, err := goCommandWithEnv(t, toolchain, root,
				[]string{instrument.ProbeEnv + "=" + log},
				"test", "-count=1", "-run", c.test, "./pkg/sample")
			if err != nil {
				t.Fatalf("running the probe tree's suite: %v\n%s", err, out)
			}

			got, readErr := instrument.ReadInfectionLog(
				bytes.NewReader(readFile(t, log)), catalog.Digest(), catalog.Len())
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

// divideSource is the package the probe fixture measures: one function, two
// returns, and one mutant reachable through each.
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

// divideTest is the suite the probe tree runs, in two halves so that a test can
// ask for one path at a time.
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

// assertProbeWellFormed runs the invariants every probe tree must hold,
// whatever the fixture.
//
// The mutant tree's [assertWellFormed] asks a question this one cannot: it
// checks that every mutant's own bytes are still on the line they were written
// on, because a guard keeps the original in one of its branches. A probe tree
// keeps no branch — the statement is rewritten into the program it always was,
// with its operands folded onto one line — so what is asserted here is that the
// rewrite parses, holds its lines, and calls Infect once per mutant.
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
		call := fmt.Sprintf("__gm.Infect(%d)", m.Index)
		if !bytes.Contains(out, []byte(call)) && !bytes.Contains(out, []byte(fmt.Sprintf(".Infect(%d)", m.Index))) {
			t.Errorf("mutant %s: nothing in the probe tree reports it", m.DisplayID)
		}
	}
}

// goCommandWithEnv is [goCommand] with extra environment for the child.
//
// It is spelled out rather than folded into that helper because the two are
// asking for different things: goCommand pins the environment so that the
// developer's shell cannot decide what a build resolves against, and this adds
// the one variable a probe run is *about*. Everything else is the same pinning,
// for the same reasons, stated there.
func goCommandWithEnv(t *testing.T, toolchain gocmd.Toolchain, dir string, extra []string, args ...string) (string, error) {
	t.Helper()

	cmd := exec.Command(toolchain.GoBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod -buildvcs=false", "GOPROXY=off")
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The catalogues of the probe fixtures. Each states the rule, the bytes it
// replaces located by a snippet that holds them, and what it writes — exactly
// as internal/discover's return-value family would have proposed them.

// probeStatementEdits catalogues the statement fixture: a `return` carrying two
// families at once over two results, and a single-result `return` beside it.
func probeStatementEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-zero-numeric", in: "return count, err", find: "count", with: "0"},
		editSpec{rule: "return-err-to-nil", in: "return count, err", find: "err", with: "nil"},
		editSpec{rule: "return-zero-numeric", in: "return total", find: "total", with: "0"},
	)
}

// probeNamedBoolEdits catalogues the named boolean fixture: the two boolean
// replacements of one result, whose declared type is not the universe bool.
func probeNamedBoolEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-true", in: "return x > y", find: "x > y", with: "true"},
		editSpec{rule: "return-false", in: "return x > y", find: "x > y", with: "false"},
		editSpec{rule: "return-false", in: "return true", find: "true", with: "false"},
	)
}

// probeMultilineEdits catalogues the multi-line fixture: operands written
// across lines, one of them with a comment inside it.
func probeMultilineEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-true", in: "x >=\n\t\tlimit-\n\t\t\t1", with: "true"},
		editSpec{rule: "return-false", in: "x >=\n\t\tlimit-\n\t\t\t1", with: "false"},
		editSpec{rule: "return-true", in: "x <= // the limit is inclusive\n\t\tlimit", with: "true"},
	)
}

// probeTypedEdits catalogues the fixture whose results are not the types of
// their operands.
func probeTypedEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-zero-numeric", in: "func Half() float32 { return 1 }", find: "1", with: "0"},
		editSpec{rule: "return-zero-numeric", in: "func Count() (n Level) { return 1 }", find: "1", with: "0"},
		editSpec{rule: "return-empty-string", in: "{ return s }", find: "s", with: `""`},
	)
}

// probeNestedEdits catalogues the nested fixture: the outer return's error and
// the literal's own return, which sits inside the operand beside it.
func probeNestedEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-err-to-nil", in: "\t}, err", find: "err", with: "nil"},
		editSpec{rule: "return-zero-numeric", in: "return a + b", find: "a + b", with: "0"},
	)
}

// probeNameEdits catalogues the fixture whose file has taken the temporaries'
// names.
func probeNameEdits(t *testing.T, src []byte) []mutation.Candidate {
	t.Helper()
	return editsIn(t, src,
		editSpec{rule: "return-zero-numeric", in: "return a, __gm_r0", find: "a", with: "0"},
	)
}
